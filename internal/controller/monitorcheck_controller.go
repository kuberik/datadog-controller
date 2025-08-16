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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	datadoghqcomv1alpha1 "github.com/DataDog/datadog-operator/apis/datadoghq/v1alpha1"
	kuberikcomv1alpha1 "github.com/kuberik/datadog-controller/api/v1alpha1"
	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

// MonitorCheckReconciler reconciles a MonitorCheck object
type MonitorCheckReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=kuberik.com,resources=monitorchecks,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kuberik.com,resources=monitorchecks/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kuberik.com,resources=monitorchecks/finalizers,verbs=update
//+kubebuilder:rbac:groups=datadoghq.com,resources=datadogmonitors,verbs=get;list;watch
//+kubebuilder:rbac:groups=kuberik.com,resources=healthchecks,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MonitorCheck object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *MonitorCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the MonitorCheck instance
	monitorCheck := &kuberikcomv1alpha1.MonitorCheck{}
	err := r.Get(ctx, req.NamespacedName, monitorCheck)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Info("MonitorCheck resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get MonitorCheck")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !monitorCheck.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, monitorCheck)
	}

	// Find matching DatadogMonitors
	matchedMonitors, err := r.findMatchingDatadogMonitors(ctx, monitorCheck)
	if err != nil {
		log.Error(err, "Failed to find matching DatadogMonitors")
		return ctrl.Result{}, err
	}

	// Evaluate monitor health
	healthyCount, unhealthyCount := r.evaluateMonitorHealth(matchedMonitors)

	// Ensure kuberik HealthCheck exists
	err = r.ensureKuberikHealthCheck(ctx, monitorCheck, matchedMonitors)
	if err != nil {
		log.Error(err, "Failed to ensure kuberik HealthCheck")
		return ctrl.Result{}, err
	}

	// Update status
	err = r.updateStatus(ctx, monitorCheck, len(matchedMonitors), int(healthyCount), int(unhealthyCount))
	if err != nil {
		log.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	// Requeue every 30 seconds to keep status updated
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// findMatchingDatadogMonitors finds DatadogMonitor resources that match the label selector
func (r *MonitorCheckReconciler) findMatchingDatadogMonitors(ctx context.Context, monitorCheck *kuberikcomv1alpha1.MonitorCheck) ([]datadoghqcomv1alpha1.DatadogMonitor, error) {
	log := log.FromContext(ctx)

	// Convert label selector to selector
	selector, err := metav1.LabelSelectorAsSelector(&monitorCheck.Spec.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	// List all DatadogMonitors in the cluster
	datadogMonitors := &datadoghqcomv1alpha1.DatadogMonitorList{}
	err = r.List(ctx, datadogMonitors)
	if err != nil {
		return nil, fmt.Errorf("failed to list DatadogMonitors: %w", err)
	}

	// Filter monitors that match the label selector
	var matchedMonitors []datadoghqcomv1alpha1.DatadogMonitor
	for _, monitor := range datadogMonitors.Items {
		if selector.Matches(labels.Set(monitor.Labels)) {
			matchedMonitors = append(matchedMonitors, monitor)
		}
	}

	log.Info("Found matching DatadogMonitors", "selector", selector.String(), "count", len(matchedMonitors))
	return matchedMonitors, nil
}

// evaluateMonitorHealth evaluates the health of a list of DatadogMonitors
func (r *MonitorCheckReconciler) evaluateMonitorHealth(monitors []datadoghqcomv1alpha1.DatadogMonitor) (healthyCount, unhealthyCount int32) {
	for _, monitor := range monitors {
		if r.isMonitorHealthy(monitor) {
			healthyCount++
		} else {
			unhealthyCount++
		}
	}
	return healthyCount, unhealthyCount
}

// isMonitorHealthy determines if a DatadogMonitor is healthy based on its state
func (r *MonitorCheckReconciler) isMonitorHealthy(monitor datadoghqcomv1alpha1.DatadogMonitor) bool {
	// Check the monitor state - only OK is considered healthy
	// All other states (Alert, Warn, No Data, Skipped, Ignored, Unknown) are unhealthy
	return monitor.Status.MonitorState == datadoghqcomv1alpha1.DatadogMonitorStateOK
}

// isMonitorExplicitlyFailed determines if a DatadogMonitor is explicitly in a failed state
func (r *MonitorCheckReconciler) isMonitorExplicitlyFailed(monitor datadoghqcomv1alpha1.DatadogMonitor) bool {
	// Only Alert state is considered explicitly failed
	// Other states like Warn, No Data, Skipped, Ignored, Unknown are not explicitly failed
	return monitor.Status.MonitorState == datadoghqcomv1alpha1.DatadogMonitorStateAlert
}

// ensureKuberikHealthCheck ensures the kuberik HealthCheck resource exists
func (r *MonitorCheckReconciler) ensureKuberikHealthCheck(ctx context.Context, monitorCheck *kuberikcomv1alpha1.MonitorCheck, monitors []datadoghqcomv1alpha1.DatadogMonitor) error {
	log := log.FromContext(ctx)
	log.Info("Ensuring kuberik HealthCheck exists", "monitorCheck", monitorCheck.Name)

	// Always use the same namespace as the MonitorCheck
	namespace := monitorCheck.Namespace

	// Generate name automatically
	name := fmt.Sprintf("datadog-check-%s", monitorCheck.Name)

	log.Info("HealthCheck template values",
		"generatedName", name,
		"namespace", namespace)

	// Check if HealthCheck already exists by listing and checking owner references
	var existingHealthChecks kuberikrolloutv1alpha1.HealthCheckList
	err := r.List(ctx, &existingHealthChecks, client.InNamespace(namespace))
	if err != nil {
		return fmt.Errorf("failed to list HealthChecks: %w", err)
	}

	// Look for existing HealthCheck with matching owner reference
	var existingHealthCheck *kuberikrolloutv1alpha1.HealthCheck
	for i := range existingHealthChecks.Items {
		hc := &existingHealthChecks.Items[i]
		// Check if this HealthCheck is owned by our MonitorCheck
		for _, ownerRef := range hc.OwnerReferences {
			if ownerRef.UID == monitorCheck.UID && ownerRef.Kind == "MonitorCheck" && ownerRef.APIVersion == kuberikcomv1alpha1.GroupVersion.String() {
				existingHealthCheck = hc
				break
			}
		}
		if existingHealthCheck != nil {
			break
		}
	}

	// Determine HealthCheck status
	var healthStatus kuberikrolloutv1alpha1.HealthStatus
	var lastErrorTime *metav1.Time

	if len(monitors) == 0 {
		// No monitors found - set to Pending
		healthStatus = kuberikrolloutv1alpha1.HealthStatusPending
	} else {
		// Check monitor states
		allHealthy := true
		anyExplicitlyFailed := false

		for _, monitor := range monitors {
			if !r.isMonitorHealthy(monitor) {
				allHealthy = false
				if r.isMonitorExplicitlyFailed(monitor) {
					anyExplicitlyFailed = true
				}
			}
		}

		if allHealthy {
			// All monitors are healthy
			healthStatus = kuberikrolloutv1alpha1.HealthStatusHealthy
		} else if anyExplicitlyFailed {
			// Some monitors are explicitly failed
			healthStatus = kuberikrolloutv1alpha1.HealthStatusUnhealthy
			now := metav1.Now()
			lastErrorTime = &now
		} else {
			// Some monitors are unhealthy but not explicitly failed (e.g., Warn, No Data)
			// Set to Pending as they're not explicitly failed
			healthStatus = kuberikrolloutv1alpha1.HealthStatusPending
		}
	}

	if existingHealthCheck == nil {
		// Create new HealthCheck
		healthCheck := &kuberikrolloutv1alpha1.HealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		}

		// Copy metadata from template
		if monitorCheck.Spec.HealthCheckTemplate.Labels != nil {
			healthCheck.Labels = monitorCheck.Spec.HealthCheckTemplate.Labels
		}
		if monitorCheck.Spec.HealthCheckTemplate.Annotations != nil {
			healthCheck.Annotations = monitorCheck.Spec.HealthCheckTemplate.Annotations
		}

		// Set owner reference (always possible since same namespace)
		if err := ctrl.SetControllerReference(monitorCheck, healthCheck, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference: %w", err)
		}

		log.Info("Creating new HealthCheck", "name", name, "namespace", namespace)
		if err := r.Create(ctx, healthCheck); err != nil {
			return fmt.Errorf("failed to create HealthCheck: %w", err)
		}

		// Now update the status
		healthCheck.Status.Status = healthStatus
		healthCheck.Status.LastErrorTime = lastErrorTime

		log.Info("Setting HealthCheck status", "name", name, "namespace", namespace, "status", healthStatus)
		return r.Status().Update(ctx, healthCheck)
	}

	// Update existing HealthCheck status if it changed
	if existingHealthCheck.Status.Status != healthStatus {
		existingHealthCheck.Status.Status = healthStatus
		existingHealthCheck.Status.LastErrorTime = lastErrorTime

		log.Info("Updating HealthCheck status", "name", existingHealthCheck.Name, "namespace", namespace, "status", healthStatus)
		return r.Status().Update(ctx, existingHealthCheck)
	}

	// For now, just log that it exists
	log.Info("HealthCheck already exists with correct status", "name", existingHealthCheck.Name, "namespace", namespace, "status", healthStatus)
	return nil
}

// updateStatus updates the MonitorCheck status
func (r *MonitorCheckReconciler) updateStatus(ctx context.Context, monitorCheck *kuberikcomv1alpha1.MonitorCheck, totalCount, healthyCount, unhealthyCount int) error {
	now := metav1.Now()

	monitorCheck.Status.MatchedMonitorsCount = int32(totalCount)
	monitorCheck.Status.HealthyMonitorsCount = int32(healthyCount)
	monitorCheck.Status.UnhealthyMonitorsCount = int32(unhealthyCount)
	monitorCheck.Status.LastCheckTime = &now

	// Update Ready condition
	r.updateStatusConditions(monitorCheck, healthyCount == totalCount && totalCount > 0)

	return r.Status().Update(ctx, monitorCheck)
}

// updateStatusConditions updates the status conditions
func (r *MonitorCheckReconciler) updateStatusConditions(monitorCheck *kuberikcomv1alpha1.MonitorCheck, isReady bool) {
	now := metav1.Now()

	// Find existing Ready condition
	readyCondition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "Reconciling",
		Message:            "MonitorCheck is being reconciled",
		LastTransitionTime: now,
	}

	if isReady {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "AllMonitorsHealthy"
		readyCondition.Message = "All selected DatadogMonitors are healthy"
	}

	// Update or add the condition
	conditionUpdated := false
	for i, condition := range monitorCheck.Status.Conditions {
		if condition.Type == "Ready" {
			monitorCheck.Status.Conditions[i] = readyCondition
			conditionUpdated = true
			break
		}
	}

	if !conditionUpdated {
		monitorCheck.Status.Conditions = append(monitorCheck.Status.Conditions, readyCondition)
	}
}

