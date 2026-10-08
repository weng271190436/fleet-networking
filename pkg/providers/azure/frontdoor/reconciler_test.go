/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package frontdoor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.goms.io/fleet-networking/pkg/controllers/hub/gatewaymodel"
)

func TestReconcile_CreateUpdateNoOpAndProvisioning(t *testing.T) {
	ctx := context.Background()
	clients := newFakeClients()
	provider := NewProvider("sub", "afd-rg", clients)
	desired := testGateway()

	result, err := provider.Reconcile(ctx, desired)
	if err != nil {
		t.Fatalf("Reconcile(create) error = %v", err)
	}
	if result.Ready {
		t.Fatal("Reconcile(create).Ready = true, want false until Azure provisioning succeeds")
	}
	if got, want := clients.operations, []string{
		"waf:get", "profile:get", "profile:upsert", "endpoint:get", "endpoint:upsert",
		"originGroup:get", "originGroup:upsert", "origin:get", "origin:upsert",
		"origin:get", "origin:upsert", "route:get", "route:upsert",
		"securityPolicy:get", "securityPolicy:upsert",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("create operations = %#v, want %#v", got, want)
	}

	clients.markSucceeded()
	clients.operations = nil
	result, err = provider.Reconcile(ctx, desired)
	if err != nil {
		t.Fatalf("Reconcile(no-op) error = %v", err)
	}
	if !result.Ready || len(clients.operations) == 0 {
		t.Fatalf("Reconcile(no-op) = %#v, operations %#v; want observed ready resources", result, clients.operations)
	}
	for _, operation := range clients.operations {
		if strings.HasSuffix(operation, ":upsert") || strings.HasSuffix(operation, ":delete") {
			t.Errorf("no-op reconciliation mutated Azure with %q", operation)
		}
	}

	desired.Routes[0].Backends[0].HealthProbePath = "/changed"
	clients.operations = nil
	if _, err := provider.Reconcile(ctx, desired); err != nil {
		t.Fatalf("Reconcile(update) error = %v", err)
	}
	if !contains(clients.operations, "originGroup:upsert") {
		t.Errorf("update operations = %#v, want originGroup:upsert", clients.operations)
	}
}

func TestReconcile_RejectsForeignOwnership(t *testing.T) {
	clients := newFakeClients()
	clients.resources[resourceKey(ResourceProfile, ProfileName(testGateway()))] = Resource{
		Name: ProfileName(testGateway()),
		Tags: map[string]string{TagController: "another-controller"},
	}
	provider := NewProvider("sub", "afd-rg", clients)

	_, err := provider.Reconcile(context.Background(), testGateway())
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("Reconcile() error = %v, want ownership rejection", err)
	}
	if contains(clients.operations, "profile:upsert") {
		t.Errorf("operations = %#v, foreign profile must not be changed", clients.operations)
	}
}

func TestReconcile_DerivesChildOwnershipFromVerifiedParentAndDeterministicName(t *testing.T) {
	clients := newFakeClients()
	provider := NewProvider("sub", "afd-rg", clients)
	desired := testGateway()
	graph, err := BuildResourceGraph("sub", "afd-rg", desired)
	if err != nil {
		t.Fatalf("BuildResourceGraph() error = %v", err)
	}

	foreignChild := graph.OriginGroups[0]
	foreignChild.Name = "operator-created-origin-group"
	foreignChild.HealthProbePath = "/foreign"
	clients.resources[resourceKey(ResourceOriginGroup, foreignChild.Name)] = foreignChild

	if _, err := provider.Reconcile(context.Background(), desired); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if _, ok := clients.resources[resourceKey(ResourceOriginGroup, foreignChild.Name)]; !ok {
		t.Fatal("unrelated child under the profile was adopted or deleted")
	}
	if got := clients.resources[resourceKey(ResourceOriginGroup, graph.OriginGroups[0].Name)]; got.Name != graph.OriginGroups[0].Name {
		t.Errorf("deterministic origin group = %#v, want %q", got, graph.OriginGroups[0].Name)
	}
}

func TestReconcile_ReturnsPartialRetryableError(t *testing.T) {
	clients := newFakeClients()
	clients.failures["origin:upsert:origin-member-b-a8eac19d"] = errors.New("temporary Azure failure")
	provider := NewProvider("sub", "afd-rg", clients)

	result, err := provider.Reconcile(context.Background(), testGateway())
	if err == nil || !IsRetryable(err) || !strings.Contains(err.Error(), "member-b") {
		t.Fatalf("Reconcile() error = %v, want actionable retryable member-b error", err)
	}
	if result.Ready {
		t.Fatal("Reconcile().Ready = true after partial failure")
	}
	if !contains(clients.operations, "origin:upsert") {
		t.Errorf("operations = %#v, want successful origin work before partial failure", clients.operations)
	}
}

func TestDelete_RemovesOwnedProfileAndNeverWAF(t *testing.T) {
	clients := newFakeClients()
	provider := NewProvider("sub", "afd-rg", clients)
	desired := testGateway()
	if _, err := provider.Reconcile(context.Background(), desired); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	clients.operations = nil

	if err := provider.Delete(context.Background(), desired); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got, want := clients.operations, []string{"profile:get", "profile:delete"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Delete operations = %#v, want %#v", got, want)
	}
	for _, operation := range clients.operations {
		if strings.HasPrefix(operation, "waf:") {
			t.Fatalf("Delete attempted WAF operation %q", operation)
		}
	}
}

