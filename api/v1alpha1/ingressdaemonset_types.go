/*

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// IngressDaemonSetSpec defines the desired state of IngressDaemonSet
type IngressDaemonSetSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	UpdateStrategy UpdateStrategySpec `json:"updateStrategy,omitempty"`

	// HealthCheckNodePorts is the list of ports bound by the health-checker daemonset to serve health-check http requests from the
	// external loadbalancer.
	//
	// You usually include only one port in the list, and add the second port only when you need to migrate the
	// health-check port.
	HealthCheckNodePorts []int `json:"healthCheckNodePorts,omitempty"`

	// HealthCheckerSpec is used for configuring the health-checking daemonset
	HealthChecker HealthCheckerSpec `json:"healthChecker,omitempty"`

	// NodeSelector is a set of key-value pairs for selecting nodes to schedule ingress daemonset pods
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	Template corev1.PodTemplateSpec `json:"template,omitempty"`
}

type UpdateStrategySpec struct {
	// Type is the type of strategy. The only supported type is "RollingUpdate"
	Type string `json:"type,omitempty"`

	// RollingUpdate contains various configuration options for the RollingUpdate update strategy
	RollingUpdate RollingUpdateSpec `json:"rollingUpdate,omitempty"`
}

type RollingUpdateSpec struct {
	// MaxUnavailableNodes is the max number of nodes for which the controller runs rolling-update with
	// detaching the node from, and after pods got updated, attaching the node to the external loadbalancer.
	//
	// The default value is 0, which means it relies on maxUnavaiablePodsPerNode and maxDegaradedNodes only.
	// +optional
	MaxUnavailableNodes *int `json:"maxUnavailableNodes,omitempty"`

	// MaxDegradedNodes is the max number of nodes for which the controller runs rolling-updates without detaching node
	// from external loadbalancer.
	//
	// The default is 1. You can only set either of `maxUnavaiableNodes` or `maxDegradedNodes` to 1 or greater.
	// +optional
	MaxDegradedNodes *int `json:"maxDegradedNodes,omitempty"`

	// MaxUnavailablePodsPerNode is the number for configuring updateStrategy.rollingUpdate.maxUnavailable for the per-node
	// deployment
	MaxUnavailablePodsPerNode *intstr.IntOrString `json:"maxUnavailablePodsPerNodes,omitempty"`

	// MaxSurgedPodsPerNode is the maxSurge for the per-node deployment
	MaxSurgedPodsPerNode *intstr.IntOrString `json:"maxSurgedPodsPerNode,omitempty"`

	// AnnotateNodeToDetach configures the controller to annotate the node before updating pods scheduled onto the node.
	// It can either be (1) annotation key or (2) annotation key=value.
	// When the first option is used, the controller annotate the node with the specified key, without an empty value.
	AnnotateNodeToDetach *AnnotateNodeToDetach `json:"annotateNodeToDetach,omitempty"`

	// WaitForDetachmentByAnnotatedTimestamp configures the controller to wait for the certain period since the detachment
	// timestamp stored in the specified annotation.
	//
	// This depends on and requires configuring AnnotateNodeToDetach, too.
	WaitForDetachmentByAnnotatedTimestamp *WaitForDetachmentByAnnotatedTimestamp `json:"waitForDetachmentByAnnotatedTimestamp,omitempty"`
}

type AnnotateNodeToDetach struct {
	// Key is the annotation key
	Key string `json:"key,omitempty"`

	// Value is the annotation value
	// +optional
	Value *string `json:"value,omitempty"`

	// GracePeriodSeconds is the duration in seconds to wait before start rolling pods and after annotating the node.
	// If WaitForDetachmentByAnnotatedTimestamp is also set, the controller waits for this grace period passes before
	// waiting with the annotated timestamp.
	GracePeriodSeconds int `json:"gracePeriodSeconds,omitempty"`
}

type WaitForDetachmentByAnnotatedTimestamp struct {
	// Key is the key of the annotation to extract the timestamp
	Key string `json:"key,omitempty"`

	// Format is the format of the timestamp saved in the annotation value.
	// The only supported value is "RFC3339".
	// +optinal
	Format *string `json:"format,omitempty"`

	// GracePeriodSeconds is the duration in seconds to wait before start rolling pods and after the time stored in the
	// timestamp
	GracePeriodSeconds int `json:"gracePeriodSeconds,omitempty"`
}

type HealthCheckerSpec struct {
	Image              string            `json:"image,omitempty"`
	ImagePullPolicy    corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
}

// IngressDaemonSetStatus defines the observed state of IngressDaemonSet
type IngressDaemonSetStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true

// IngressDaemonSet is the Schema for the ingressdaemonsets API
type IngressDaemonSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IngressDaemonSetSpec   `json:"spec,omitempty"`
	Status IngressDaemonSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IngressDaemonSetList contains a list of IngressDaemonSet
type IngressDaemonSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IngressDaemonSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IngressDaemonSet{}, &IngressDaemonSetList{})
}