// handleDeletion handles the deletion of MonitorCheck resources
func (r *MonitorCheckReconciler) handleDeletion(ctx context.Context, monitorCheck *kuberikcomv1alpha1.MonitorCheck) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Handling deletion of MonitorCheck", "name", monitorCheck.Name)

	// TODO: Implement cleanup logic for associated kuberik HealthCheck resources
	// For now, just log the intended deletion
	log.Info("Would delete associated kuberik HealthCheck resources")

	return ctrl.Result{}, nil
}

// enqueueMonitorChecksForMonitor enqueues MonitorCheck objects that select a given DatadogMonitor
func (r *MonitorCheckReconciler) enqueueMonitorChecksForMonitor(ctx context.Context, obj client.Object) []reconcile.Request {
	datadogMonitor, ok := obj.(*datadoghqcomv1alpha1.DatadogMonitor)
	if !ok {
		return []reconcile.Request{}
	}

	// Find all MonitorCheck resources that might select this DatadogMonitor
	monitorChecks := &kuberikcomv1alpha1.MonitorCheckList{}
	err := r.List(ctx, monitorChecks)
	if err != nil {
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, monitorCheck := range monitorChecks.Items {
		// Check if this MonitorCheck's label selector matches the DatadogMonitor
		selector, err := metav1.LabelSelectorAsSelector(&monitorCheck.Spec.LabelSelector)
		if err != nil {
			continue
		}

		if selector.Matches(labels.Set(datadogMonitor.Labels)) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      monitorCheck.Name,
					Namespace: monitorCheck.Namespace,
				},
			})
		}
	}

	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *MonitorCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kuberikcomv1alpha1.MonitorCheck{}).
		Watches(
			&datadoghqcomv1alpha1.DatadogMonitor{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueMonitorChecksForMonitor),
		).
		Complete(r)
}
