/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package frontdoor

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cdn/armcdn/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/frontdoor/armfrontdoor/v2"
	"k8s.io/utils/ptr"
)

// AzureClients adapts the pinned Azure SDK clients to the provider's narrow interfaces.
type AzureClients struct {
	profiles         *armcdn.ProfilesClient
	endpoints        *armcdn.AFDEndpointsClient
	originGroups     *armcdn.AFDOriginGroupsClient
	origins          *armcdn.AFDOriginsClient
	routes           *armcdn.RoutesClient
	securityPolicies *armcdn.SecurityPoliciesClient
	wafPolicies      *armfrontdoor.PoliciesClient
}

var _ Clients = (*AzureClients)(nil)

// NewAzureClients constructs adapters for the pinned AFD and WAF SDK clients.
func NewAzureClients(
	subscriptionID string,
	credential azcore.TokenCredential,
	options *arm.ClientOptions,
) (*AzureClients, error) {
	profiles, err := armcdn.NewProfilesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, fmt.Errorf("create AFD profiles client: %w", err)
	}
	endpoints, err := armcdn.NewAFDEndpointsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, fmt.Errorf("create AFD endpoints client: %w", err)
	}
	originGroups, err := armcdn.NewAFDOriginGroupsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, fmt.Errorf("create AFD origin groups client: %w", err)
	}
	origins, err := armcdn.NewAFDOriginsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, fmt.Errorf("create AFD origins client: %w", err)
	}
	routes, err := armcdn.NewRoutesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, fmt.Errorf("create AFD routes client: %w", err)
	}
	securityPolicies, err := armcdn.NewSecurityPoliciesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, fmt.Errorf("create AFD security policies client: %w", err)
	}
	wafPolicies, err := armfrontdoor.NewPoliciesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, fmt.Errorf("create read-only Front Door WAF policies client: %w", err)
	}
	return &AzureClients{
		profiles:         profiles,
		endpoints:        endpoints,
		originGroups:     originGroups,
		origins:          origins,
		routes:           routes,
		securityPolicies: securityPolicies,
		wafPolicies:      wafPolicies,
	}, nil
}

// Get retrieves an AFD resource from its exact parent hierarchy.
func (c *AzureClients) Get(ctx context.Context, kind ResourceKind, parent ResourceParent, name string) (Resource, error) {
	var resource Resource
	var err error
	switch kind {
	case ResourceProfile:
		var response armcdn.ProfilesClientGetResponse
		response, err = c.profiles.Get(ctx, parent.ResourceGroup, name, nil)
		resource = resourceFromProfile(response.Profile)
	case ResourceEndpoint:
		var response armcdn.AFDEndpointsClientGetResponse
		response, err = c.endpoints.Get(ctx, parent.ResourceGroup, parent.ProfileName, name, nil)
		resource = resourceFromEndpoint(response.AFDEndpoint)
	case ResourceOriginGroup:
		var response armcdn.AFDOriginGroupsClientGetResponse
		response, err = c.originGroups.Get(ctx, parent.ResourceGroup, parent.ProfileName, name, nil)
		resource = resourceFromOriginGroup(response.AFDOriginGroup)
	case ResourceOrigin:
		var response armcdn.AFDOriginsClientGetResponse
		response, err = c.origins.Get(ctx, parent.ResourceGroup, parent.ProfileName, parent.OriginGroupName, name, nil)
		resource = resourceFromOrigin(response.AFDOrigin)
	case ResourceRoute:
		var response armcdn.RoutesClientGetResponse
		response, err = c.routes.Get(ctx, parent.ResourceGroup, parent.ProfileName, parent.EndpointName, name, nil)
		resource = resourceFromRoute(response.Route)
	case ResourceSecurityPolicy:
		var response armcdn.SecurityPoliciesClientGetResponse
		response, err = c.securityPolicies.Get(ctx, parent.ResourceGroup, parent.ProfileName, name, nil)
		resource = resourceFromSecurityPolicy(response.SecurityPolicy)
	default:
		return Resource{}, fmt.Errorf("unsupported AFD resource kind %q", kind)
	}
	if err != nil {
		if isNotFound(err) {
			return Resource{}, ErrNotFound
		}
		return Resource{}, fmt.Errorf("Azure GET %s %q: %w", kind, name, err)
	}
	return resource, nil
}

