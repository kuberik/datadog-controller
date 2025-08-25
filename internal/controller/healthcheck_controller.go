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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	datadoghqcomv1alpha1 "github.com/DataDog/datadog-operator/apis/datadoghq/v1alpha1"
	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

// HealthCheckReconciler reconciles a HealthCheck object
type HealthCheckReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kuberik.com,resources=healthchecks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kuberik.com,resources=healthchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=datadoghq.com,resources=datadogmonitors,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *HealthCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the HealthCheck instance
	healthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
	err := r.Get(ctx, req.NamespacedName, healthCheck)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Info("HealthCheck resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get HealthCheck")
		return ctrl.Result{}, err
	}

	// Check if this HealthCheck is managed by our controller
	if !r.isManagedByDatadogController(healthCheck) {
		log.Info("HealthCheck is not managed by datadog-controller, skipping")
		return ctrl.Result{}, nil
	}

	// Find the owner DatadogMonitor
	datadogMonitor, err := r.findOwnerDatadogMonitor(ctx, healthCheck)
	if err != nil {
		log.Error(err, "Failed to find owner DatadogMonitor")
		return ctrl.Result{}, err
	}

	if datadogMonitor == nil {
		// No owner found, set status to Pending
		if err := r.updateHealthCheckStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusPending, nil); err != nil {
			log.Error(err, "Failed to update HealthCheck status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Update HealthCheck status based on DatadogMonitor status
	if err := r.updateHealthCheckStatusFromMonitor(ctx, healthCheck, datadogMonitor); err != nil {
		log.Error(err, "Failed to update HealthCheck status from monitor")
		return ctrl.Result{}, err
	}

	// Requeue every 30 seconds to keep status updated
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// isManagedByDatadogController checks if the HealthCheck is managed by our controller
func (r *HealthCheckReconciler) isManagedByDatadogController(healthCheck *kuberikrolloutv1alpha1.HealthCheck) bool {
	if healthCheck.Labels == nil {
		return false
	}

	value, exists := healthCheck.Labels["kuberik.com/managed-by"]
	return exists && value == "datadog-controller"
}

// findOwnerDatadogMonitor finds the DatadogMonitor that owns this HealthCheck
func (r *HealthCheckReconciler) findOwnerDatadogMonitor(ctx context.Context, healthCheck *kuberikrolloutv1alpha1.HealthCheck) (*datadoghqcomv1alpha1.DatadogMonitor, error) {
	// First try to find by owner reference
	for _, ownerRef := range healthCheck.OwnerReferences {
		if ownerRef.Kind == "DatadogMonitor" && ownerRef.APIVersion == datadoghqcomv1alpha1.GroupVersion.String() {
			datadogMonitor := &datadoghqcomv1alpha1.DatadogMonitor{}
			err := r.Get(ctx, types.NamespacedName{
				Namespace: healthCheck.Namespace,
				Name:      ownerRef.Name,
			}, datadogMonitor)

			if err != nil && !errors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to get owner DatadogMonitor: %w", err)
			}

			if err == nil {
				return datadogMonitor, nil
			}
		}
	}

	// Fallback: try to find by annotation
	if healthCheck.Annotations != nil {
		if monitorName, exists := healthCheck.Annotations["kuberik.com/datadog-monitor-name"]; exists {
			datadogMonitor := &datadoghqcomv1alpha1.DatadogMonitor{}
			err := r.Get(ctx, types.NamespacedName{
				Namespace: healthCheck.Namespace,
				Name:      monitorName,
			}, datadogMonitor)

			if err != nil && !errors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to get DatadogMonitor by annotation: %w", err)
			}

			if err == nil {
				return datadogMonitor, nil
			}
		}
	}

	return nil, nil
}

