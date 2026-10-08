/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package serviceoriginassignment

import (
	"strings"
	"testing"

	"k8s.io/utils/ptr"
)

func TestPrivateLinkServiceCoordinates(t *testing.T) {
	tests := []struct {
		name       string
		resourceID string
		wantGroup  string
		wantName   string
		wantErr    bool
	}{
		{
			name:       "valid PLS ID",
			resourceID: "/subscriptions/sub/resourceGroups/member-rg/providers/Microsoft.Network/privateLinkServices/echo",
			wantGroup:  "member-rg",
			wantName:   "echo",
		},
		{name: "wrong resource type", resourceID: "/subscriptions/sub/resourceGroups/member-rg/providers/Microsoft.Network/loadBalancers/echo", wantErr: true},
		{name: "malformed resource ID", resourceID: "echo", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			group, name, err := privateLinkServiceCoordinates(tc.resourceID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("privateLinkServiceCoordinates() error = %v, wantErr %t", err, tc.wantErr)
			}
			if group != tc.wantGroup || name != tc.wantName {
				t.Errorf("privateLinkServiceCoordinates() = (%q, %q), want (%q, %q)", group, name, tc.wantGroup, tc.wantName)
			}
		})
	}
}

func TestApprovedPrivateEndpointConnectionPreservesUpdateFields(t *testing.T) {
	connection := testConnection("expected", "Pending", testRequestMessage, testManagedPrivateEndpointID)
	connection.ID = ptr.To(testPLSID + "/privateEndpointConnections/expected")
	connection.Properties.PrivateEndpointLocation = ptr.To("eastus")
	connection.Properties.PrivateLinkServiceConnectionState.ActionsRequired = ptr.To("None")

	parameters := approvedPrivateEndpointConnection(connection)
	if got := ptr.Deref(parameters.Properties.PrivateLinkServiceConnectionState.Status, ""); got != "Approved" {
		t.Errorf("approval status = %q, want Approved", got)
	}
	if got := ptr.Deref(parameters.Properties.PrivateLinkServiceConnectionState.Description, ""); got != testRequestMessage {
		t.Errorf("description = %q, want exact request message", got)
	}
	if got := ptr.Deref(parameters.Properties.PrivateEndpoint.ID, ""); got != testManagedPrivateEndpointID {
		t.Errorf("private endpoint ID = %q, want %q", got, testManagedPrivateEndpointID)
	}
	if got := ptr.Deref(parameters.Properties.PrivateEndpointLocation, ""); got != "eastus" {
		t.Errorf("private endpoint location = %q, want eastus", got)
	}
	if got := ptr.Deref(connection.Properties.PrivateLinkServiceConnectionState.Status, ""); !strings.EqualFold(got, "Pending") {
		t.Errorf("input connection was mutated to %q", got)
	}
}
