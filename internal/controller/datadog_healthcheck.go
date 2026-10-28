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
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

const (
	// HealthCheckClassDatadog evaluates Datadog monitors selected by ID and/or tags.
	HealthCheckClassDatadog = "datadog"
	// HealthCheckClassDatadogIncidents is unhealthy while a matching Datadog incident is open.
	HealthCheckClassDatadogIncidents = "datadog-incidents"
	// HealthCheckClassDatadogMonitorCRD marks HealthChecks generated from an annotated
	// DatadogMonitor resource; their state mirrors the resource's status.
	HealthCheckClassDatadogMonitorCRD = "datadog-monitor-crd"

	// AnnotationDatadogMonitorIDs is a comma-separated list of monitor IDs.
	AnnotationDatadogMonitorIDs = "healthcheck.kuberik.com/datadog-monitor-ids"
	// AnnotationDatadogMonitorTags is a comma-separated list of monitor tags; a monitor
	// is selected when it carries all of them.
	AnnotationDatadogMonitorTags = "healthcheck.kuberik.com/datadog-monitor-tags"
	// AnnotationDatadogGroupTags is a comma-separated list of tags that narrows the
	// groups of each selected monitor (e.g. "service:api" picks the service:api group
	// of a monitor grouped by service).
	AnnotationDatadogGroupTags = "healthcheck.kuberik.com/datadog-group-tags"
	// AnnotationDatadogIncidentQuery is a Datadog incident search query; only open
	// (active or stable) incidents are considered.
	AnnotationDatadogIncidentQuery = "healthcheck.kuberik.com/datadog-incident-query"
	// AnnotationRequeueInterval overrides how often Datadog is polled.
	AnnotationRequeueInterval = "healthcheck.kuberik.com/requeue-interval"

	defaultDatadogPollInterval = 60 * time.Second
	monitorStatusAlert         = "Alert"
	maxListedInMessage         = 5
)

// reconcileDatadogClassHealthCheck handles HealthChecks of the datadog and
// datadog-incidents classes. ok is false for any other class.
func (r *HealthCheckReconciler) reconcileDatadogClassHealthCheck(
	ctx context.Context,
	healthCheck *kuberikrolloutv1alpha1.HealthCheck,
) (result ctrl.Result, ok bool, err error) {
	if healthCheck.Spec.Class == nil {
		return ctrl.Result{}, false, nil
	}
	class := *healthCheck.Spec.Class
	if class != HealthCheckClassDatadog && class != HealthCheckClassDatadogIncidents {
		return ctrl.Result{}, false, nil
	}

	logger := log.FromContext(ctx)
	requeue := ctrl.Result{RequeueAfter: pollInterval(healthCheck)}

	api, err := r.datadogAPI()
	if err != nil {
		logger.Info("Datadog API is not configured, marking HealthCheck Pending", "error", err.Error())
		return requeue, true, r.setStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusPending, err.Error())
	}

	var unhealthy bool
	var message string
	if class == HealthCheckClassDatadog {
		unhealthy, message, err = evaluateMonitors(ctx, api, healthCheck.Annotations)
	} else {
		unhealthy, message, err = evaluateIncidents(ctx, api, healthCheck.Annotations)
	}
	if err != nil {
		logger.Info("Datadog evaluation failed, marking HealthCheck Pending", "error", err.Error())
		return requeue, true, r.setStatus(ctx, healthCheck, kuberikrolloutv1alpha1.HealthStatusPending, err.Error())
	}

	status := kuberikrolloutv1alpha1.HealthStatusHealthy
	if unhealthy {
		status = kuberikrolloutv1alpha1.HealthStatusUnhealthy
	}
	return requeue, true, r.setStatus(ctx, healthCheck, status, message)
}

func (r *HealthCheckReconciler) datadogAPI() (datadogAPI, error) {
	if r.DatadogAPI != nil {
		return r.DatadogAPI, nil
	}
	return newDatadogAPIFromEnv()
}

// evaluateMonitors is unhealthy while any selected monitor, restricted to the
// groups matching the group tags, is alerting.
func evaluateMonitors(ctx context.Context, api datadogAPI, annotations map[string]string) (bool, string, error) {
	ids, err := parseMonitorIDs(annotations[AnnotationDatadogMonitorIDs])
	if err != nil {
		return false, "", err
	}
	monitorTags := splitList(annotations[AnnotationDatadogMonitorTags])
	groupTags := splitList(annotations[AnnotationDatadogGroupTags])
	if len(ids) == 0 && len(monitorTags) == 0 {
		return false, "", fmt.Errorf("set %s and/or %s", AnnotationDatadogMonitorIDs, AnnotationDatadogMonitorTags)
	}

	monitors := map[int64]datadogMonitorState{}
	for _, id := range ids {
		monitor, err := api.GetMonitor(ctx, id)
		if err != nil {
			return false, "", err
		}
		monitors[monitor.ID] = monitor
	}
	if len(monitorTags) > 0 {
		listed, err := api.ListMonitorsByTags(ctx, monitorTags)
		if err != nil {
			return false, "", err
		}
		for _, monitor := range listed {
			if hasAllTags(monitor.Tags, monitorTags) {
				monitors[monitor.ID] = monitor
			}
		}
	}
	if len(monitors) == 0 {
		return false, fmt.Sprintf("No Datadog monitors carry tags %v; nothing to evaluate", monitorTags), nil
	}

	var alerting []string
	evaluated := 0
	for _, monitor := range monitors {
		statuses := monitorStatuses(monitor, groupTags)
		if len(statuses) == 0 {
			continue
		}
		evaluated++
		var alertingGroups []string
		for group, status := range statuses {
			if status == monitorStatusAlert {
				alertingGroups = append(alertingGroups, group)
			}
		}
		if len(alertingGroups) > 0 {
			sort.Strings(alertingGroups)
			alerting = append(alerting, fmt.Sprintf("%s [%s]", monitor.Name, strings.Join(alertingGroups, "; ")))
		}
	}
	if evaluated == 0 {
		return false, fmt.Sprintf("None of the %d matched Datadog monitors has groups matching %v; nothing to evaluate",
			len(monitors), groupTags), nil
	}
	if len(alerting) > 0 {
		sort.Strings(alerting)
		return true, fmt.Sprintf("%d of %d Datadog monitors alerting: %s",
			len(alerting), evaluated, summarize(alerting)), nil
	}
	return false, fmt.Sprintf("%d Datadog monitors OK", evaluated), nil
}