// updateHealthCheckStatusFromMonitor updates the HealthCheck status based on DatadogMonitor status
func (r *HealthCheckReconciler) updateHealthCheckStatusFromMonitor(ctx context.Context, healthCheck *kuberikrolloutv1alpha1.HealthCheck, monitor *datadoghqcomv1alpha1.DatadogMonitor) error {
	log := log.FromContext(ctx)

	var healthStatus kuberikrolloutv1alpha1.HealthStatus
	var lastErrorTime *metav1.Time

	// Determine health status based on monitor state
	switch monitor.Status.MonitorState {
	case datadoghqcomv1alpha1.DatadogMonitorStateOK:
		healthStatus = kuberikrolloutv1alpha1.HealthStatusHealthy
		// Don't reset lastErrorTime - keep the existing value
		lastErrorTime = healthCheck.Status.LastErrorTime
	case datadoghqcomv1alpha1.DatadogMonitorStateAlert:
		healthStatus = kuberikrolloutv1alpha1.HealthStatusUnhealthy
		now := metav1.Now()
		lastErrorTime = &now
	case datadoghqcomv1alpha1.DatadogMonitorStateWarn:
		// Warning state is considered pending (not explicitly failed)
		healthStatus = kuberikrolloutv1alpha1.HealthStatusPending
		// Don't reset lastErrorTime - keep the existing value
		lastErrorTime = healthCheck.Status.LastErrorTime
	case datadoghqcomv1alpha1.DatadogMonitorStateNoData:
		// No data state is considered pending
		healthStatus = kuberikrolloutv1alpha1.HealthStatusPending
		// Don't reset lastErrorTime - keep the existing value
		lastErrorTime = healthCheck.Status.LastErrorTime
	case datadoghqcomv1alpha1.DatadogMonitorStateSkipped:
		// Skipped state is considered pending
		healthStatus = kuberikrolloutv1alpha1.HealthStatusPending
		// Don't reset lastErrorTime - keep the existing value
		lastErrorTime = healthCheck.Status.LastErrorTime
	case datadoghqcomv1alpha1.DatadogMonitorStateIgnored:
		// Ignored state is considered pending
		healthStatus = kuberikrolloutv1alpha1.HealthStatusPending
		// Don't reset lastErrorTime - keep the existing value
		lastErrorTime = healthCheck.Status.LastErrorTime
	default:
		// Unknown state is considered pending
		healthStatus = kuberikrolloutv1alpha1.HealthStatusPending
		// Don't reset lastErrorTime - keep the existing value
		lastErrorTime = healthCheck.Status.LastErrorTime
	}

	log.Info("Updating HealthCheck status from monitor",
		"healthCheck", healthCheck.Name,
		"monitor", monitor.Name,
		"monitorState", monitor.Status.MonitorState,
		"healthStatus", healthStatus)

	return r.updateHealthCheckStatus(ctx, healthCheck, healthStatus, lastErrorTime)
}

// updateHealthCheckStatus updates the HealthCheck status
func (r *HealthCheckReconciler) updateHealthCheckStatus(ctx context.Context, healthCheck *kuberikrolloutv1alpha1.HealthCheck, status kuberikrolloutv1alpha1.HealthStatus, lastErrorTime *metav1.Time) error {
	// Only update if status has changed
	if healthCheck.Status.Status == status &&
		((healthCheck.Status.LastErrorTime == nil && lastErrorTime == nil) ||
			(healthCheck.Status.LastErrorTime != nil && lastErrorTime != nil &&
				healthCheck.Status.LastErrorTime.Equal(lastErrorTime))) {
		return nil
	}

	healthCheck.Status.Status = status
	healthCheck.Status.LastErrorTime = lastErrorTime

	return r.Status().Update(ctx, healthCheck)
}

// enqueueHealthChecksForMonitor enqueues HealthCheck objects that are owned by a given DatadogMonitor
func (r *HealthCheckReconciler) enqueueHealthChecksForMonitor(ctx context.Context, obj client.Object) []reconcile.Request {
	datadogMonitor, ok := obj.(*datadoghqcomv1alpha1.DatadogMonitor)
	if !ok {
		return []reconcile.Request{}
	}

	// Find all HealthCheck resources that might be owned by this DatadogMonitor
	healthChecks := &kuberikrolloutv1alpha1.HealthCheckList{}
	err := r.List(ctx, healthChecks, client.InNamespace(datadogMonitor.Namespace))
	if err != nil {
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, healthCheck := range healthChecks.Items {
		// Check if this HealthCheck is owned by our DatadogMonitor
		for _, ownerRef := range healthCheck.OwnerReferences {
			if ownerRef.UID == datadogMonitor.UID && ownerRef.Kind == "DatadogMonitor" && ownerRef.APIVersion == datadoghqcomv1alpha1.GroupVersion.String() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      healthCheck.Name,
						Namespace: healthCheck.Namespace,
					},
				})
				break
			}
		}
	}

	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *HealthCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kuberikrolloutv1alpha1.HealthCheck{}).
		Watches(
			&datadoghqcomv1alpha1.DatadogMonitor{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueHealthChecksForMonitor),
		).
		Named("healthcheck").
		Complete(r)
}
