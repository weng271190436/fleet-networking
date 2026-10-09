/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package frontdoor

import (
	"reflect"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cdn/armcdn/v3"
)

func TestOriginSDKConversion_PreservesPrivateLink(t *testing.T) {
	resource := Resource{
		Name:                 "origin-a",
		Enabled:              true,
		HostName:             "10.0.0.1",
		HTTPPort:             8080,
		Priority:             1,
		Weight:               1000,
		PrivateLinkServiceID: "/subscriptions/sub/resourceGroups/member/providers/Microsoft.Network/privateLinkServices/pls",
		PrivateLinkLocation:  "eastus",
		RequestMessage:       "fleet:assignment-uid:request-token",
	}

	sdk := originFromResource(resource)
	sdk.Name = &resource.Name
	got := resourceFromOrigin(sdk)

	resource.ID = ""
	resource.ProvisioningState = ""
	if !reflect.DeepEqual(got, resource) {
		t.Errorf("round trip = %#v, want %#v", got, resource)
	}
}

func TestOriginGroupSDKConversion_SetsRequiredLoadBalancingDefaults(t *testing.T) {
	sdk := originGroupFromResource(Resource{HealthProbePath: "/healthz"})
	if sdk.Properties == nil || sdk.Properties.LoadBalancingSettings == nil {
		t.Fatal("origin group load balancing settings = nil, want required defaults")
	}
	settings := sdk.Properties.LoadBalancingSettings
	if settings.SampleSize == nil || *settings.SampleSize != 4 {
		t.Errorf("SampleSize = %v, want 4", settings.SampleSize)
	}
	if settings.SuccessfulSamplesRequired == nil || *settings.SuccessfulSamplesRequired != 3 {
		t.Errorf("SuccessfulSamplesRequired = %v, want 3", settings.SuccessfulSamplesRequired)
	}
	if settings.AdditionalLatencyInMilliseconds == nil || *settings.AdditionalLatencyInMilliseconds != 0 {
		t.Errorf("AdditionalLatencyInMilliseconds = %v, want 0", settings.AdditionalLatencyInMilliseconds)
	}
}

func TestSecurityPolicySDKConversion_PreservesReadOnlyWAFReference(t *testing.T) {
	resource := Resource{
		Name:        "security-global",
		WAFPolicyID: "/subscriptions/sub/resourceGroups/security/providers/Microsoft.Network/frontdoorWebApplicationFirewallPolicies/poc-waf",
		DomainIDs:   []string{"/subscriptions/sub/resourceGroups/afd/providers/Microsoft.Cdn/profiles/p/afdEndpoints/e"},
		Patterns:    []string{"/*"},
	}

	sdk := securityPolicyFromResource(resource)
	sdk.Name = &resource.Name
	got := resourceFromSecurityPolicy(sdk)

	if !reflect.DeepEqual(got, resource) {
		t.Errorf("round trip = %#v, want %#v", got, resource)
	}
	if _, ok := sdk.Properties.Parameters.(*armcdn.SecurityPolicyWebApplicationFirewallParameters); !ok {
		t.Fatalf("security policy parameters = %T, want WAF association", sdk.Properties.Parameters)
	}
}

func TestRouteSDKConversion_PreservesProtocols(t *testing.T) {
	resource := Resource{
		Name:                "route-echo",
		Enabled:             true,
		OriginGroupID:       "/subscriptions/sub/resourceGroups/afd/providers/Microsoft.Cdn/profiles/p/originGroups/og",
		Patterns:            []string{"/*"},
		Protocols:           []string{"HTTP", "HTTPS"},
		ForwardingProtocol:  "HttpOnly",
		LinkToDefaultDomain: true,
	}

	sdk := routeFromResource(resource)
	sdk.Name = &resource.Name
	got := resourceFromRoute(sdk)

	if !reflect.DeepEqual(got, resource) {
		t.Errorf("round trip = %#v, want %#v", got, resource)
	}
}