// monitorStatuses returns the status of each monitor group consistent with the
// group tags. A group is consistent unless it has one of the tag keys with a
// different value, so a monitor not grouped by a key is evaluated as a whole.
// Monitors without group state fall back to their overall state.
func monitorStatuses(monitor datadogMonitorState, groupTags []string) map[string]string {
	if len(monitor.Groups) == 0 {
		return map[string]string{"*": monitor.OverallState}
	}
	statuses := map[string]string{}
	for group, status := range monitor.Groups {
		if groupMatches(group, groupTags) {
			statuses[group] = status
		}
	}
	return statuses
}

func groupMatches(group string, groupTags []string) bool {
	values := map[string]string{}
	for _, tag := range strings.Split(group, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(tag), ":")
		if ok {
			values[key] = value
		}
	}
	for _, tag := range groupTags {
		key, want, ok := strings.Cut(tag, ":")
		if !ok {
			continue
		}
		if got, grouped := values[key]; grouped && got != want {
			return false
		}
	}
	return true
}

// evaluateIncidents is unhealthy while any open incident matches the query.
func evaluateIncidents(ctx context.Context, api datadogAPI, annotations map[string]string) (bool, string, error) {
	query := strings.TrimSpace(annotations[AnnotationDatadogIncidentQuery])
	if query == "" {
		return false, "", fmt.Errorf("set %s", AnnotationDatadogIncidentQuery)
	}
	incidents, err := api.SearchOpenIncidents(ctx, query)
	if err != nil {
		return false, "", err
	}
	if len(incidents) == 0 {
		return false, "No open Datadog incidents match", nil
	}
	described := make([]string, 0, len(incidents))
	for _, incident := range incidents {
		described = append(described, fmt.Sprintf("#%d %s", incident.PublicID, incident.Title))
	}
	return true, fmt.Sprintf("%d open Datadog incidents: %s", len(incidents), summarize(described)), nil
}

// setStatus writes status and message. While Unhealthy, lastErrorTime keeps the
// time the failure was first seen; the rollout controller clears it on a new
// deployment or retry, so a failure still present afterwards is stamped anew.
func (r *HealthCheckReconciler) setStatus(
	ctx context.Context,
	healthCheck *kuberikrolloutv1alpha1.HealthCheck,
	status kuberikrolloutv1alpha1.HealthStatus,
	message string,
) error {
	lastErrorTime := healthCheck.Status.LastErrorTime
	if status == kuberikrolloutv1alpha1.HealthStatusUnhealthy &&
		(healthCheck.Status.Status != kuberikrolloutv1alpha1.HealthStatusUnhealthy || lastErrorTime == nil) {
		now := metav1.Now()
		lastErrorTime = &now
	}

	currentMessage := ""
	if healthCheck.Status.Message != nil {
		currentMessage = *healthCheck.Status.Message
	}
	if healthCheck.Status.Status == status && currentMessage == message && lastErrorTime == healthCheck.Status.LastErrorTime {
		return nil
	}

	healthCheck.Status.Status = status
	healthCheck.Status.Message = &message
	healthCheck.Status.LastErrorTime = lastErrorTime
	return r.Status().Update(ctx, healthCheck)
}

func pollInterval(healthCheck *kuberikrolloutv1alpha1.HealthCheck) time.Duration {
	if value := strings.TrimSpace(healthCheck.Annotations[AnnotationRequeueInterval]); value != "" {
		if interval, err := time.ParseDuration(value); err == nil && interval >= 10*time.Second {
			return interval
		}
	}
	return defaultDatadogPollInterval
}

func parseMonitorIDs(value string) ([]int64, error) {
	items := splitList(value)
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		id, err := strconv.ParseInt(item, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid monitor id %q in %s", item, AnnotationDatadogMonitorIDs)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func hasAllTags(tags []string, required []string) bool {
	present := map[string]bool{}
	for _, tag := range tags {
		present[tag] = true
	}
	for _, tag := range required {
		if !present[tag] {
			return false
		}
	}
	return true
}

func summarize(items []string) string {
	if len(items) <= maxListedInMessage {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:maxListedInMessage], ", "), len(items)-maxListedInMessage)
}
