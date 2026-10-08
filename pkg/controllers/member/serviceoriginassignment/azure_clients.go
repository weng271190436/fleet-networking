/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package serviceoriginassignment

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4"
	"k8s.io/utils/ptr"
)

type azureLoadBalancerClient struct {
	client *armnetwork.LoadBalancersClient
}

// NewAzureLoadBalancerClient adapts the ARM load balancer client to the discovery interface.
func NewAzureLoadBalancerClient(client *armnetwork.LoadBalancersClient) LoadBalancerClient {
	return &azureLoadBalancerClient{client: client}
}

func (c *azureLoadBalancerClient) List(ctx context.Context, resourceGroupName string) ([]*armnetwork.LoadBalancer, error) {
	pager := c.client.NewListPager(resourceGroupName, nil)
	var loadBalancers []*armnetwork.LoadBalancer
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("get next load balancer page: %w", err)
		}
		loadBalancers = append(loadBalancers, page.Value...)
	}
	return loadBalancers, nil
}

type azurePrivateLinkServiceClient struct {
	client *armnetwork.PrivateLinkServicesClient
}

// NewAzurePrivateLinkServiceClient adapts the ARM Private Link Service client to the discovery interface.
func NewAzurePrivateLinkServiceClient(client *armnetwork.PrivateLinkServicesClient) PrivateLinkServiceClient {
	return &azurePrivateLinkServiceClient{client: client}
}

func (c *azurePrivateLinkServiceClient) List(ctx context.Context, resourceGroupName string) ([]*armnetwork.PrivateLinkService, error) {
	pager := c.client.NewListPager(resourceGroupName, nil)
	var privateLinkServices []*armnetwork.PrivateLinkService
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("get next Private Link Service page: %w", err)
		}
		privateLinkServices = append(privateLinkServices, page.Value...)
	}
	return privateLinkServices, nil
}

type azurePrivateEndpointConnectionClient struct {
	client *armnetwork.PrivateLinkServicesClient
}

// NewAzurePrivateEndpointConnectionClient adapts the ARM Private Link Service client to approval operations.
func NewAzurePrivateEndpointConnectionClient(client *armnetwork.PrivateLinkServicesClient) PrivateEndpointConnectionClient {
	return &azurePrivateEndpointConnectionClient{client: client}
}

func (c *azurePrivateEndpointConnectionClient) List(
	ctx context.Context,
	privateLinkServiceID string,
) ([]*armnetwork.PrivateEndpointConnection, error) {
	resourceGroupName, serviceName, err := privateLinkServiceCoordinates(privateLinkServiceID)
	if err != nil {
		return nil, err
	}
	pager := c.client.NewListPrivateEndpointConnectionsPager(resourceGroupName, serviceName, nil)
	var connections []*armnetwork.PrivateEndpointConnection
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list private endpoint connections for Private Link Service %q: %w", privateLinkServiceID, err)
		}
		connections = append(connections, page.Value...)
	}
	return connections, nil
}

func (c *azurePrivateEndpointConnectionClient) Get(
	ctx context.Context,
	privateLinkServiceID, connectionName string,
) (*armnetwork.PrivateEndpointConnection, error) {
	resourceGroupName, serviceName, err := privateLinkServiceCoordinates(privateLinkServiceID)
	if err != nil {
		return nil, err
	}
	response, err := c.client.GetPrivateEndpointConnection(ctx, resourceGroupName, serviceName, connectionName, nil)
	if err != nil {
		return nil, fmt.Errorf(
			"get private endpoint connection %q for Private Link Service %q: %w",
			connectionName, privateLinkServiceID, err,
		)
	}
	connection := response.PrivateEndpointConnection
	return &connection, nil
}

func (c *azurePrivateEndpointConnectionClient) Approve(
	ctx context.Context,
	privateLinkServiceID string,
	connection *armnetwork.PrivateEndpointConnection,
) error {
	resourceGroupName, serviceName, err := privateLinkServiceCoordinates(privateLinkServiceID)
	if err != nil {
		return err
	}
	connectionName := strings.TrimSpace(ptr.Deref(connection.Name, ""))
	if connectionName == "" || connection.Properties == nil ||
		connection.Properties.PrivateLinkServiceConnectionState == nil {
		return fmt.Errorf("private endpoint connection is missing its name or connection state")
	}

	parameters := approvedPrivateEndpointConnection(connection)
	if _, err := c.client.UpdatePrivateEndpointConnection(
		ctx, resourceGroupName, serviceName, connectionName, *parameters, nil,
	); err != nil {
		return fmt.Errorf(
			"approve private endpoint connection %q for Private Link Service %q: %w",
			connectionName, privateLinkServiceID, err,
		)
	}
	return nil
}

func privateLinkServiceCoordinates(privateLinkServiceID string) (string, string, error) {
	resourceID, err := arm.ParseResourceID(strings.TrimSpace(privateLinkServiceID))
	if err != nil {
		return "", "", fmt.Errorf("parse Private Link Service resource ID %q: %w", privateLinkServiceID, err)
	}
	if !strings.EqualFold(resourceID.ResourceType.String(), "Microsoft.Network/privateLinkServices") ||
		resourceID.ResourceGroupName == "" || resourceID.Name == "" {
		return "", "", fmt.Errorf("resource ID %q is not a Private Link Service resource ID", privateLinkServiceID)
	}
	return resourceID.ResourceGroupName, resourceID.Name, nil
}

func approvedPrivateEndpointConnection(connection *armnetwork.PrivateEndpointConnection) *armnetwork.PrivateEndpointConnection {
	parameters := *connection
	properties := *connection.Properties
	state := *connection.Properties.PrivateLinkServiceConnectionState
	state.Status = ptr.To("Approved")
	properties.PrivateLinkServiceConnectionState = &state
	parameters.Properties = &properties
	return &parameters
}