// Upsert creates or updates an AFD resource and waits for the ARM operation.
func (c *AzureClients) Upsert(ctx context.Context, kind ResourceKind, parent ResourceParent, desired Resource) (Resource, error) {
	switch kind {
	case ResourceProfile:
		poller, err := c.profiles.BeginCreate(ctx, parent.ResourceGroup, desired.Name, profileFromResource(desired), nil)
		if err != nil {
			return Resource{}, fmt.Errorf("begin AFD profile create or update %q: %w", desired.Name, err)
		}
		response, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return Resource{}, fmt.Errorf("wait for AFD profile create or update %q: %w", desired.Name, err)
		}
		return resourceFromProfile(response.Profile), nil
	case ResourceEndpoint:
		poller, err := c.endpoints.BeginCreate(ctx, parent.ResourceGroup, parent.ProfileName, desired.Name, endpointFromResource(desired), nil)
		if err != nil {
			return Resource{}, fmt.Errorf("begin AFD endpoint create or update %q: %w", desired.Name, err)
		}
		response, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return Resource{}, fmt.Errorf("wait for AFD endpoint create or update %q: %w", desired.Name, err)
		}
		return resourceFromEndpoint(response.AFDEndpoint), nil
	case ResourceOriginGroup:
		poller, err := c.originGroups.BeginCreate(ctx, parent.ResourceGroup, parent.ProfileName, desired.Name, originGroupFromResource(desired), nil)
		if err != nil {
			return Resource{}, fmt.Errorf("begin AFD origin group create or update %q: %w", desired.Name, err)
		}
		response, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return Resource{}, fmt.Errorf("wait for AFD origin group create or update %q: %w", desired.Name, err)
		}
		return resourceFromOriginGroup(response.AFDOriginGroup), nil
	case ResourceOrigin:
		poller, err := c.origins.BeginCreate(ctx, parent.ResourceGroup, parent.ProfileName, parent.OriginGroupName, desired.Name, originFromResource(desired), nil)
		if err != nil {
			return Resource{}, fmt.Errorf("begin AFD origin create or update %q: %w", desired.Name, err)
		}
		response, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return Resource{}, fmt.Errorf("wait for AFD origin create or update %q: %w", desired.Name, err)
		}
		return resourceFromOrigin(response.AFDOrigin), nil
	case ResourceRoute:
		poller, err := c.routes.BeginCreate(ctx, parent.ResourceGroup, parent.ProfileName, parent.EndpointName, desired.Name, routeFromResource(desired), nil)
		if err != nil {
			return Resource{}, fmt.Errorf("begin AFD route create or update %q: %w", desired.Name, err)
		}
		response, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return Resource{}, fmt.Errorf("wait for AFD route create or update %q: %w", desired.Name, err)
		}
		return resourceFromRoute(response.Route), nil
	case ResourceSecurityPolicy:
		poller, err := c.securityPolicies.BeginCreate(ctx, parent.ResourceGroup, parent.ProfileName, desired.Name, securityPolicyFromResource(desired), nil)
		if err != nil {
			return Resource{}, fmt.Errorf("begin AFD security policy create or update %q: %w", desired.Name, err)
		}
		response, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return Resource{}, fmt.Errorf("wait for AFD security policy create or update %q: %w", desired.Name, err)
		}
		return resourceFromSecurityPolicy(response.SecurityPolicy), nil
	default:
		return Resource{}, fmt.Errorf("unsupported AFD resource kind %q", kind)
	}
}

