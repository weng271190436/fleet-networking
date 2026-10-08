/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package gatewaymodel

import (
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
)

func TestBuildMultiClusterBackend_TwoReadyAssignments(t *testing.T) {
	probePath := "/ready"
	backend := &fleetnetv1alpha1.MultiClusterBackend{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "echo", UID: types.UID("backend-uid")},
		Spec: fleetnetv1alpha1.MultiClusterBackendSpec{
			Service:     fleetnetv1alpha1.MultiClusterBackendService{Name: "echo", Port: 8080},
			HealthProbe: &fleetnetv1alpha1.MultiClusterBackendHealthProbe{Path: &probePath},
		},
	}
	assignments := []fleetnetv1alpha1.ServiceOriginAssignment{
		readyAssignment("member-b", "assignment-b", "token-b", "10.0.0.2", "westus2", "/subscriptions/sub/resourceGroups/member-b/providers/Microsoft.Network/privateLinkServices/pls-b"),
		readyAssignment("member-a", "assignment-a", "token-a", "10.0.0.1", "eastus", "/subscriptions/sub/resourceGroups/member-a/providers/Microsoft.Network/privateLinkServices/pls-a"),
	}

	got, err := BuildMultiClusterBackend(backend, assignments)
	if err != nil {
		t.Fatalf("BuildMultiClusterBackend() error = %v", err)
	}

	if got.Namespace != "apps" || got.Name != "echo" || got.UID != "backend-uid" {
		t.Errorf("backend identity = %#v, want apps/echo backend-uid", got)
	}
	if got.HealthProbePath != "/ready" {
		t.Errorf("HealthProbePath = %q, want /ready", got.HealthProbePath)
	}
	if got.OriginGroupName != "og-echo-759363be" {
		t.Errorf("OriginGroupName = %q, want deterministic name", got.OriginGroupName)
	}
	if len(got.Origins) != 2 {
		t.Fatalf("Origins = %d, want 2", len(got.Origins))
	}
	if got.Origins[0].Cluster != "member-a" || got.Origins[1].Cluster != "member-b" {
		t.Errorf("origin order = %#v, want member-a then member-b", got.Origins)
	}
	if got.Origins[0].Name != "origin-member-a-ce9c9ffd" {
		t.Errorf("origin name = %q, want deterministic assignment-derived name", got.Origins[0].Name)
	}
	if got.Origins[0].RequestMessage != "fleet:assignment-a:token-a" {
		t.Errorf("request message = %q, want exact assignment correlation message", got.Origins[0].RequestMessage)
	}
	if got.Origins[0].Connectivity != string(fleetnetv1alpha1.ServiceOriginConnectivityTypePrivateLink) ||
		got.Origins[0].PrivateLinkResourceID == "" {
		t.Errorf("origin = %#v, want required Private Link", got.Origins[0])
	}
}

func TestBuildMultiClusterBackend_DeepCopiesAndDefaults(t *testing.T) {
	backend := &fleetnetv1alpha1.MultiClusterBackend{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "echo", UID: types.UID("backend-uid")},
		Spec: fleetnetv1alpha1.MultiClusterBackendSpec{
			Service: fleetnetv1alpha1.MultiClusterBackendService{Name: "echo", Port: 80},
		},
	}
	assignments := []fleetnetv1alpha1.ServiceOriginAssignment{
		readyAssignment("member-a", "assignment-a", "token-a", "10.0.0.1", "eastus", "/subscriptions/sub/resourceGroups/member-a/providers/Microsoft.Network/privateLinkServices/pls-a"),
	}

	first, err := BuildMultiClusterBackend(backend, assignments)
	if err != nil {
		t.Fatalf("BuildMultiClusterBackend() error = %v", err)
	}
	assignments[0].Status.Origin.LoadBalancerAddress = "10.9.9.9"
	second, err := BuildMultiClusterBackend(backend, assignments)
	if err != nil {
		t.Fatalf("BuildMultiClusterBackend() second error = %v", err)
	}

	if first.HealthProbePath != "/" {
		t.Errorf("default HealthProbePath = %q, want /", first.HealthProbePath)
	}
	if first.Origins[0].Endpoint != "10.0.0.1" {
		t.Errorf("first origin mutated to %q", first.Origins[0].Endpoint)
	}
	if reflect.DeepEqual(first, second) {
		t.Error("second model did not reflect changed source assignment")
	}
}

