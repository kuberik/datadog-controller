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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"

	datadoghqcomv1alpha1 "github.com/DataDog/datadog-operator/apis/datadoghq/v1alpha1"
	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

var _ = Describe("DatadogMonitor Controller", func() {
	Context("When reconciling a DatadogMonitor", func() {
		const resourceName = "test-datadogmonitor"

		var namespace string
		var typeNamespacedName types.NamespacedName
		var datadogMonitor *datadoghqcomv1alpha1.DatadogMonitor
		var testRunID string

		JustBeforeEach(func() {
			ctx := context.Background()
			By("creating a unique namespace for the test")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-ns-",
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			namespace = ns.Name
			testRunID = namespace

			By("setting up the test environment")
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}

			By("creating a DatadogMonitor with health-check annotation")
			datadogMonitor = &datadoghqcomv1alpha1.DatadogMonitor{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
					Labels: map[string]string{
						"app":         "test-app",
						"test-run":    testRunID,
						"environment": "test",
						"team":        "platform",
					},
					Annotations: map[string]string{
						AnnotationKeyHealthCheck:            AnnotationValueHealthCheck,
						"kuberik.com/description":           "Test monitor for health check",
						"monitoring.kubernetes.io/severity": "warning",
					},
				},
				Spec: datadoghqcomv1alpha1.DatadogMonitorSpec{
					Name:  "Test Monitor",
					Type:  "metric alert",
					Query: "avg(last_5m):avg:system.cpu.user{*} > 80",
				},
			}
			Expect(k8sClient.Create(ctx, datadogMonitor)).To(Succeed())
		})

		AfterEach(func() {
			ctx := context.Background()
			By("Cleaning up the test namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
		})

		It("should create a HealthCheck when DatadogMonitor has health-check annotation", func() {
			ctx := context.Background()
			By("Reconciling the DatadogMonitor")
			controllerReconciler := &DatadogMonitorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck was created")
			healthCheckName := "datadog-check-" + resourceName
			healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying HealthCheck metadata")
			Expect(healthCheck.Name).To(Equal(healthCheckName))
			Expect(healthCheck.Namespace).To(Equal(namespace))
			Expect(healthCheck.Labels["kuberik.com/datadog-monitor"]).To(Equal(resourceName))
			Expect(healthCheck.Labels["kuberik.com/managed-by"]).To(Equal("datadog-controller"))
			Expect(healthCheck.Annotations["kuberik.com/datadog-monitor-name"]).To(Equal(resourceName))
			Expect(healthCheck.Annotations["kuberik.com/datadog-monitor-uid"]).To(Equal(string(datadogMonitor.UID)))

			By("Verifying that all monitor labels were copied")
			Expect(healthCheck.Labels["app"]).To(Equal("test-app"))
			Expect(healthCheck.Labels["test-run"]).To(Equal(testRunID))
			Expect(healthCheck.Labels["environment"]).To(Equal("test"))
			Expect(healthCheck.Labels["team"]).To(Equal("platform"))

			By("Verifying that all monitor annotations were copied (except internal ones)")
			Expect(healthCheck.Annotations["kuberik.com/description"]).To(Equal("Test monitor for health check"))
			Expect(healthCheck.Annotations["monitoring.kubernetes.io/severity"]).To(Equal("warning"))
			Expect(healthCheck.Annotations[AnnotationKeyHealthCheck]).To(BeEmpty()) // Should not be copied

			By("Verifying owner reference")
			Expect(healthCheck.OwnerReferences).To(HaveLen(1))
			Expect(healthCheck.OwnerReferences[0].Kind).To(Equal("DatadogMonitor"))
			Expect(healthCheck.OwnerReferences[0].Name).To(Equal(resourceName))
			Expect(healthCheck.OwnerReferences[0].UID).To(Equal(datadogMonitor.UID))
		})

		It("should not create a HealthCheck when DatadogMonitor lacks health-check annotation", func() {
			ctx := context.Background()
			By("removing the health-check annotation")
			datadogMonitor.Annotations = nil
			Expect(k8sClient.Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the DatadogMonitor")
			controllerReconciler := &DatadogMonitorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that no HealthCheck was created")
			healthCheckName := "datadog-check-" + resourceName
			healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).To(HaveOccurred())
		})

		It("should delete HealthCheck when health-check annotation is removed", func() {
			ctx := context.Background()
			By("Reconciling the DatadogMonitor to create HealthCheck")
			controllerReconciler := &DatadogMonitorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck was created")
			healthCheckName := "datadog-check-" + resourceName
			healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).NotTo(HaveOccurred())

			By("removing the health-check annotation")
			datadogMonitor.Annotations = nil
			Expect(k8sClient.Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the DatadogMonitor again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck was deleted")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).To(HaveOccurred())
		})

		It("should create HealthCheck when health-check annotation is added", func() {
			ctx := context.Background()
			By("removing the health-check annotation")
			datadogMonitor.Annotations = nil
			Expect(k8sClient.Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the DatadogMonitor without annotation")
			controllerReconciler := &DatadogMonitorReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that no HealthCheck was created")
			healthCheckName := "datadog-check-" + resourceName
			healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).To(HaveOccurred())

			By("adding the health-check annotation back")
			datadogMonitor.Annotations = map[string]string{
				AnnotationKeyHealthCheck: AnnotationValueHealthCheck,
			}
			Expect(k8sClient.Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the DatadogMonitor with annotation")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck was created")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).NotTo(HaveOccurred())
			Expect(healthCheck.Name).To(Equal(healthCheckName))
		})
	})
})