// Delete removes an AFD resource from its exact parent hierarchy.
func (c *AzureClients) Delete(ctx context.Context, kind ResourceKind, parent ResourceParent, name string) error {
	var poller deletePoller
	var err error
	switch kind {
	case ResourceProfile:
		var typed *runtime.Poller[armcdn.ProfilesClientDeleteResponse]
		typed, err = c.profiles.BeginDelete(ctx, parent.ResourceGroup, name, nil)
		poller = pollerAdapter[armcdn.ProfilesClientDeleteResponse]{typed}
	case ResourceEndpoint:
		var typed *runtime.Poller[armcdn.AFDEndpointsClientDeleteResponse]
		typed, err = c.endpoints.BeginDelete(ctx, parent.ResourceGroup, parent.ProfileName, name, nil)
		poller = pollerAdapter[armcdn.AFDEndpointsClientDeleteResponse]{typed}
	case ResourceOriginGroup:
		var typed *runtime.Poller[armcdn.AFDOriginGroupsClientDeleteResponse]
		typed, err = c.originGroups.BeginDelete(ctx, parent.ResourceGroup, parent.ProfileName, name, nil)
		poller = pollerAdapter[armcdn.AFDOriginGroupsClientDeleteResponse]{typed}
	case ResourceOrigin:
		var typed *runtime.Poller[armcdn.AFDOriginsClientDeleteResponse]
		typed, err = c.origins.BeginDelete(ctx, parent.ResourceGroup, parent.ProfileName, parent.OriginGroupName, name, nil)
		poller = pollerAdapter[armcdn.AFDOriginsClientDeleteResponse]{typed}
	case ResourceRoute:
		var typed *runtime.Poller[armcdn.RoutesClientDeleteResponse]
		typed, err = c.routes.BeginDelete(ctx, parent.ResourceGroup, parent.ProfileName, parent.EndpointName, name, nil)
		poller = pollerAdapter[armcdn.RoutesClientDeleteResponse]{typed}
	case ResourceSecurityPolicy:
		var typed *runtime.Poller[armcdn.SecurityPoliciesClientDeleteResponse]
		typed, err = c.securityPolicies.BeginDelete(ctx, parent.ResourceGroup, parent.ProfileName, name, nil)
		poller = pollerAdapter[armcdn.SecurityPoliciesClientDeleteResponse]{typed}
	default:
		return fmt.Errorf("unsupported AFD resource kind %q", kind)
	}
	if err != nil {
		if isNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("begin Azure DELETE %s %q: %w", kind, name, err)
	}
	if err := poller.wait(ctx); err != nil {
		return fmt.Errorf("wait for Azure DELETE %s %q: %w", kind, name, err)
	}
	return nil
}

// GetWAFPolicy reads an externally owned Microsoft.Network Front Door WAF policy.
func (c *AzureClients) GetWAFPolicy(ctx context.Context, resourceGroup, name string) (WAFPolicy, error) {
	response, err := c.wafPolicies.Get(ctx, resourceGroup, name, nil)
	if err != nil {
		if isNotFound(err) {
			return WAFPolicy{}, ErrNotFound
		}
		return WAFPolicy{}, fmt.Errorf("Azure GET WAF policy %q: %w", name, err)
	}
	return WAFPolicy{ID: stringValue(response.ID)}, nil
}

type deletePoller interface {
	wait(context.Context) error
}

type pollerAdapter[T any] struct {
	poller *runtime.Poller[T]
}

func (p pollerAdapter[T]) wait(ctx context.Context) error {
	_, err := p.poller.PollUntilDone(ctx, nil)
	return err
}

func isNotFound(err error) bool {
	var responseError *azcore.ResponseError
	return errors.As(err, &responseError) && responseError.StatusCode == http.StatusNotFound
}

func profileFromResource(resource Resource) armcdn.Profile {
	return armcdn.Profile{
		Location: ptr.To(resource.Location),
		SKU:      &armcdn.SKU{Name: ptr.To(armcdn.SKUName(resource.SKU))},
		Tags:     stringPointerMap(resource.Tags),
	}
}

func endpointFromResource(resource Resource) armcdn.AFDEndpoint {
	return armcdn.AFDEndpoint{
		Location: ptr.To(resource.Location),
		Tags:     stringPointerMap(resource.Tags),
		Properties: &armcdn.AFDEndpointProperties{
			EnabledState: ptr.To(armcdn.EnabledStateEnabled),
		},
	}
}

func originGroupFromResource(resource Resource) armcdn.AFDOriginGroup {
	return armcdn.AFDOriginGroup{Properties: &armcdn.AFDOriginGroupProperties{
		HealthProbeSettings: &armcdn.HealthProbeParameters{
			ProbePath:        ptr.To(resource.HealthProbePath),
			ProbeProtocol:    ptr.To(armcdn.ProbeProtocolHTTP),
			ProbeRequestType: ptr.To(armcdn.HealthProbeRequestTypeGET),
		},
	}}
}

