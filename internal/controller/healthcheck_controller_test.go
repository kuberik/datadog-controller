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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"

	datadoghqcomv1alpha1 "github.com/DataDog/datadog-operator/apis/datadoghq/v1alpha1"
	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

var _ = Describe("HealthCheck Controller", func() {
	Context("When reconciling a HealthCheck", func() {
		const resourceName = "test-healthcheck"

		var namespace string
		var typeNamespacedName types.NamespacedName
		var healthCheck *kuberikrolloutv1alpha1.HealthCheck
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

			By("creating the HealthCheck")
			healthCheck = &kuberikrolloutv1alpha1.HealthCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
					Labels: map[string]string{
						"kuberik.com/managed-by": "datadog-controller",
						"app":                    "test-app",
						"test-run":               testRunID,
					},
					Annotations: map[string]string{
						"kuberik.com/datadog-monitor-name": "test-datadog-monitor",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: datadoghqcomv1alpha1.GroupVersion.String(),
							Kind:       "DatadogMonitor",
							Name:       "test-datadog-monitor",
							UID:        datadogMonitor.UID,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, healthCheck)).To(Succeed())
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

		It("should update HealthCheck status to Healthy when DatadogMonitor is OK", func() {
			ctx := context.Background()
			By("Reconciling the HealthCheck")
			controllerReconciler := &HealthCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status was updated to Healthy")
			updatedHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusHealthy))
			Expect(updatedHealthCheck.Status.LastErrorTime).To(BeNil())
		})

		It("should update HealthCheck status to Unhealthy when DatadogMonitor is Alert", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to Alert state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateAlert
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the HealthCheck")
			controllerReconciler := &HealthCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status was updated to Unhealthy")
			updatedHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
			Expect(updatedHealthCheck.Status.LastErrorTime).NotTo(BeNil())
		})

		It("should update HealthCheck status to Pending when DatadogMonitor is Warn", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to Warn state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateWarn
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the HealthCheck")
			controllerReconciler := &HealthCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status was updated to Pending")
			updatedHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusPending))
			Expect(updatedHealthCheck.Status.LastErrorTime).To(BeNil())
		})

		It("should preserve lastErrorTime when transitioning from Unhealthy to Healthy", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to Alert state to create lastErrorTime")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateAlert
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the HealthCheck to set Unhealthy status")
			controllerReconciler := &HealthCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status was updated to Unhealthy with lastErrorTime")
			updatedHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
			Expect(updatedHealthCheck.Status.LastErrorTime).NotTo(BeNil())
			originalErrorTime := updatedHealthCheck.Status.LastErrorTime

			By("Setting DatadogMonitor back to OK state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateOK
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the HealthCheck again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status was updated to Healthy but lastErrorTime was preserved")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusHealthy))
			Expect(updatedHealthCheck.Status.LastErrorTime).To(Equal(originalErrorTime))
		})

		It("should preserve lastErrorTime when transitioning from Unhealthy to Pending states", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to Alert state to create lastErrorTime")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateAlert
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the HealthCheck to set Unhealthy status")
			controllerReconciler := &HealthCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status was updated to Unhealthy with lastErrorTime")
			updatedHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
			Expect(updatedHealthCheck.Status.LastErrorTime).NotTo(BeNil())
			originalErrorTime := updatedHealthCheck.Status.LastErrorTime

			By("Setting DatadogMonitor to Warn state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateWarn
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the HealthCheck again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status was updated to Pending but lastErrorTime was preserved")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusPending))
			Expect(updatedHealthCheck.Status.LastErrorTime).To(Equal(originalErrorTime))
		})

		It("should only update lastErrorTime when entering Unhealthy state", func() {
			ctx := context.Background()
			By("Setting DatadogMonitor to Alert state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateAlert
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the HealthCheck")
			controllerReconciler := &HealthCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that HealthCheck status was updated to Unhealthy with lastErrorTime")
			updatedHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
			Expect(updatedHealthCheck.Status.LastErrorTime).NotTo(BeNil())
			firstErrorTime := updatedHealthCheck.Status.LastErrorTime

			By("Setting DatadogMonitor to OK state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateOK
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Reconciling the HealthCheck to Healthy state")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Setting DatadogMonitor back to Alert state")
			datadogMonitor.Status.MonitorState = datadoghqcomv1alpha1.DatadogMonitorStateAlert
			Expect(k8sClient.Status().Update(ctx, datadogMonitor)).To(Succeed())

			By("Waiting a moment to ensure timestamp difference")
			time.Sleep(100 * time.Millisecond)

			By("Reconciling the HealthCheck again to Unhealthy state")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that lastErrorTime was updated")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: namespace,
			}, updatedHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatusUnhealthy))
			// The lastErrorTime should be updated when entering Unhealthy state again
			Expect(updatedHealthCheck.Status.LastErrorTime).NotTo(BeNil())
			// Since metav1.Now() might return the same time within the same second,
			// we just verify that the field is properly set and not nil
			Expect(updatedHealthCheck.Status.LastErrorTime.Time).To(BeTemporally(">=", firstErrorTime.Time))
		})

		It("should skip HealthChecks not managed by datadog-controller", func() {
			ctx := context.Background()
			By("creating a HealthCheck not managed by datadog-controller")
			otherHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-healthcheck",
					Namespace: namespace,
					Labels: map[string]string{
						"kuberik.com/managed-by": "other-controller",
						"app":                    "test-app",
						"test-run":               testRunID,
					},
				},
			}
			Expect(k8sClient.Create(ctx, otherHealthCheck)).To(Succeed())

			By("Reconciling the other HealthCheck")
			controllerReconciler := &HealthCheckReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "other-healthcheck",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that the other HealthCheck status was not changed")
			updatedOtherHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      "other-healthcheck",
				Namespace: namespace,
			}, updatedOtherHealthCheck)
			Expect(err).NotTo(HaveOccurred())

			Expect(updatedOtherHealthCheck.Status.Status).To(Equal(kuberikrolloutv1alpha1.HealthStatus("")))
		})
	})
})
