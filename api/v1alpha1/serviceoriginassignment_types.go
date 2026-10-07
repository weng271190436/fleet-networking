/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// ServiceOriginAssignmentKind is the kind of the ServiceOriginAssignment resource.
	ServiceOriginAssignmentKind = "ServiceOriginAssignment"
)

// ServiceOriginConnectivityType identifies how AFD connects to a member origin.
type ServiceOriginConnectivityType string

const (
	// ServiceOriginConnectivityTypePrivateLink requires Azure Private Link connectivity.
	ServiceOriginConnectivityTypePrivateLink ServiceOriginConnectivityType = "PrivateLink"
)

// ServiceOriginAssignmentConditionType identifies a condition on a ServiceOriginAssignment.
type ServiceOriginAssignmentConditionType string

const (
	// ServiceOriginAssignmentConditionServiceResolved indicates whether the member-local Service and port resolve.
	ServiceOriginAssignmentConditionServiceResolved ServiceOriginAssignmentConditionType = "ServiceResolved"
	// ServiceOriginAssignmentConditionInfrastructureReady indicates whether the ILB and PLS are ready.
	ServiceOriginAssignmentConditionInfrastructureReady ServiceOriginAssignmentConditionType = "InfrastructureReady"
	// ServiceOriginAssignmentConditionPrivateLinkApproved indicates whether the matching connection is approved.
	ServiceOriginAssignmentConditionPrivateLinkApproved ServiceOriginAssignmentConditionType = "PrivateLinkApproved"
)

// ServiceOriginAssignmentConditionReason explains a ServiceOriginAssignment condition.
type ServiceOriginAssignmentConditionReason string

const (
	// ServiceOriginAssignmentReasonServiceResolved indicates that the Service and port resolved.
	ServiceOriginAssignmentReasonServiceResolved ServiceOriginAssignmentConditionReason = "ServiceResolved"
	// ServiceOriginAssignmentReasonServiceNotFound indicates that the member-local Service does not exist.
	ServiceOriginAssignmentReasonServiceNotFound ServiceOriginAssignmentConditionReason = "ServiceNotFound"
	// ServiceOriginAssignmentReasonPortNotFound indicates that the requested Service port does not exist.
	ServiceOriginAssignmentReasonPortNotFound ServiceOriginAssignmentConditionReason = "PortNotFound"
	// ServiceOriginAssignmentReasonInfrastructureReady indicates that the ILB and PLS are ready.
	ServiceOriginAssignmentReasonInfrastructureReady ServiceOriginAssignmentConditionReason = "InfrastructureReady"
	// ServiceOriginAssignmentReasonLoadBalancerNotReady indicates that the ILB is not ready.
	ServiceOriginAssignmentReasonLoadBalancerNotReady ServiceOriginAssignmentConditionReason = "LoadBalancerNotReady"
	// ServiceOriginAssignmentReasonPrivateLinkServiceNotReady indicates that the PLS is not ready.
	ServiceOriginAssignmentReasonPrivateLinkServiceNotReady ServiceOriginAssignmentConditionReason = "PrivateLinkServiceNotReady"
	// ServiceOriginAssignmentReasonConnectionApproved indicates that the matching connection is approved.
	ServiceOriginAssignmentReasonConnectionApproved ServiceOriginAssignmentConditionReason = "ConnectionApproved"
	// ServiceOriginAssignmentReasonConnectionPending indicates that the matching connection is pending.
	ServiceOriginAssignmentReasonConnectionPending ServiceOriginAssignmentConditionReason = "ConnectionPending"
	// ServiceOriginAssignmentReasonApprovalValidationFailed indicates that approval validation failed.
	ServiceOriginAssignmentReasonApprovalValidationFailed ServiceOriginAssignmentConditionReason = "ApprovalValidationFailed"
	// ServiceOriginAssignmentReasonPrivateLinkApprovalFailed indicates that the Azure approval operation failed.
	ServiceOriginAssignmentReasonPrivateLinkApprovalFailed ServiceOriginAssignmentConditionReason = "PrivateLinkApprovalFailed"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,categories={fleet-networking},shortName=soa
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:JSONPath=`.spec.serviceRef.namespace`,name="Service-Namespace",type=string
// +kubebuilder:printcolumn:JSONPath=`.spec.serviceRef.name`,name="Service",type=string
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=='InfrastructureReady')].status`,name="Infrastructure-Ready",type=string
// +kubebuilder:printcolumn:JSONPath=`.status.conditions[?(@.type=='PrivateLinkApproved')].status`,name="PrivateLink-Approved",type=string
// +kubebuilder:printcolumn:JSONPath=`.metadata.creationTimestamp`,name="Age",type=date

// ServiceOriginAssignment requests member-local origin discovery for a MultiClusterBackend.
// This internal API is experimental and must not be created directly by users.
type ServiceOriginAssignment struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is owned by the hub backend-selection controller.
	// +required
	Spec ServiceOriginAssignmentSpec `json:"spec"`

	// Status is owned by the selected member networking controller.
	// +optional
	Status ServiceOriginAssignmentStatus `json:"status,omitempty"`
}

// ServiceOriginAssignmentSpec defines the origin discovery request.
type ServiceOriginAssignmentSpec struct {
	// BackendRef identifies the MultiClusterBackend that owns this assignment.
	// +required
	BackendRef ServiceOriginAssignmentBackendReference `json:"backendRef"`

	// ServiceRef identifies the member-local Service to discover.
	// +required
	ServiceRef ServiceOriginAssignmentServiceReference `json:"serviceRef"`

	// Connectivity identifies the required origin connectivity.
	// +required
	Connectivity ServiceOriginAssignmentConnectivity `json:"connectivity"`

	// Approval contains correlation data for Private Link approval.
	// +required
	Approval ServiceOriginAssignmentApproval `json:"approval"`
}

// ServiceOriginAssignmentBackendReference identifies the owning MultiClusterBackend.
type ServiceOriginAssignmentBackendReference struct {
	// Namespace is the backend namespace.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`

	// Name is the backend name.
	// +required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	Name string `json:"name"`

	// UID is the immutable backend identity.
	// +required
	// +kubebuilder:validation:XValidation:rule="self != ''",message="backendRef.uid must not be empty"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="backendRef.uid is immutable"
	UID types.UID `json:"uid"`
}

