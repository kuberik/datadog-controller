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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
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

const (
	managedByLabelKey                     = "kuberik.com/managed-by"
	managedByDatadogController            = "datadog-controller"
	datadogMonitorNameAnnotationKey       = "kuberik.com/datadog-monitor-name"
	datadogGateServiceAnnotationKey       = "kuberik.com/datadog-gate-service"
	datadogGateEnvAnnotationKey           = "kuberik.com/datadog-gate-env"
	datadogGateIdentifierAnnotationKey    = "kuberik.com/datadog-gate-identifier"
	datadogGateVersionAnnotationKey       = "kuberik.com/datadog-gate-version"
	datadogGateAPMPrimaryTagAnnotationKey = "kuberik.com/datadog-gate-apm-primary-tag"
	datadogGateEvaluationIDAnnotationKey  = "kuberik.com/datadog-gate-evaluation-id"
	datadogGateInputHashAnnotationKey     = "kuberik.com/datadog-gate-input-hash"
	defaultDatadogGateIdentifier          = "default"
	healthCheckRequeueInterval            = 30 * time.Second
	deploymentGatePollInterval            = 15 * time.Second
)

// HealthCheckReconciler reconciles a HealthCheck object
type HealthCheckReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	GateClient deploymentGateClient
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
		return ctrl.Result{RequeueAfter: healthCheckRequeueInterval}, nil
	}

	if gateInput, ok := r.resolveDeploymentGateInput(datadogMonitor); ok {
		return r.reconcileDeploymentGate(ctx, healthCheck, gateInput)
	}

	// Fall back to DatadogMonitor status when deployment gate inputs are not configured.
	if err := r.updateHealthCheckStatusFromMonitor(ctx, healthCheck, datadogMonitor); err != nil {
		log.Error(err, "Failed to update HealthCheck status from monitor")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: healthCheckRequeueInterval}, nil
}

// isManagedByDatadogController checks if the HealthCheck is managed by our controller
func (r *HealthCheckReconciler) isManagedByDatadogController(healthCheck *kuberikrolloutv1alpha1.HealthCheck) bool {
	if healthCheck.Labels == nil {
		return false
	}

	value, exists := healthCheck.Labels[managedByLabelKey]
	return exists && value == managedByDatadogController
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
		if monitorName, exists := healthCheck.Annotations[datadogMonitorNameAnnotationKey]; exists {
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

func (r *HealthCheckReconciler) resolveDeploymentGateInput(monitor *datadoghqcomv1alpha1.DatadogMonitor) (deploymentGateInput, bool) {
	if monitor == nil {
		return deploymentGateInput{}, false
	}

	service := firstNonEmpty(
		annotationValue(monitor.Annotations, datadogGateServiceAnnotationKey),
		tagValue(monitor.Spec.Tags, "service"),
	)
	env := firstNonEmpty(
		annotationValue(monitor.Annotations, datadogGateEnvAnnotationKey),
		tagValue(monitor.Spec.Tags, "env"),
	)

	if service == "" || env == "" {
		return deploymentGateInput{}, false
	}

	return deploymentGateInput{
		Service:       service,
		Env:           env,
		Identifier:    firstNonEmpty(annotationValue(monitor.Annotations, datadogGateIdentifierAnnotationKey), defaultDatadogGateIdentifier),
		Version:       annotationValue(monitor.Annotations, datadogGateVersionAnnotationKey),
		APMPrimaryTag: annotationValue(monitor.Annotations, datadogGateAPMPrimaryTagAnnotationKey),
	}, true
}

func (r *HealthCheckReconciler) reconcileDeploymentGate(
	ctx context.Context,
	healthCheck *kuberikrolloutv1alpha1.HealthCheck,
	input deploymentGateInput,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	gateClient, err := r.getDeploymentGateClient()
	if err != nil {
		logger.Info("Datadog deployment gates client is not configured, marking HealthCheck as Pending", "error", err)
		if err := r.updateHealthCheckStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusPending, healthCheck.Status.LastErrorTime); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: healthCheckRequeueInterval}, nil
	}

	inputHash := hashDeploymentGateInput(input)
	currentHash := annotationValue(healthCheck.Annotations, datadogGateInputHashAnnotationKey)
	evaluationID := annotationValue(healthCheck.Annotations, datadogGateEvaluationIDAnnotationKey)

	if evaluationID == "" || currentHash != inputHash {
		evaluationID, err = gateClient.StartEvaluation(ctx, input)
		if err != nil {
			logger.Error(err, "Failed to start Datadog deployment gate evaluation", "service", input.Service, "env", input.Env, "identifier", input.Identifier)
			if err := r.updateHealthCheckStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusPending, healthCheck.Status.LastErrorTime); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: healthCheckRequeueInterval}, nil
		}

		logger.Info("Started Datadog deployment gate evaluation",
			"healthCheck", healthCheck.Name,
			"service", input.Service,
			"env", input.Env,
			"identifier", input.Identifier,
			"evaluationID", evaluationID)

		if err := r.patchHealthCheckAnnotations(ctx, healthCheck, map[string]string{
			datadogGateEvaluationIDAnnotationKey: evaluationID,
			datadogGateInputHashAnnotationKey:    inputHash,
		}); err != nil {
			return ctrl.Result{}, err
		}

		refreshedHealthCheck := &kuberikrolloutv1alpha1.HealthCheck{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(healthCheck), refreshedHealthCheck); err != nil {
			return ctrl.Result{}, err
		}

		if err := r.updateHealthCheckStatus(ctx, refreshedHealthCheck, kuberikrolloutv1alpha1.HealthStatusPending, refreshedHealthCheck.Status.LastErrorTime); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: deploymentGatePollInterval}, nil
	}

	evaluation, err := gateClient.GetEvaluation(ctx, evaluationID)
	if err != nil {
		logger.Error(err, "Failed to fetch Datadog deployment gate evaluation", "evaluationID", evaluationID, "healthCheck", healthCheck.Name)
		if err := r.updateHealthCheckStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusPending, healthCheck.Status.LastErrorTime); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: healthCheckRequeueInterval}, nil
	}

	logger.Info("Fetched Datadog deployment gate evaluation",
		"healthCheck", healthCheck.Name,
		"evaluationID", evaluationID,
		"gateStatus", evaluation.Status,
		"ruleCount", len(evaluation.Rules))

	switch evaluation.Status {
	case "pass":
		if err := r.updateHealthCheckStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusHealthy, healthCheck.Status.LastErrorTime); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	case "fail":
		if err := r.updateHealthCheckStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusUnhealthy, lastErrorTimeForUnhealthy(healthCheck)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	case "in_progress":
		if err := r.updateHealthCheckStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusPending, healthCheck.Status.LastErrorTime); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: deploymentGatePollInterval}, nil
	default:
		logger.Info("Unknown Datadog deployment gate status, marking HealthCheck as Pending", "evaluationID", evaluationID, "gateStatus", evaluation.Status)
		if err := r.updateHealthCheckStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusPending, healthCheck.Status.LastErrorTime); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: healthCheckRequeueInterval}, nil
	}
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

