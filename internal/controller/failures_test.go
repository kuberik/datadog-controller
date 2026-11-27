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
	"testing"
	"time"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadogV2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

func i64(v int64) *int64 { return &v }

func TestSeverityFromMonitorPriority(t *testing.T) {
	cases := []struct {
		in   *int64
		want kuberikrolloutv1alpha1.FailureSeverity
	}{
		{i64(1), "Critical"}, {i64(2), "Critical"}, {i64(3), "Warning"}, {i64(4), "Warning"}, {i64(5), "Warning"},
		{nil, "Critical"}, {i64(0), "Critical"}, {i64(9), "Critical"},
	}
	for _, c := range cases {
		if got := severityFromMonitorPriority(c.in); got != c.want {
			t.Errorf("%v: got %s want %s", c.in, got, c.want)
		}
	}
}

func TestSeverityFromIncident(t *testing.T) {
	cases := map[string]kuberikrolloutv1alpha1.FailureSeverity{
		"SEV-1": "Critical", "sev-2": "Critical", "SEV-3": "Warning", "SEV-4": "Warning", "SEV-5": "Warning",
		"UNKNOWN": "Critical", "": "Critical",
	}
	for in, want := range cases {
		if got := severityFromIncident(in); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}

func TestIncidentSeverityReadsFieldThenLegacyAttribute(t *testing.T) {
	sev := datadogV2.INCIDENTSEVERITY_SEV_3
	if got := incidentSeverity(&datadogV2.IncidentResponseAttributes{Severity: &sev}); got != "SEV-3" {
		t.Errorf("legacy attribute: got %q", got)
	}
	single := datadogV2.NewIncidentFieldAttributesSingleValue()
	single.SetValue("SEV-1")
	attrs := &datadogV2.IncidentResponseAttributes{
		Severity: &sev,
		Fields:   map[string]datadogV2.IncidentFieldAttributes{"severity": {IncidentFieldAttributesSingleValue: single}},
	}
	if got := incidentSeverity(attrs); got != "SEV-1" {
		t.Errorf("severity field must win: got %q", got)
	}
	if got := incidentSeverity(&datadogV2.IncidentResponseAttributes{}); got != "" {
		t.Errorf("unset: got %q", got)
	}
}

func TestSeverityOverrideAnnotation(t *testing.T) {
	mk := func() []kuberikrolloutv1alpha1.HealthCheckFailure {
		return []kuberikrolloutv1alpha1.HealthCheckFailure{{Severity: "Critical"}, {Severity: "Warning"}}
	}
	ann := func(v string) map[string]string {
		return map[string]string{kuberikrolloutv1alpha1.SeverityAnnotation: v}
	}
	for _, want := range []kuberikrolloutv1alpha1.FailureSeverity{"Critical", "Warning"} {
		for _, f := range applySeverityOverride(mk(), ann(string(want))) {
			if f.Severity != want {
				t.Errorf("override %s not applied: %+v", want, f)
			}
		}
	}
	got := applySeverityOverride(mk(), ann("bogus"))
	if got[0].Severity != "Critical" || got[1].Severity != "Critical" {
		t.Errorf("an unknown annotation value counts as Critical: %+v", got)
	}
	got = applySeverityOverride(mk(), nil)
	if got[1].Severity != "Warning" {
		t.Errorf("no annotation must keep per-alert severity: %+v", got)
	}
}

func TestMonitorFailuresNamingAndDisambiguation(t *testing.T) {
	now := metav1.Now()
	fs := []kuberikrolloutv1alpha1.HealthCheckFailure{
		alertingMonitorFailure("latency", 7, i64(3), []string{"service:b", "service:a"}, now),
		alertingMonitorFailure("latency", 9, nil, nil, now),
		alertingMonitorFailure("errors", 11, i64(1), nil, now),
	}
	disambiguateSources(fs, []int64{7, 9, 11})
	wantSrc := []string{"Alert latency (#7)", "Alert latency (#9)", "Alert errors"}
	wantSev := []kuberikrolloutv1alpha1.FailureSeverity{"Warning", "Critical", "Critical"}
	for i := range fs {
		if fs[i].Source != wantSrc[i] || fs[i].Severity != wantSev[i] || fs[i].Kind != kuberikrolloutv1alpha1.FailureKindAlert {
			t.Errorf("%d: %+v", i, fs[i])
		}
	}
	if fs[0].Message != "Monitor alerting for service:a; service:b" {
		t.Errorf("message %q", fs[0].Message)
	}
	inc := incidentFailure(datadogIncident{PublicID: 412, Title: "Checkout errors", Severity: "SEV-4"}, now)
	if inc.Source != "Incident #412 Checkout errors" || inc.Kind != kuberikrolloutv1alpha1.FailureKindIncident || inc.Severity != "Warning" {
		t.Errorf("incident %+v", inc)
	}
}

func TestMergeFailuresWitness(t *testing.T) {
	t0 := metav1.NewTime(time.Now().Add(-time.Hour))
	t1 := metav1.Now()
	rec := []kuberikrolloutv1alpha1.HealthCheckFailure{{Source: "Alert A", Kind: "Alert", Severity: "Warning", FirstSeenTime: t0}}
	out := mergeFailures(rec, []kuberikrolloutv1alpha1.HealthCheckFailure{
		{Source: "Alert A", Kind: "Alert", Severity: "Critical", FirstSeenTime: t1},
		{Source: "Alert B", Kind: "Alert", Severity: "Critical", FirstSeenTime: t1},
	})
	if len(out) != 2 || !out[0].FirstSeenTime.Equal(&t0) || out[0].Severity != "Critical" || out[1].Source != "Alert B" {
		t.Fatalf("unexpected merge: %+v", out)
	}
	if out = mergeFailures(out, nil); len(out) != 2 {
		t.Fatalf("recovery must keep witness: %+v", out)
	}
	var many []kuberikrolloutv1alpha1.HealthCheckFailure
	for i := 0; i < 20; i++ {
		many = append(many, kuberikrolloutv1alpha1.HealthCheckFailure{Source: "Alert " + string(rune('a'+i)), Kind: "Alert", FirstSeenTime: t1})
	}
	if got := mergeFailures(nil, many); len(got) != maxFailures {
		t.Fatalf("cap: got %d", len(got))
	}
}

var _ = Describe("Datadog failures witness", func() {
	var namespace string

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "datadog-fail-"}}
		Expect(k8sClient.Create(context.Background(), ns)).To(Succeed())
		namespace = ns.Name
	})

	reconcileOnce := func(hc *kuberikrolloutv1alpha1.HealthCheck, api datadogAPI) {
		r := &HealthCheckReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), DatadogAPI: api}
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(hc)})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(hc), hc)).To(Succeed())
	}
	create := func(name, class string, ann map[string]string) *kuberikrolloutv1alpha1.HealthCheck {
		hc := &kuberikrolloutv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: ann},
			Spec:       kuberikrolloutv1alpha1.HealthCheckSpec{Class: &class},
		}
		Expect(k8sClient.Create(context.Background(), hc)).To(Succeed())
		return hc
	}

	It("records alerting monitors with priority severity and keeps them after recovery", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{
			1: {ID: 1, Name: "checkout latency", OverallState: "Alert", Priority: i64(1)},
			2: {ID: 2, Name: "queue depth", OverallState: "Alert", Priority: i64(4)},
			3: {ID: 3, Name: "healthy", OverallState: "OK"},
		}}
		hc := create("monitors", HealthCheckClassDatadog, map[string]string{AnnotationDatadogMonitorIDs: "1,2,3"})
		reconcileOnce(hc, api)
		Expect(hc.Status.Failures).To(HaveLen(2))
		Expect(hc.Status.Failures[0].Source).To(Equal("Alert checkout latency"))
		Expect(hc.Status.Failures[0].Kind).To(Equal(kuberikrolloutv1alpha1.FailureKindAlert))
		Expect(hc.Status.Failures[0].Severity).To(Equal(kuberikrolloutv1alpha1.FailureSeverityCritical))
		Expect(hc.Status.Failures[1].Source).To(Equal("Alert queue depth"))
		Expect(hc.Status.Failures[1].Severity).To(Equal(kuberikrolloutv1alpha1.FailureSeverityWarning))
		first := hc.Status.Failures[0].FirstSeenTime

		By("recovery keeps the witness")
		api.monitors[1] = datadogMonitorState{ID: 1, Name: "checkout latency", OverallState: "OK"}
		api.monitors[2] = datadogMonitorState{ID: 2, Name: "queue depth", OverallState: "OK"}
		reconcileOnce(hc, api)
		Expect(hc.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusHealthy))
		Expect(hc.Status.Failures).To(HaveLen(2))
		Expect(hc.Status.Failures[0].FirstSeenTime.Equal(&first)).To(BeTrue())
	})

	It("lets the kuberik.com/severity annotation override monitor severity", func() {
		api := &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{
			1: {ID: 1, Name: "p1", OverallState: "Alert", Priority: i64(1)}}}
		hc := create("override", HealthCheckClassDatadog, map[string]string{
			AnnotationDatadogMonitorIDs: "1", kuberikrolloutv1alpha1.SeverityAnnotation: "Warning"})
		reconcileOnce(hc, api)
		Expect(hc.Status.Failures).To(HaveLen(1))
		Expect(hc.Status.Failures[0].Severity).To(Equal(kuberikrolloutv1alpha1.FailureSeverityWarning))
	})

	It("records open incidents with Incident kind and severity", func() {
		api := &fakeDatadogAPI{incidents: []datadogIncident{
			{PublicID: 412, Title: "Checkout errors", Severity: "SEV-2"},
			{PublicID: 415, Title: "Slow reports", Severity: "SEV-4"}}}
		hc := create("incidents", HealthCheckClassDatadogIncidents, map[string]string{AnnotationDatadogIncidentQuery: "services:shop"})
		reconcileOnce(hc, api)
		Expect(hc.Status.Failures).To(HaveLen(2))
		Expect(hc.Status.Failures[0].Source).To(Equal("Incident #412 Checkout errors"))
		Expect(hc.Status.Failures[0].Kind).To(Equal(kuberikrolloutv1alpha1.FailureKindIncident))
		Expect(hc.Status.Failures[0].Severity).To(Equal(kuberikrolloutv1alpha1.FailureSeverityCritical))
		Expect(hc.Status.Failures[1].Severity).To(Equal(kuberikrolloutv1alpha1.FailureSeverityWarning))
	})

	It("does not record a failure when the check cannot be evaluated", func() {
		hc := create("check-error", HealthCheckClassDatadog, map[string]string{AnnotationDatadogMonitorIDs: "1"})
		reconcileOnce(hc, &fakeDatadogAPI{monitors: map[int64]datadogMonitorState{}})
		Expect(hc.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusPending))
		Expect(hc.Status.Failures).To(BeEmpty())
	})
})