// ServiceOriginAssignmentServiceReference identifies a member-local Service and port.
type ServiceOriginAssignmentServiceReference struct {
	// Namespace is the Service namespace.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`

	// Name is the Service name.
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

// ServiceOriginAssignmentConnectivity defines the required origin connectivity.
type ServiceOriginAssignmentConnectivity struct {
	// Type is the origin connectivity type.
	// +required
	// +kubebuilder:validation:Enum=PrivateLink
	Type ServiceOriginConnectivityType `json:"type"`
}

// ServiceOriginAssignmentApproval contains Private Link approval correlation data.
type ServiceOriginAssignmentApproval struct {
	// RequestToken is unique and unpredictable for this assignment UID.
	// It is correlation data, not a credential.
	// +required
	// +kubebuilder:validation:MinLength=32
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9._~-]+$`
	RequestToken string `json:"requestToken"`
}

// ServiceOriginAssignmentStatus defines member-observed origin state.
type ServiceOriginAssignmentStatus struct {
	// ObservedGeneration is the assignment generation most recently processed by the member.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Origin contains discovered member-local Azure infrastructure.
	// +optional
	Origin *ServiceOriginAssignmentOriginStatus `json:"origin,omitempty"`

	// Conditions contains the current discovery and approval conditions.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// ServiceOriginAssignmentOriginStatus contains discovered origin infrastructure.
type ServiceOriginAssignmentOriginStatus struct {
	// AzureLocation is the Azure location of the Private Link Service.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+$`
	AzureLocation string `json:"azureLocation"`

	// LoadBalancerAddress is the member-local internal load balancer IP address.
	// +required
	// +kubebuilder:validation:Format=ip
	LoadBalancerAddress string `json:"loadBalancerAddress"`

	// PrivateLinkServiceID is the full Azure resource ID of the member-local Private Link Service.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	PrivateLinkServiceID string `json:"privateLinkServiceID"`
}

// +kubebuilder:object:root=true

// ServiceOriginAssignmentList contains a list of ServiceOriginAssignment resources.
type ServiceOriginAssignmentList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceOriginAssignment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServiceOriginAssignment{}, &ServiceOriginAssignmentList{})
}
