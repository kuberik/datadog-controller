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
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kuberikrolloutv1alpha1 "github.com/kuberik/rollout-controller/api/v1alpha1"
)

// maxFailures caps HealthCheck.status.failures (the CRD's MaxItems).
const maxFailures = 16

const (
	maxFailureSourceLen  = 256
	maxFailureMessageLen = 256
)

// severityFromMonitorPriority maps a Datadog monitor priority: P1 and P2 are
// Critical, P3 to P5 are Warning, and no priority (nil or 0) or any other value
// is Critical.
func severityFromMonitorPriority(priority *int64) kuberikrolloutv1alpha1.FailureSeverity {
	if priority != nil && *priority >= 3 && *priority <= 5 {
		return kuberikrolloutv1alpha1.FailureSeverityWarning
	}
	return kuberikrolloutv1alpha1.FailureSeverityCritical
}

// severityFromIncident maps a Datadog incident severity: SEV-1 and SEV-2 are
// Critical, SEV-3 to SEV-5 are Warning, and an empty, UNKNOWN or other value is
// Critical. Case-insensitive.
func severityFromIncident(severity string) kuberikrolloutv1alpha1.FailureSeverity {
	switch strings.ToUpper(strings.TrimSpace(severity)) {
	case "SEV-3", "SEV-4", "SEV-5":
		return kuberikrolloutv1alpha1.FailureSeverityWarning
	}
	return kuberikrolloutv1alpha1.FailureSeverityCritical
}

// severityOverride reads the kuberik.com/severity annotation on the HealthCheck.
// It returns false when the annotation is absent; Warning is Warning and any
// other value counts as Critical, as on every other object (SeverityAnnotation).
func severityOverride(annotations map[string]string) (kuberikrolloutv1alpha1.FailureSeverity, bool) {
	v, ok := annotations[kuberikrolloutv1alpha1.SeverityAnnotation]
	if !ok || v == "" {
		return "", false
	}
	if kuberikrolloutv1alpha1.FailureSeverity(v) == kuberikrolloutv1alpha1.FailureSeverityWarning {
		return kuberikrolloutv1alpha1.FailureSeverityWarning, true
	}
	return kuberikrolloutv1alpha1.FailureSeverityCritical, true
}

// applySeverityOverride sets every failure's severity to the HealthCheck's
// kuberik.com/severity annotation when it is valid.
func applySeverityOverride(failures []kuberikrolloutv1alpha1.HealthCheckFailure, annotations map[string]string) []kuberikrolloutv1alpha1.HealthCheckFailure {
	if sev, ok := severityOverride(annotations); ok {
		for i := range failures {
			failures[i].Severity = sev
		}
	}
	return failures
}

// clip cuts s to n bytes. The source and message limits are both 256 today; each call names its own limit.
//
//nolint:unparam // two named limits that happen to be equal
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// alertingMonitorFailure is the failure for one alerting monitor.
func alertingMonitorFailure(name string, id int64, priority *int64, alertingGroups []string, now metav1.Time) kuberikrolloutv1alpha1.HealthCheckFailure {
	if name == "" {
		name = fmt.Sprintf("monitor %d", id)
	}
	msg := "Monitor alerting"
	if len(alertingGroups) > 0 {
		sorted := append([]string(nil), alertingGroups...)
		sort.Strings(sorted)
		msg = "Monitor alerting for " + strings.Join(sorted, "; ")
	}
	return kuberikrolloutv1alpha1.HealthCheckFailure{
		Source:        clip("Alert "+name, maxFailureSourceLen),
		Kind:          kuberikrolloutv1alpha1.FailureKindAlert,
		Severity:      severityFromMonitorPriority(priority),
		Reason:        "MonitorAlerting",
		Message:       clip(msg, maxFailureMessageLen),
		FirstSeenTime: now,
	}
}

// disambiguateSources appends the monitor id to a source shared by several
// failures, so same-named monitors keep separate entries. ids parallels failures.
func disambiguateSources(failures []kuberikrolloutv1alpha1.HealthCheckFailure, ids []int64) {
	counts := map[string]int{}
	for _, f := range failures {
		counts[f.Source]++
	}
	for i := range failures {
		if counts[failures[i].Source] > 1 {
			failures[i].Source = clip(fmt.Sprintf("%s (#%d)", failures[i].Source, ids[i]), maxFailureSourceLen)
		}
	}
}

// incidentFailure is the failure for one open incident.
func incidentFailure(incident datadogIncident, now metav1.Time) kuberikrolloutv1alpha1.HealthCheckFailure {
	return kuberikrolloutv1alpha1.HealthCheckFailure{
		Source:        clip(fmt.Sprintf("Incident #%d %s", incident.PublicID, incident.Title), maxFailureSourceLen),
		Kind:          kuberikrolloutv1alpha1.FailureKindIncident,
		Severity:      severityFromIncident(incident.Severity),
		Reason:        "IncidentOpen",
		Message:       clip(incident.Title, maxFailureMessageLen),
		FirstSeenTime: now,
	}
}

// mergeFailures folds the failures seen now into the recorded ones (the
// witness). Entries are keyed by source and kind and keep their first-seen time;
// entries no longer failing stay, so the list shrinks only when rollout-controller
// resets it at the next deploy or retry. At most maxFailures are kept, oldest
// first. Mirrors rollout-controller's mergeFailures.
func mergeFailures(recorded, current []kuberikrolloutv1alpha1.HealthCheckFailure) []kuberikrolloutv1alpha1.HealthCheckFailure {
	out := make([]kuberikrolloutv1alpha1.HealthCheckFailure, len(recorded))
	copy(out, recorded)
	for _, f := range current {
		idx := -1
		for i := range out {
			if out[i].Source == f.Source && out[i].Kind == f.Kind {
				idx = i
				break
			}
		}
		if idx >= 0 {
			f.FirstSeenTime = out[idx].FirstSeenTime
			out[idx] = f
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].FirstSeenTime.Before(&out[j].FirstSeenTime) })
	if len(out) > maxFailures {
		out = out[:maxFailures]
	}
	return out
}