func originFromResource(resource Resource) armcdn.AFDOrigin {
	return armcdn.AFDOrigin{Properties: &armcdn.AFDOriginProperties{
		EnabledState: ptr.To(armcdn.EnabledStateEnabled),
		HTTPPort:     ptr.To(resource.HTTPPort),
		HostName:     ptr.To(resource.HostName),
		Priority:     ptr.To(resource.Priority),
		Weight:       ptr.To(resource.Weight),
		SharedPrivateLinkResource: &armcdn.SharedPrivateLinkResourceProperties{
			PrivateLink:         &armcdn.ResourceReference{ID: ptr.To(resource.PrivateLinkServiceID)},
			PrivateLinkLocation: ptr.To(resource.PrivateLinkLocation),
			RequestMessage:      ptr.To(resource.RequestMessage),
		},
	}}
}

func routeFromResource(resource Resource) armcdn.Route {
	protocols := make([]*armcdn.AFDEndpointProtocols, 0, len(resource.Protocols))
	for _, protocol := range resource.Protocols {
		var value armcdn.AFDEndpointProtocols
		switch protocol {
		case "HTTP":
			value = armcdn.AFDEndpointProtocolsHTTP
		case "HTTPS":
			value = armcdn.AFDEndpointProtocolsHTTPS
		default:
			value = armcdn.AFDEndpointProtocols(protocol)
		}
		protocols = append(protocols, &value)
	}
	return armcdn.Route{Properties: &armcdn.RouteProperties{
		EnabledState:        ptr.To(armcdn.EnabledStateEnabled),
		ForwardingProtocol:  ptr.To(armcdn.ForwardingProtocol(resource.ForwardingProtocol)),
		LinkToDefaultDomain: ptr.To(armcdn.LinkToDefaultDomainEnabled),
		OriginGroup:         &armcdn.ResourceReference{ID: ptr.To(resource.OriginGroupID)},
		PatternsToMatch:     stringPointers(resource.Patterns),
		SupportedProtocols:  protocols,
	}}
}

func securityPolicyFromResource(resource Resource) armcdn.SecurityPolicy {
	domains := make([]*armcdn.ActivatedResourceReference, 0, len(resource.DomainIDs))
	for _, id := range resource.DomainIDs {
		domains = append(domains, &armcdn.ActivatedResourceReference{ID: ptr.To(id)})
	}
	return armcdn.SecurityPolicy{Properties: &armcdn.SecurityPolicyProperties{
		Parameters: &armcdn.SecurityPolicyWebApplicationFirewallParameters{
			Associations: []*armcdn.SecurityPolicyWebApplicationFirewallAssociation{{
				Domains:         domains,
				PatternsToMatch: stringPointers(resource.Patterns),
			}},
			WafPolicy: &armcdn.ResourceReference{ID: ptr.To(resource.WAFPolicyID)},
		},
	}}
}

func resourceFromProfile(value armcdn.Profile) Resource {
	result := Resource{
		Name:     stringValue(value.Name),
		ID:       stringValue(value.ID),
		Location: stringValue(value.Location),
		Tags:     stringMap(value.Tags),
	}
	if value.SKU != nil && value.SKU.Name != nil {
		result.SKU = string(*value.SKU.Name)
	}
	if value.Properties != nil && value.Properties.ProvisioningState != nil {
		result.ProvisioningState = ProvisioningState(*value.Properties.ProvisioningState)
	}
	return result
}

func resourceFromEndpoint(value armcdn.AFDEndpoint) Resource {
	result := Resource{
		Name:     stringValue(value.Name),
		ID:       stringValue(value.ID),
		Location: stringValue(value.Location),
		Tags:     stringMap(value.Tags),
	}
	if value.Properties != nil {
		result.Enabled = value.Properties.EnabledState != nil && *value.Properties.EnabledState == armcdn.EnabledStateEnabled
		result.HostName = stringValue(value.Properties.HostName)
		if value.Properties.ProvisioningState != nil {
			result.ProvisioningState = ProvisioningState(*value.Properties.ProvisioningState)
		}
	}
	return result
}

func resourceFromOriginGroup(value armcdn.AFDOriginGroup) Resource {
	result := Resource{Name: stringValue(value.Name), ID: stringValue(value.ID)}
	if value.Properties != nil {
		if value.Properties.HealthProbeSettings != nil {
			result.HealthProbePath = stringValue(value.Properties.HealthProbeSettings.ProbePath)
		}
		if value.Properties.ProvisioningState != nil {
			result.ProvisioningState = ProvisioningState(*value.Properties.ProvisioningState)
		}
	}
	return result
}

