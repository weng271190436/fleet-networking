/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package serviceoriginassignment

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4"
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