func (r *HealthCheckReconciler) patchHealthCheckAnnotations(ctx context.Context, healthCheck *kuberikrolloutv1alpha1.HealthCheck, annotations map[string]string) error {
	original := healthCheck.DeepCopy()
	if healthCheck.Annotations == nil {
		healthCheck.Annotations = make(map[string]string, len(annotations))
	}

	changed := false
	for key, value := range annotations {
		if healthCheck.Annotations[key] != value {
			healthCheck.Annotations[key] = value
			changed = true
		}
	}

	if !changed {
		return nil
	}

	return r.Patch(ctx, healthCheck, client.MergeFrom(original))
}

func (r *HealthCheckReconciler) getDeploymentGateClient() (deploymentGateClient, error) {
	if r.GateClient != nil {
		return r.GateClient, nil
	}

	return newDatadogDeploymentGatesClientFromEnv()
}

func lastErrorTimeForUnhealthy(healthCheck *kuberikrolloutv1alpha1.HealthCheck) *metav1.Time {
	if healthCheck.Status.Status == kuberikrolloutv1alpha1.HealthStatusUnhealthy && healthCheck.Status.LastErrorTime != nil {
		return healthCheck.Status.LastErrorTime
	}

	now := metav1.Now()
	return &now
}

func hashDeploymentGateInput(input deploymentGateInput) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{
		input.Service,
		input.Env,
		input.Identifier,
		input.Version,
		input.APMPrimaryTag,
	}, "\x00")))

	return hex.EncodeToString(hash[:])
}

func annotationValue(annotations map[string]string, key string) string {
	if annotations == nil {
		return ""
	}

	return strings.TrimSpace(annotations[key])
}

func tagValue(tags []string, key string) string {
	for _, tag := range tags {
		tagKey, tagValue, ok := strings.Cut(tag, ":")
		if !ok {
			continue
		}

		if strings.EqualFold(strings.TrimSpace(tagKey), key) {
			return strings.TrimSpace(tagValue)
		}
	}

	return ""
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
