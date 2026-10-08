/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package serviceoriginassignment

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4"
	"k8s.io/utils/ptr"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
)

var subscriptionIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// ParseRequesterSubscriptionAllowlist validates and normalizes the member-local subscription allowlist.
func ParseRequesterSubscriptionAllowlist(value string) (map[string]struct{}, error) {
	allowlist := make(map[string]struct{})
	if strings.TrimSpace(value) == "" {
		return allowlist, nil
	}
	for _, entry := range strings.Split(value, ",") {
		subscriptionID := strings.ToLower(strings.TrimSpace(entry))
		if !subscriptionIDPattern.MatchString(subscriptionID) {
			return nil, fmt.Errorf("requester subscription ID %q is not a valid UUID", entry)
		}
		allowlist[subscriptionID] = struct{}{}
	}
	return allowlist, nil
}

func approvalRequestMessage(assignment *fleetnetv1alpha1.ServiceOriginAssignment) string {
	return fmt.Sprintf("fleet:%s:%s", assignment.UID, assignment.Spec.Approval.RequestToken)
}

func findApprovalConnection(
	assignment *fleetnetv1alpha1.ServiceOriginAssignment,
	expectedPLSID, actualPLSID string,
	connections []*armnetwork.PrivateEndpointConnection,
	subscriptionAllowlist map[string]struct{},
) (*armnetwork.PrivateEndpointConnection, bool, error) {
	if !strings.EqualFold(strings.TrimSpace(expectedPLSID), strings.TrimSpace(actualPLSID)) {
		return nil, false, fmt.Errorf("discovered Private Link Service ID changed from %q to %q", expectedPLSID, actualPLSID)
	}

	expectedMessage := approvalRequestMessage(assignment)
	var matches []*armnetwork.PrivateEndpointConnection
	for _, connection := range connections {
		if connection == nil || connection.Properties == nil ||
			connection.Properties.PrivateLinkServiceConnectionState == nil {
			continue
		}
		if ptr.Deref(connection.Properties.PrivateLinkServiceConnectionState.Description, "") == expectedMessage {
			matches = append(matches, connection)
		}
	}
	if len(matches) == 0 {
		return nil, false, nil
	}
	if len(matches) != 1 {
		return nil, false, fmt.Errorf("multiple (%d) Private Link connections have the exact assignment request message", len(matches))
	}

	connection := matches[0]
	if strings.TrimSpace(ptr.Deref(connection.Name, "")) == "" {
		return nil, false, fmt.Errorf("matching Private Link connection has no name")
	}
	if err := validateRequesterSubscription(connection, subscriptionAllowlist); err != nil {
		return nil, false, err
	}
	status := strings.TrimSpace(ptr.Deref(connection.Properties.PrivateLinkServiceConnectionState.Status, ""))
	switch {
	case strings.EqualFold(status, "Pending"):
		return connection, false, nil
	case strings.EqualFold(status, "Approved"):
		return connection, true, nil
	default:
		return nil, false, fmt.Errorf("matching Private Link connection has state %q instead of Pending or Approved", status)
	}
}

func validateRequesterSubscription(
	connection *armnetwork.PrivateEndpointConnection,
	subscriptionAllowlist map[string]struct{},
) error {
	if len(subscriptionAllowlist) == 0 {
		return nil
	}
	if connection.Properties == nil || connection.Properties.PrivateEndpoint == nil {
		return fmt.Errorf("matching connection has no managed private endpoint resource ID")
	}
	privateEndpointID := strings.TrimSpace(ptr.Deref(connection.Properties.PrivateEndpoint.ID, ""))
	resourceID, err := arm.ParseResourceID(privateEndpointID)
	if err != nil || !strings.EqualFold(resourceID.ResourceType.String(), "Microsoft.Network/privateEndpoints") ||
		!subscriptionIDPattern.MatchString(strings.ToLower(resourceID.SubscriptionID)) {
		return fmt.Errorf("matching connection has malformed managed private endpoint resource ID")
	}
	subscriptionID := strings.ToLower(resourceID.SubscriptionID)
	if _, ok := subscriptionAllowlist[subscriptionID]; !ok {
		return fmt.Errorf("requester subscription %q is not allowlisted", subscriptionID)
	}
	return nil
}
