/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

type fakeDatadogAPI struct {
	monitors       map[int64]datadogMonitorState
	incidents      []datadogIncident
	incidentQuery  string
	listedWithTags []string
}

func (f *fakeDatadogAPI) GetMonitor(_ context.Context, id int64) (datadogMonitorState, error) {
	monitor, ok := f.monitors[id]
	if !ok {
		return datadogMonitorState{}, fmt.Errorf("monitor %d not found", id)
	}
	return monitor, nil
}

func (f *fakeDatadogAPI) ListMonitorsByTags(_ context.Context, tags []string) ([]datadogMonitorState, error) {
	f.listedWithTags = tags
	monitors := make([]datadogMonitorState, 0, len(f.monitors))
	for _, monitor := range f.monitors {
		monitors = append(monitors, monitor)
	}
	return monitors, nil
}

func (f *fakeDatadogAPI) SearchOpenIncidents(_ context.Context, query string) ([]datadogIncident, error) {
	f.incidentQuery = query
	return f.incidents, nil
}

var _ = Describe("Datadog class HealthChecks", func() {
	var namespace string

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "datadog-class-"}}
		Expect(k8sClient.Create(context.Background(), ns)).To(Succeed())
		namespace = ns.Name
	})

	AfterEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(k8sClient.Delete(context.Background(), ns)).To(Succeed())
	})

	createHealthCheck := func(name, class string, annotations map[string]string) client.ObjectKey {
		healthCheck := &kuberikrolloutv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: annotations},
			Spec:       kuberikrolloutv1alpha1.HealthCheckSpec{Class: &class},
		}
		Expect(k8sClient.Create(context.Background(), healthCheck)).To(Succeed())
		return client.ObjectKeyFromObject(healthCheck)
	}

	reconcileAndGet := func(api datadogAPI, key client.ObjectKey) *kuberikrolloutv1alpha1.HealthCheck {
		reconciler := &HealthCheckReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), DatadogAPI: api}
		result, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(defaultDatadogPollInterval))
		healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
		Expect(k8sClient.Get(context.Background(), key, healthCheck)).To(Succeed())
		return healthCheck
	}

	sharedSLOMonitor := datadogMonitorState{
		ID:           1,
		Name:         "HTTP success rate burn",
		Tags:         []string{"env:prod", "slo:http-success"},
		OverallState: monitorStatusAlert,
		Groups: map[string]string{
			"env:prod,service:api": monitorStatusAlert,
			"env:prod,service:db":  "OK",
		},
	}

	It("only evaluates the monitor groups matching the group tags", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{1: sharedSLOMonitor}}
		annotations := func(service string) map[string]string {
			return map[string]string{
				AnnotationDatadogMonitorTags: "slo:http-success, env:prod",
				AnnotationDatadogGroupTags:   "service:" + service + ",env:prod",
			}
		}

		db := reconcileAndGet(api, createHealthCheck("db", HealthCheckClassDatadog, annotations("db")))
		Expect(api.listedWithTags).To(Equal([]string{"slo:http-success", "env:prod"}))
		Expect(db.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusHealthy))
		Expect(db.Status.LastErrorTime).To(BeNil())

		apiCheck := reconcileAndGet(api, createHealthCheck("api", HealthCheckClassDatadog, annotations("api")))
		Expect(apiCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
		Expect(apiCheck.Status.LastErrorTime).NotTo(BeNil())
		Expect(*apiCheck.Status.Message).To(ContainSubstring("HTTP success rate burn [env:prod,service:api]"))
	})

	It("uses the overall state of monitors selected by ID without group state", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{
			7: {ID: 7, Name: "error rate", OverallState: "OK"},
		}}
		healthCheck := reconcileAndGet(api, createHealthCheck("by-id", HealthCheckClassDatadog,
			map[string]string{AnnotationDatadogMonitorIDs: "7"}))
		Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusHealthy))
		Expect(*healthCheck.Status.Message).To(Equal("1 Datadog monitors OK"))
	})

	It("is Healthy when no monitor carries the tags, since tagging is opt-in", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{}}
		healthCheck := reconcileAndGet(api, createHealthCheck("no-match", HealthCheckClassDatadog,
			map[string]string{AnnotationDatadogMonitorTags: "service:nope"}))
		Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusHealthy))
		Expect(*healthCheck.Status.Message).To(ContainSubstring("nothing to evaluate"))
	})

	It("stays Pending when an explicit monitor ID does not exist", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{}}
		healthCheck := reconcileAndGet(api, createHealthCheck("missing-id", HealthCheckClassDatadog,
			map[string]string{AnnotationDatadogMonitorIDs: "99"}))
		Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusPending))
		Expect(*healthCheck.Status.Message).To(ContainSubstring("monitor 99 not found"))
	})

	It("stamps a new lastErrorTime when an alert outlives a rollout reset", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{1: sharedSLOMonitor}}
		key := createHealthCheck("reset", HealthCheckClassDatadog, map[string]string{
			AnnotationDatadogMonitorIDs: "1",
			AnnotationDatadogGroupTags:  "service:api",
		})
		first := reconcileAndGet(api, key)
		Expect(first.Status.LastErrorTime).NotTo(BeNil())

		By("keeping the first failure time while it stays Unhealthy")
		again := reconcileAndGet(api, key)
		Expect(again.Status.LastErrorTime.Time).To(Equal(first.Status.LastErrorTime.Time))

		By("simulating the rollout controller resetting the HealthCheck")
		again.Status.Status = kuberikrolloutv1alpha1.HealthStatusPending
		again.Status.LastErrorTime = nil
		Expect(k8sClient.Status().Update(context.Background(), again)).To(Succeed())

		afterReset := reconcileAndGet(api, key)
		Expect(afterReset.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
		Expect(afterReset.Status.LastErrorTime).NotTo(BeNil())
	})

	It("is Unhealthy while a matching incident is open", func() {
		api := &fakeDatadogAPI{incidents: []datadogIncident{{PublicID: 42, Title: "API down"}}}
		healthCheck := reconcileAndGet(api, createHealthCheck("incidents", HealthCheckClassDatadogIncidents,
			map[string]string{AnnotationDatadogIncidentQuery: `services:api`}))
		Expect(api.incidentQuery).To(Equal("services:api"))
		Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
		Expect(*healthCheck.Status.Message).To(ContainSubstring("#42 API down"))

		api.incidents = nil
		healthCheck = reconcileAndGet(api, client.ObjectKeyFromObject(healthCheck))
		Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusHealthy))
		Expect(healthCheck.Status.LastErrorTime).NotTo(BeNil(), "lastErrorTime is kept after recovery")
	})

	It("stays Pending without Datadog credentials", func() {
		GinkgoT().Setenv(datadogAltAPIKeyEnvVar, "")
		GinkgoT().Setenv(datadogAPIKeyEnvVar, "")
		reconciler := &HealthCheckReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		key := createHealthCheck("no-creds", HealthCheckClassDatadog, map[string]string{AnnotationDatadogMonitorIDs: "1"})
		_, err := reconciler.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
		Expect(k8sClient.Get(context.Background(), key, healthCheck)).To(Succeed())
		Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusPending))
	})
})

var _ = DescribeTable("groupMatches",
	func(group string, groupTags []string, expected bool) {
		Expect(groupMatches(group, groupTags)).To(Equal(expected))
	},
	Entry("same value", "env:prod,service:api", []string{"service:api"}, true),
	Entry("different value", "env:prod,service:db", []string{"service:api"}, false),
	Entry("not grouped by the key", "resource_name:get_/x", []string{"service:api"}, true),
	Entry("ungrouped monitor", "*", []string{"service:api"}, true),
	Entry("no filter", "service:db", nil, true),
)
