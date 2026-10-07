/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// MultiClusterBackendKind is the kind of the MultiClusterBackend resource.
	MultiClusterBackendKind = "MultiClusterBackend"

	// MultiClusterBackendSelectedMemberLimit is the maximum number of selected members supported by the POC.
	MultiClusterBackendSelectedMemberLimit = 40
)

// MultiClusterBackendConditionType identifies a condition on a MultiClusterBackend.
type MultiClusterBackendConditionType string

const (
	// MultiClusterBackendConditionAccepted indicates whether the backend configuration is valid.
	MultiClusterBackendConditionAccepted MultiClusterBackendConditionType = "Accepted"
	// MultiClusterBackendConditionResolvedRefs indicates whether the backend has at least one ready origin.
	MultiClusterBackendConditionResolvedRefs MultiClusterBackendConditionType = "ResolvedRefs"
	// MultiClusterBackendConditionProgrammed indicates whether the desired Azure configuration is programmed.
	MultiClusterBackendConditionProgrammed MultiClusterBackendConditionType = "Programmed"
)

// MultiClusterBackendConditionReason explains a MultiClusterBackend condition.
type MultiClusterBackendConditionReason string

const (
	// MultiClusterBackendReasonAccepted indicates that the backend configuration is valid.
	MultiClusterBackendReasonAccepted MultiClusterBackendConditionReason = "Accepted"
	// MultiClusterBackendReasonInvalid indicates that the backend configuration is invalid.
	MultiClusterBackendReasonInvalid MultiClusterBackendConditionReason = "Invalid"
	// MultiClusterBackendReasonNoMatchingClusters indicates that the selector matched no eligible members.
	MultiClusterBackendReasonNoMatchingClusters MultiClusterBackendConditionReason = "NoMatchingClusters"
	// MultiClusterBackendReasonTooManySelectedClusters indicates that selection exceeded the POC limit.
	MultiClusterBackendReasonTooManySelectedClusters MultiClusterBackendConditionReason = "TooManySelectedClusters"
	// MultiClusterBackendReasonReadyOriginsAvailable indicates that at least one selected origin is ready.
	MultiClusterBackendReasonReadyOriginsAvailable MultiClusterBackendConditionReason = "ReadyOriginsAvailable"
	// MultiClusterBackendReasonNoReadyOrigins indicates that no selected origin is ready.
	MultiClusterBackendReasonNoReadyOrigins MultiClusterBackendConditionReason = "NoReadyOrigins"
	// MultiClusterBackendReasonProgrammed indicates that the backend matches observed Azure state.
	MultiClusterBackendReasonProgrammed MultiClusterBackendConditionReason = "Programmed"
	// MultiClusterBackendReasonProgramming indicates that backend programming is in progress.
	MultiClusterBackendReasonProgramming MultiClusterBackendConditionReason = "Programming"
)

// MultiClusterBackendMemberConditionType identifies a condition on a selected member.
type MultiClusterBackendMemberConditionType string

const (
	// MultiClusterBackendMemberConditionReady indicates whether the selected member has a ready origin.
	MultiClusterBackendMemberConditionReady MultiClusterBackendMemberConditionType = "Ready"
)

// MultiClusterBackendMemberConditionReason explains a selected member condition.
type MultiClusterBackendMemberConditionReason string