func TestDesiredGraph_RequiresPrivateLinkAndFullOwnershipTags(t *testing.T) {
	desired := testGateway()
	graph, err := BuildResourceGraph("sub", "afd-rg", desired)
	if err != nil {
		t.Fatalf("BuildResourceGraph() error = %v", err)
	}
	if len(graph.Origins) != 2 {
		t.Fatalf("Origins = %d, want 2", len(graph.Origins))
	}
	for _, origin := range graph.Origins {
		if origin.PrivateLinkServiceID == "" || origin.PrivateLinkLocation == "" || origin.RequestMessage == "" {
			t.Errorf("origin = %#v, want Private Link fields", origin)
		}
	}

	for _, resource := range []Resource{graph.Profile, graph.Endpoint} {
		for _, key := range []string{TagHubIdentity, TagGatewayNamespace, TagGatewayName, TagGatewayUID, TagController} {
			if resource.Tags[key] == "" {
				t.Errorf("%s tag %q is empty", resource.Name, key)
			}
		}
	}

	desired.Routes[0].Backends[0].Origins[0].PrivateLinkResourceID = ""
	if _, err := BuildResourceGraph("sub", "afd-rg", desired); err == nil {
		t.Fatal("BuildResourceGraph() error = nil, want Private Link requirement")
	}
}

func TestDeterministicNamesStayWithinAzureChildLimit(t *testing.T) {
	desired := testGateway()
	desired.Name = strings.Repeat("gateway", 36)
	graph, err := BuildResourceGraph("sub", "afd-rg", desired)
	if err != nil {
		t.Fatalf("BuildResourceGraph() error = %v", err)
	}
	for kind, name := range map[string]string{
		"profile":        graph.Profile.Name,
		"endpoint":       graph.Endpoint.Name,
		"route":          graph.Routes[0].Name,
		"securityPolicy": graph.SecurityPolicy.Name,
	} {
		if len(name) > maxAzureResourceNameLength {
			t.Errorf("%s name length = %d, want <= %d: %q", kind, len(name), maxAzureResourceNameLength, name)
		}
	}
}

func testGateway() gatewaymodel.GlobalGateway {
	return gatewaymodel.GlobalGateway{
		Namespace:   "apps",
		Name:        "global",
		UID:         "gateway-uid",
		WAFPolicyID: "/subscriptions/sub/resourceGroups/security/providers/Microsoft.Network/frontdoorWebApplicationFirewallPolicies/poc-waf",
		Listeners:   []gatewaymodel.Listener{{Name: "http", Protocol: "HTTP", Port: 80}},
		Routes: []gatewaymodel.Route{{
			Namespace: "apps",
			Name:      "echo",
			Hostnames: []string{"echo.example.com"},
			Backends: []gatewaymodel.Backend{{
				Namespace:       "apps",
				Name:            "echo",
				Port:            8080,
				RouteWeight:     1,
				HealthProbePath: "/ready",
				OriginGroupName: "og-echo-759363be",
				Origins: []gatewaymodel.Origin{
					{Cluster: "member-a", Name: "origin-member-a-ce9c9ffd", Endpoint: "10.0.0.1", Weight: 1000, Connectivity: "PrivateLink", PrivateLinkResourceID: "/subscriptions/sub/resourceGroups/a/providers/Microsoft.Network/privateLinkServices/pls-a", PrivateLinkLocation: "eastus", RequestMessage: "fleet:assignment-a:token-a"},
					{Cluster: "member-b", Name: "origin-member-b-a8eac19d", Endpoint: "10.0.0.2", Weight: 1000, Connectivity: "PrivateLink", PrivateLinkResourceID: "/subscriptions/sub/resourceGroups/b/providers/Microsoft.Network/privateLinkServices/pls-b", PrivateLinkLocation: "westus2", RequestMessage: "fleet:assignment-b:token-b"},
				},
			}},
		}},
	}
}

type fakeClients struct {
	resources  map[string]Resource
	operations []string
	failures   map[string]error
}

func newFakeClients() *fakeClients {
	return &fakeClients{
		resources: map[string]Resource{},
		failures:  map[string]error{},
	}
}

func (f *fakeClients) Get(_ context.Context, kind ResourceKind, _ ResourceParent, name string) (Resource, error) {
	f.operations = append(f.operations, string(kind)+":get")
	resource, ok := f.resources[resourceKey(kind, name)]
	if !ok {
		return Resource{}, ErrNotFound
	}
	return resource, nil
}

func (f *fakeClients) Upsert(_ context.Context, kind ResourceKind, _ ResourceParent, resource Resource) (Resource, error) {
	f.operations = append(f.operations, string(kind)+":upsert")
	if err := f.failures[string(kind)+":upsert:"+resource.Name]; err != nil {
		return Resource{}, err
	}
	f.resources[resourceKey(kind, resource.Name)] = resource
	return resource, nil
}

func (f *fakeClients) Delete(_ context.Context, kind ResourceKind, _ ResourceParent, name string) error {
	f.operations = append(f.operations, string(kind)+":delete")
	delete(f.resources, resourceKey(kind, name))
	return nil
}

func (f *fakeClients) GetWAFPolicy(_ context.Context, resourceGroup, name string) (WAFPolicy, error) {
	f.operations = append(f.operations, "waf:get")
	return WAFPolicy{ID: "/subscriptions/sub/resourceGroups/" + resourceGroup + "/providers/Microsoft.Network/frontdoorWebApplicationFirewallPolicies/" + name}, nil
}

func (f *fakeClients) markSucceeded() {
	for key, resource := range f.resources {
		resource.ProvisioningState = ProvisioningStateSucceeded
		f.resources[key] = resource
	}
}

func resourceKey(kind ResourceKind, name string) string {
	return string(kind) + "/" + name
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