func resourceFromOrigin(value armcdn.AFDOrigin) Resource {
	result := Resource{Name: stringValue(value.Name), ID: stringValue(value.ID)}
	if value.Properties != nil {
		properties := value.Properties
		result.Enabled = properties.EnabledState != nil && *properties.EnabledState == armcdn.EnabledStateEnabled
		result.HostName = stringValue(properties.HostName)
		result.HTTPPort = int32Value(properties.HTTPPort)
		result.Priority = int32Value(properties.Priority)
		result.Weight = int32Value(properties.Weight)
		if properties.SharedPrivateLinkResource != nil {
			result.PrivateLinkLocation = stringValue(properties.SharedPrivateLinkResource.PrivateLinkLocation)
			result.RequestMessage = stringValue(properties.SharedPrivateLinkResource.RequestMessage)
			if properties.SharedPrivateLinkResource.PrivateLink != nil {
				result.PrivateLinkServiceID = stringValue(properties.SharedPrivateLinkResource.PrivateLink.ID)
			}
		}
		if properties.ProvisioningState != nil {
			result.ProvisioningState = ProvisioningState(*properties.ProvisioningState)
		}
	}
	return result
}

func resourceFromRoute(value armcdn.Route) Resource {
	result := Resource{Name: stringValue(value.Name), ID: stringValue(value.ID)}
	if value.Properties != nil {
		properties := value.Properties
		result.Enabled = properties.EnabledState != nil && *properties.EnabledState == armcdn.EnabledStateEnabled
		result.ForwardingProtocol = enumString(properties.ForwardingProtocol)
		result.LinkToDefaultDomain = properties.LinkToDefaultDomain != nil && *properties.LinkToDefaultDomain == armcdn.LinkToDefaultDomainEnabled
		result.Patterns = stringsFromPointers(properties.PatternsToMatch)
		if properties.OriginGroup != nil {
			result.OriginGroupID = stringValue(properties.OriginGroup.ID)
		}
		for _, protocol := range properties.SupportedProtocols {
			if protocol != nil {
				result.Protocols = append(result.Protocols, upper(string(*protocol)))
			}
		}
		if properties.ProvisioningState != nil {
			result.ProvisioningState = ProvisioningState(*properties.ProvisioningState)
		}
	}
	return result
}

func resourceFromSecurityPolicy(value armcdn.SecurityPolicy) Resource {
	result := Resource{Name: stringValue(value.Name), ID: stringValue(value.ID)}
	if value.Properties != nil {
		if value.Properties.ProvisioningState != nil {
			result.ProvisioningState = ProvisioningState(*value.Properties.ProvisioningState)
		}
		if parameters, ok := value.Properties.Parameters.(*armcdn.SecurityPolicyWebApplicationFirewallParameters); ok {
			if parameters.WafPolicy != nil {
				result.WAFPolicyID = stringValue(parameters.WafPolicy.ID)
			}
			for _, association := range parameters.Associations {
				if association == nil {
					continue
				}
				result.Patterns = append(result.Patterns, stringsFromPointers(association.PatternsToMatch)...)
				for _, domain := range association.Domains {
					if domain != nil {
						result.DomainIDs = append(result.DomainIDs, stringValue(domain.ID))
					}
				}
			}
		}
	}
	return result
}

func stringPointerMap(values map[string]string) map[string]*string {
	result := make(map[string]*string, len(values))
	for key, value := range values {
		result[key] = ptr.To(value)
	}
	return result
}

func stringMap(values map[string]*string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = stringValue(value)
	}
	return result
}

func stringPointers(values []string) []*string {
	result := make([]*string, 0, len(values))
	for _, value := range values {
		result = append(result, ptr.To(value))
	}
	return result
}

func stringsFromPointers(values []*string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, *value)
		}
	}
	return result
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int32Value(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

func enumString[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func upper(value string) string {
	if value == "" {
		return ""
	}
	if value == "Http" {
		return "HTTP"
	}
	if value == "Https" {
		return "HTTPS"
	}
	return value
}
