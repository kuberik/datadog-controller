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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	datadoghqcomv1alpha1 "github.com/DataDog/datadog-operator/apis/datadoghq/v1alpha1"
	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

const (
	// AnnotationKeyHealthCheck is the annotation key that triggers HealthCheck creation
	AnnotationKeyHealthCheck = "kuberik.com/health-check"
	// AnnotationValueHealthCheck is the value that enables HealthCheck creation
	AnnotationValueHealthCheck = "true"
)

// DatadogMonitorReconciler reconciles a DatadogMonitor object
type DatadogMonitorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=datadoghq.com,resources=datadogmonitors,verbs=get;list;watch
// +kubebuilder:rbac:groups=kuberik.com,resources=healthchecks,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *DatadogMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the DatadogMonitor instance
	datadogMonitor := &datadoghqcomv1alpha1.DatadogMonitor{}
	err := r.Get(ctx, req.NamespacedName, datadogMonitor)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Info("DatadogMonitor resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get DatadogMonitor")
		return ctrl.Result{}, err
	}

	// Check if this DatadogMonitor should have a HealthCheck
	if !r.shouldCreateHealthCheck(datadogMonitor) {
		// Check if we need to clean up an existing HealthCheck
		if err := r.cleanupHealthCheckIfNeeded(ctx, datadogMonitor); err != nil {
			log.Error(err, "Failed to cleanup HealthCheck")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Ensure HealthCheck exists
	if err := r.ensureHealthCheck(ctx, datadogMonitor); err != nil {
		log.Error(err, "Failed to ensure HealthCheck")
		return ctrl.Result{}, err
	}

	// Requeue every 30 seconds to keep HealthCheck status updated
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// shouldCreateHealthCheck checks if a HealthCheck should be created for this DatadogMonitor
func (r *DatadogMonitorReconciler) shouldCreateHealthCheck(monitor *datadoghqcomv1alpha1.DatadogMonitor) bool {
	if monitor.Annotations == nil {
		return false
	}

	value, exists := monitor.Annotations[AnnotationKeyHealthCheck]
	return exists && value == AnnotationValueHealthCheck
}

// ensureHealthCheck ensures that a HealthCheck resource exists for the DatadogMonitor
func (r *DatadogMonitorReconciler) ensureHealthCheck(ctx context.Context, monitor *datadoghqcomv1alpha1.DatadogMonitor) error {
	log := log.FromContext(ctx)

	// Generate HealthCheck name
	healthCheckName := fmt.Sprintf("datadog-check-%s", monitor.Name)

	// Check if HealthCheck already exists
	existingHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: monitor.Namespace,
		Name:      healthCheckName,
	}, existingHealthCheck)

	if err != nil && errors.IsNotFound(err) {
		// Create new HealthCheck
		healthCheck := &kuberikrolloutv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name:      healthCheckName,
				Namespace: monitor.Namespace,
				Labels:    make(map[string]string),
				Annotations: map[string]string{
					"kuberik.com/datadog-monitor-name": monitor.Name,
					"kuberik.com/datadog-monitor-uid":  string(monitor.UID),
				},
			},
		}

		// Copy all labels from the monitor
		for key, value := range monitor.Labels {
			healthCheck.Labels[key] = value
		}
		// Add our required labels
		healthCheck.Labels["kuberik.com/datadog-monitor"] = monitor.Name
		healthCheck.Labels["kuberik.com/managed-by"] = "datadog-controller"

		// Copy all annotations from the monitor (except our internal ones)
		for key, value := range monitor.Annotations {
			if key != AnnotationKeyHealthCheck {
				healthCheck.Annotations[key] = value
			}
		}

		// Set owner reference
		if err := ctrl.SetControllerReference(monitor, healthCheck, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference: %w", err)
		}

		log.Info("Creating new HealthCheck", "name", healthCheckName, "namespace", monitor.Namespace)
		if err := r.Create(ctx, healthCheck); err != nil {
			return fmt.Errorf("failed to create HealthCheck: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get existing HealthCheck: %w", err)
	}

	return nil
}

// cleanupHealthCheckIfNeeded removes HealthCheck if the annotation is removed
func (r *DatadogMonitorReconciler) cleanupHealthCheckIfNeeded(ctx context.Context, monitor *datadoghqcomv1alpha1.DatadogMonitor) error {
	log := log.FromContext(ctx)

	// Generate HealthCheck name
	healthCheckName := fmt.Sprintf("datadog-check-%s", monitor.Name)

	// Check if HealthCheck exists
	existingHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
	err := r.Get(ctx, client.ObjectKey{
		Namespace: monitor.Namespace,
		Name:      healthCheckName,
	}, existingHealthCheck)

	if err != nil && errors.IsNotFound(err) {
		// HealthCheck doesn't exist, nothing to clean up
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get existing HealthCheck: %w", err)
	}

	// Check if this HealthCheck is owned by our DatadogMonitor
	isOwner := false
	for _, ownerRef := range existingHealthCheck.OwnerReferences {
		if ownerRef.UID == monitor.UID && ownerRef.Kind == "DatadogMonitor" && ownerRef.APIVersion == datadoghqcomv1alpha1.GroupVersion.String() {
			isOwner = true
			break
		}
	}

	if isOwner {
		log.Info("Deleting HealthCheck as annotation was removed", "name", healthCheckName, "namespace", monitor.Namespace)
		if err := r.Delete(ctx, existingHealthCheck); err != nil {
			return fmt.Errorf("failed to delete HealthCheck: %w", err)
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DatadogMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&datadoghqcomv1alpha1.DatadogMonitor{}).
		Named("datadogmonitor").
		Complete(r)
}