func TestBuildMultiClusterBackend_RejectsReadyNonPrivateOrigin(t *testing.T) {
	backend := &fleetnetv1alpha1.MultiClusterBackend{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "echo", UID: types.UID("backend-uid")},
		Spec:       fleetnetv1alpha1.MultiClusterBackendSpec{Service: fleetnetv1alpha1.MultiClusterBackendService{Name: "echo", Port: 80}},
	}
	assignment := readyAssignment("member-a", "assignment-a", "token-a", "10.0.0.1", "eastus", "")
	assignment.Spec.Connectivity.Type = ""

	if _, err := BuildMultiClusterBackend(backend, []fleetnetv1alpha1.ServiceOriginAssignment{assignment}); err == nil {
		t.Fatal("BuildMultiClusterBackend() error = nil, want Private Link validation error")
	}
}

func TestBuildMultiClusterBackend_TruncatesAzureResourceNames(t *testing.T) {
	backend := &fleetnetv1alpha1.MultiClusterBackend{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "apps",
			Name:      "backend.with.a.very.long.name.that.must.be.truncated.for.azure",
			UID:       types.UID("backend-uid"),
		},
		Spec: fleetnetv1alpha1.MultiClusterBackendSpec{
			Service: fleetnetv1alpha1.MultiClusterBackendService{Name: "echo", Port: 80},
		},
	}
	assignment := readyAssignment(
		"member.with.a.very.long.name.that.must.be.truncated.for.azure",
		"assignment-a",
		"token-a",
		"10.0.0.1",
		"eastus",
		"/subscriptions/sub/resourceGroups/member-a/providers/Microsoft.Network/privateLinkServices/pls-a",
	)
	assignment.Spec.BackendRef.Name = backend.Name

	got, err := BuildMultiClusterBackend(backend, []fleetnetv1alpha1.ServiceOriginAssignment{assignment})
	if err != nil {
		t.Fatalf("BuildMultiClusterBackend() error = %v", err)
	}
	if len(got.OriginGroupName) > maxAFDChildResourceNameLength {
		t.Errorf("OriginGroupName length = %d, want <= %d", len(got.OriginGroupName), maxAFDChildResourceNameLength)
	}
	if len(got.Origins[0].Name) > maxAFDChildResourceNameLength {
		t.Errorf("origin name length = %d, want <= %d", len(got.Origins[0].Name), maxAFDChildResourceNameLength)
	}
}

func readyAssignment(cluster, uid, token, address, location, privateLinkServiceID string) fleetnetv1alpha1.ServiceOriginAssignment {
	return fleetnetv1alpha1.ServiceOriginAssignment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "assignment-" + cluster,
			Namespace:  "fleet-member-" + cluster,
			UID:        types.UID(uid),
			Generation: 2,
		},
		Spec: fleetnetv1alpha1.ServiceOriginAssignmentSpec{
			BackendRef: fleetnetv1alpha1.ServiceOriginAssignmentBackendReference{
				Namespace: "apps",
				Name:      "echo",
				UID:       types.UID("backend-uid"),
			},
			Connectivity: fleetnetv1alpha1.ServiceOriginAssignmentConnectivity{
				Type: fleetnetv1alpha1.ServiceOriginConnectivityTypePrivateLink,
			},
			Approval: fleetnetv1alpha1.ServiceOriginAssignmentApproval{RequestToken: token},
		},
		Status: fleetnetv1alpha1.ServiceOriginAssignmentStatus{
			ObservedGeneration: 2,
			Origin: &fleetnetv1alpha1.ServiceOriginAssignmentOriginStatus{
				AzureLocation:        location,
				LoadBalancerAddress:  address,
				PrivateLinkServiceID: privateLinkServiceID,
			},
			Conditions: []metav1.Condition{{
				Type:   string(fleetnetv1alpha1.ServiceOriginAssignmentConditionInfrastructureReady),
				Status: metav1.ConditionTrue,
			}},
		},
	}
}
