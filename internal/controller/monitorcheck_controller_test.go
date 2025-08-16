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
	kuberikcomv1alpha1 "github.com/kuberik/datadog-controller/api/v1alpha1"
	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

var _ = Describe("MonitorCheck Controller", func() {
	Context("When reconciling a MonitorCheck", func() {
		const resourceName = "test-monitorcheck"

		var namespace string
		var typeNamespacedName types.NamespacedName
		var monitorCheck *kuberikcomv1alpha1.MonitorCheck
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

			By("creating a DatadogMonitor")
			datadogMonitor = &datadoghqcomv1alpha1.DatadogMonitor{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-datadog-monitor",
					Namespace: namespace,
					Labels: map[string]string{
						"app":      "test-app",
						"test-run": testRunID,
					},
				},
				Spec: datadoghqcomv1alpha1.DatadogMonitorSpec{
					Name: "test-monitor",
				},
			}
			Expect(k8sClient.Create(ctx, datadogMonitor)).To(Succeed())

			By("setting up DatadogMonitor status")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateOK
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("creating the MonitorCheck")
			monitorCheck = &kuberikcomv1alpha1.MonitorCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: kuberikcomv1alpha1.MonitorCheckSpec{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app":      "test-app",
							"test-run": testRunID,
						},
					},
					HealthCheckTemplate: kuberikcomv1alpha1.HealthCheckTemplate{
						Labels: map[string]string{
							"app":      "test-app",
							"test-run": testRunID,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, monitorCheck)).To(Succeed())
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

		It("should create a HealthCheck when MonitorCheck is created", func() {
			ctx := context.Background()
			By("Reconciling the MonitorCheck")
			controllerReconciler := &MonitorCheckReconciler{
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
			Expect(healthCheck.Labels["app"]).To(Equal("test-app"))

			By("Verifying owner reference")
			Expect(healthCheck.OwnerReferences).To(HaveLen(1))
			Expect(healthCheck.OwnerReferences[0].Kind).To(Equal("MonitorCheck"))
			Expect(healthCheck.OwnerReferences[0].Name).To(Equal(resourceName))
		})

		It("should update MonitorCheck status with monitor counts", func() {
			ctx := context.Background()
			By("Reconciling the MonitorCheck")
			controllerReconciler := &MonitorCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that MonitorCheck status was updated")
			updatedMonitorCheck := &kuberikcomv1alpha1.MonitorCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedMonitorCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedMonitorCheck.Status.MatchedMonitorsCount).To(Equal(int32(1)))
			Expect(updatedMonitorCheck.Status.HealthyMonitorsCount).To(Equal(int32(1)))
			Expect(updatedMonitorCheck.Status.UnhealthyMonitorsCount).To(Equal(int32(0)))
			Expect(updatedMonitorCheck.Status.LastCheckTime).NotTo(BeNil())
		})

		It("should handle unhealthy monitors correctly", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to unhealthy state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateAlert
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the MonitorCheck")
			controllerReconciler := &MonitorCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that MonitorCheck status reflects unhealthy state")
			updatedMonitorCheck := &kuberikcomv1alpha1.MonitorCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedMonitorCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedMonitorCheck.Status.MatchedMonitorsCount).To(Equal(int32(1)))
			Expect(updatedMonitorCheck.Status.HealthyMonitorsCount).To(Equal(int32(0)))
			Expect(updatedMonitorCheck.Status.UnhealthyMonitorsCount).To(Equal(int32(1)))
		})

		It("should set HealthCheck status to Pending for non-failed monitors", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to Warning state (not explicitly failed)")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateWarn
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the MonitorCheck")
			controllerReconciler := &MonitorCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status is set to Pending")
			healthCheckName := "datadog-check-" + resourceName
			healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).NotTo(HaveOccurred())
			Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusPending))
		})

		It("should set HealthCheck status to Unhealthy for explicitly failed monitors", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to Alert state (explicitly failed)")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateAlert
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the MonitorCheck")
			controllerReconciler := &MonitorCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status is set to Unhealthy")
			healthCheckName := "datadog-check-" + resourceName
			healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).NotTo(HaveOccurred())
			Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
			Expect(healthCheck.Status.LastErrorTime).NotTo(BeNil())
		})

		It("should set HealthCheck status to Healthy when all monitors are OK", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to OK state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateOK
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the MonitorCheck")
			controllerReconciler := &MonitorCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status is set to Healthy")
			healthCheckName := "datadog-check-" + resourceName
			healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      healthCheckName,
				Namespace: namespace,
			}, healthCheck)
			Expect(err).NotTo(HaveOccurred())
			Expect(healthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusHealthy))
		})
	})

	Context("When testing enqueuing functions", func() {
		var namespace string
		var monitorCheck *kuberikcomv1alpha1.MonitorCheck
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

			By("creating a MonitorCheck")
			monitorCheck = &kuberikcomv1alpha1.MonitorCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-monitorcheck-enqueue",
					Namespace: namespace,
				},
				Spec: kuberikcomv1alpha1.MonitorCheckSpec{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app":      "test-app",
							"test-run": testRunID,
						},
					},
					HealthCheckTemplate: kuberikcomv1alpha1.HealthCheckTemplate{
						Labels: map[string]string{
							"app":      "test-app",
							"test-run": testRunID,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, monitorCheck)).To(Succeed())

			By("creating a DatadogMonitor")
			datadogMonitor = &datadoghqcomv1alpha1.DatadogMonitor{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-datadog-monitor-enqueue",
					Namespace: namespace,
					Labels: map[string]string{
						"app":      "test-app",
						"test-run": testRunID,
					},
				},
				Spec: datadoghqcomv1alpha1.DatadogMonitorSpec{
					Name: "test-monitor",
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

		It("should enqueue MonitorChecks when DatadogMonitor changes", func() {
			ctx := context.Background()
			By("Testing the enqueueMonitorChecksForMonitor function")
			controllerReconciler := &MonitorCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			requests := controllerReconciler.enqueueMonitorChecksForMonitor(ctx, datadogMonitor)
			Expect(requests).To(HaveLen(1))

			By("Verifying that the correct MonitorCheck is enqueued")
			Expect(requests[0].NamespacedName.Name).To(Equal("test-monitorcheck-enqueue"))
			Expect(requests[0].NamespacedName.Namespace).To(Equal(namespace))
		})

		It("should not enqueue MonitorChecks with non-matching labels", func() {
			ctx := context.Background()
			By("creating a MonitorCheck with different labels")
			otherMonitorCheck := &kuberikcomv1alpha1.MonitorCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-monitorcheck",
					Namespace: namespace,
				},
				Spec: kuberikcomv1alpha1.MonitorCheckSpec{
					LabelSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "other-app",
						},
					},
					HealthCheckTemplate: kuberikcomv1alpha1.HealthCheckTemplate{
						Labels: map[string]string{
							"app": "other-app",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, otherMonitorCheck)).To(Succeed())

			By("Testing the enqueueMonitorChecksForMonitor function")
			controllerReconciler := &MonitorCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			requests := controllerReconciler.enqueueMonitorChecksForMonitor(ctx, datadogMonitor)
			Expect(requests).To(HaveLen(1))

			By("Verifying that only the matching MonitorCheck is enqueued")
			Expect(requests[0].NamespacedName.Name).To(Equal("test-monitorcheck-enqueue"))
			Expect(requests[0].NamespacedName.Name).NotTo(Equal("other-monitorcheck"))
		})
	})
})