const (
	// MultiClusterBackendMemberReasonReady indicates that the member origin is ready.
	MultiClusterBackendMemberReasonReady MultiClusterBackendMemberConditionReason = "Ready"
	// MultiClusterBackendMemberReasonServiceNotFound indicates that the member-local Service does not exist.
	MultiClusterBackendMemberReasonServiceNotFound MultiClusterBackendMemberConditionReason = "ServiceNotFound"
	// MultiClusterBackendMemberReasonPortNotFound indicates that the requested Service port does not exist.
	MultiClusterBackendMemberReasonPortNotFound MultiClusterBackendMemberConditionReason = "PortNotFound"
	// MultiClusterBackendMemberReasonLoadBalancerNotReady indicates that the internal load balancer is not ready.
	MultiClusterBackendMemberReasonLoadBalancerNotReady MultiClusterBackendMemberConditionReason = "LoadBalancerNotReady"
	// MultiClusterBackendMemberReasonPrivateLinkServiceNotReady indicates that the Private Link Service is not ready.
	MultiClusterBackendMemberReasonPrivateLinkServiceNotReady MultiClusterBackendMemberConditionReason = "PrivateLinkServiceNotReady"
	// MultiClusterBackendMemberReasonApprovalPending indicates that Private Link approval is pending.
	MultiClusterBackendMemberReasonApprovalPending MultiClusterBackendMemberConditionReason = "ApprovalPending"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories={fleet-networking},shortName=mcb
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:JSONPath=`.status.selectedClusters`,name="Selected",type=integer
// +kubebuilder:printcolumn:JSONPath=`.status.readyOrigins`,name="Ready",type=integer
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=='Programmed')].status`,name="Programmed",type=string
// +kubebuilder:printcolumn:JSONPath=`.metadata.creationTimestamp`,name="Age",type=date

// MultiClusterBackend selects Fleet member clusters that host a logical Service backend.
// This API is experimental and is intended only for the Azure Front Door Private Link POC.
type MultiClusterBackend struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the desired state of the backend.
	// +required
	Spec MultiClusterBackendSpec `json:"spec"`

	// Status is the observed state of the backend.
	// +optional
	Status MultiClusterBackendStatus `json:"status,omitempty"`
}

// MultiClusterBackendSpec defines the desired state of a MultiClusterBackend.
type MultiClusterBackendSpec struct {
	// Service identifies the member-local Service shared by all selected clusters.
	// +required
	Service MultiClusterBackendService `json:"service"`

	// ClusterSelector selects eligible Fleet MemberCluster objects by metadata labels.
	// An empty selector is rejected to prevent accidental fleet-wide selection.
	// +required
	// +kubebuilder:validation:XValidation:rule="(has(self.matchLabels) && size(self.matchLabels) > 0) || (has(self.matchExpressions) && size(self.matchExpressions) > 0)",message="clusterSelector must contain at least one matchLabel or matchExpression"
	ClusterSelector metav1.LabelSelector `json:"clusterSelector"`

	// HealthProbe configures the HTTP health probe for the AFD origin group.
	// +optional
	HealthProbe *MultiClusterBackendHealthProbe `json:"healthProbe,omitempty"`
}

// MultiClusterBackendService identifies a member-local Service and port.
type MultiClusterBackendService struct {
	// Name is the Service name in the MultiClusterBackend namespace.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Port is the numeric Service port.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// MultiClusterBackendHealthProbe configures the backend health probe.
type MultiClusterBackendHealthProbe struct {
	// Path is the absolute HTTP probe path.
	// +optional
	// +kubebuilder:default="/"
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Pattern=`^/.*$`
	Path *string `json:"path,omitempty"`
}

// MultiClusterBackendStatus defines the observed state of a MultiClusterBackend.
type MultiClusterBackendStatus struct {
	// ObservedGeneration is the generation most recently reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SelectedClusters is the number of eligible MemberClusters selected by the backend.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=40
	SelectedClusters int32 `json:"selectedClusters,omitempty"`

	// ReadyOrigins is the number of selected members with ready origin infrastructure.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=40
	ReadyOrigins int32 `json:"readyOrigins,omitempty"`

	// Conditions contains the current backend conditions.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Members contains status for each selected member cluster.
	// +optional
	// +listType=map
	// +listMapKey=clusterName
	// +kubebuilder:validation:MaxItems=40
	Members []MultiClusterBackendMemberStatus `json:"members,omitempty"`
}

// MultiClusterBackendMemberStatus describes origin readiness for a selected member cluster.
type MultiClusterBackendMemberStatus struct {
	// ClusterName is the selected MemberCluster name.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ClusterName string `json:"clusterName"`

	// Conditions contains the current conditions for this member.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true

// MultiClusterBackendList contains a list of MultiClusterBackend resources.
type MultiClusterBackendList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MultiClusterBackend `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MultiClusterBackend{}, &MultiClusterBackendList{})
}
