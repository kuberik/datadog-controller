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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MonitorCheckSpec defines the desired state of MonitorCheck
type MonitorCheckSpec struct {
	// LabelSelector specifies which DatadogMonitors to watch
	// +kubebuilder:validation:Required
	LabelSelector metav1.LabelSelector `json:"labelSelector"`

	// HealthCheckTemplate defines the template for creating kuberik HealthCheck resources
	// +kubebuilder:validation:Required
	HealthCheckTemplate HealthCheckTemplate `json:"healthCheckTemplate"`
}

// HealthCheckTemplate defines the template for creating kuberik HealthCheck resources
// Similar to PodTemplate in Kubernetes deployments
type HealthCheckTemplate struct {
	// Labels specifies additional labels for the HealthCheck resource
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations specifies additional annotations for the HealthCheck resource
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// MonitorCheckStatus defines the observed state of MonitorCheck
type MonitorCheckStatus struct {
	// Conditions represent the latest available observations of a MonitorCheck's current state
	// +listType=map
	// +listMapKey=type
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// MatchedMonitorsCount is the total number of DatadogMonitors that match the label selector
	// +optional
	MatchedMonitorsCount int32 `json:"matchedMonitorsCount,omitempty"`

	// HealthyMonitorsCount is the number of healthy DatadogMonitors
	// +optional
	HealthyMonitorsCount int32 `json:"healthyMonitorsCount,omitempty"`

	// UnhealthyMonitorsCount is the number of unhealthy DatadogMonitors
	// +optional
	UnhealthyMonitorsCount int32 `json:"unhealthyMonitorsCount,omitempty"`

	// LastCheckTime is the timestamp of the last health check
	// +optional
	LastCheckTime *metav1.Time `json:"lastCheckTime,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Matched",type="integer",JSONPath=".status.matchedMonitorsCount"
//+kubebuilder:printcolumn:name="Healthy",type="integer",JSONPath=".status.healthyMonitorsCount"
//+kubebuilder:printcolumn:name="Unhealthy",type="integer",JSONPath=".status.unhealthyMonitorsCount"
//+kubebuilder:printcolumn:name="Last Check",type="date",JSONPath=".status.lastCheckTime"

// MonitorCheck is the Schema for the monitorchecks API
type MonitorCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MonitorCheckSpec   `json:"spec,omitempty"`
	Status MonitorCheckStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MonitorCheckList contains a list of MonitorCheck
type MonitorCheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MonitorCheck `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MonitorCheck{}, &MonitorCheckList{})
}
