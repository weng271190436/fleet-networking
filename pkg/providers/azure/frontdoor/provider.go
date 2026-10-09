/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

// Package frontdoor reconciles provider-neutral Gateway state into Azure Front Door Premium.
package frontdoor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"go.goms.io/fleet-networking/pkg/controllers/hub/gatewaymodel"
)

const (
	// ControllerIdentifier is recorded on every taggable resource owned by this provider.
	ControllerIdentifier = "fleet-networking-hub-gateway"

	maxAzureResourceNameLength = 50

	// TagHubIdentity records the Fleet hub identity.
	TagHubIdentity = "fleet-networking-hub-identity"
	// TagGatewayNamespace records the owning Gateway namespace.
	TagGatewayNamespace = "fleet-networking-gateway-namespace"
	// TagGatewayName records the owning Gateway name.
	TagGatewayName = "fleet-networking-gateway-name"
	// TagGatewayUID records the owning Gateway UID.
	TagGatewayUID = "fleet-networking-gateway-uid"
	// TagController records the owning controller identifier.
	TagController = "fleet-networking-controller"

	// ProvisioningStateSucceeded indicates that Azure finished provisioning a resource.
	ProvisioningStateSucceeded ProvisioningState = "Succeeded"
)

// ErrNotFound indicates that an Azure resource does not exist.
var ErrNotFound = errors.New("Azure resource not found")

// ProvisioningState is the provider-neutral Azure provisioning state.
type ProvisioningState string

// ResourceKind identifies an AFD resource type.
type ResourceKind string

const (
	// ResourceProfile identifies an AFD profile.
	ResourceProfile ResourceKind = "profile"
	// ResourceEndpoint identifies an AFD endpoint.
	ResourceEndpoint ResourceKind = "endpoint"
	// ResourceOriginGroup identifies an AFD origin group.
	ResourceOriginGroup ResourceKind = "originGroup"
	// ResourceOrigin identifies an AFD origin.
	ResourceOrigin ResourceKind = "origin"
	// ResourceRoute identifies an AFD route.
	ResourceRoute ResourceKind = "route"
	// ResourceSecurityPolicy identifies an AFD security policy.
	ResourceSecurityPolicy ResourceKind = "securityPolicy"
)

// ResourceParent identifies the Azure hierarchy containing a resource.
type ResourceParent struct {
	ResourceGroup   string
	ProfileName     string
	EndpointName    string
	OriginGroupName string
}

// Resource is the narrow provider representation shared with Azure adapters.
type Resource struct {
	Name                 string
	ID                   string
	Tags                 map[string]string
	ProvisioningState    ProvisioningState
	Location             string
	SKU                  string
	Enabled              bool
	HealthProbePath      string
	HostName             string
	HTTPPort             int32
	Weight               int32
	Priority             int32
	PrivateLinkServiceID string
	PrivateLinkLocation  string
	RequestMessage       string
	OriginGroupID        string
	Patterns             []string
	Protocols            []string
	ForwardingProtocol   string
	LinkToDefaultDomain  bool
	WAFPolicyID          string
	DomainIDs            []string
}

// ResourceGraph is the desired AFD resource hierarchy for one Gateway.
type ResourceGraph struct {
	Profile        Resource
	Endpoint       Resource
	OriginGroups   []Resource
	Origins        []Resource
	Routes         []Resource
	SecurityPolicy Resource
}

// ResourceClient reconciles controller-owned AFD resources.
type ResourceClient interface {
	Get(context.Context, ResourceKind, ResourceParent, string) (Resource, error)
	Upsert(context.Context, ResourceKind, ResourceParent, Resource) (Resource, error)
	Delete(context.Context, ResourceKind, ResourceParent, string) error
}

// WAFPolicyClient intentionally exposes Get only because the WAF policy is externally owned.
type WAFPolicyClient interface {
	GetWAFPolicy(context.Context, string, string) (WAFPolicy, error)
}

// Clients combines owned-resource operations with read-only WAF lookup.
type Clients interface {
	ResourceClient
	WAFPolicyClient
}

// WAFPolicy identifies an externally owned WAF policy.
type WAFPolicy struct {
	ID string
}

// Provider reconciles normalized Gateway state into AFD Premium resources.
type Provider struct {
	subscriptionID string
	resourceGroup  string
	hubIdentity    string
	clients        Clients
}

// Result describes whether the desired resource graph is fully provisioned.
type Result struct {
	Ready            bool
	Pending          []string
	EndpointHostName string
}

// NewProvider constructs an AFD Premium provider.
func NewProvider(subscriptionID, resourceGroup string, clients Clients) *Provider {
	return &Provider{
		subscriptionID: subscriptionID,
		resourceGroup:  resourceGroup,
		hubIdentity:    "subscription/" + subscriptionID,
		clients:        clients,
	}
}

// Reconcile creates or updates the desired AFD resource graph and observes provisioning state.
func (p *Provider) Reconcile(ctx context.Context, desired gatewaymodel.GlobalGateway) (Result, error) {
	graph, err := BuildResourceGraphWithHubIdentity(p.subscriptionID, p.resourceGroup, p.hubIdentity, desired)
	if err != nil {
		return Result{}, err
	}
	wafGroup, wafName, err := parseWAFPolicyID(desired.WAFPolicyID)
	if err != nil {
		return Result{}, err
	}
	waf, err := p.clients.GetWAFPolicy(ctx, wafGroup, wafName)
	if err != nil {
		return Result{}, retryable(fmt.Errorf("get pre-created WAF policy %q: %w", desired.WAFPolicyID, err))
	}
	if !strings.EqualFold(waf.ID, desired.WAFPolicyID) {
		return Result{}, fmt.Errorf("pre-created WAF policy returned ID %q, want %q", waf.ID, desired.WAFPolicyID)
	}

	resources := []struct {
		kind   ResourceKind
		parent ResourceParent
		value  Resource
	}{
		{ResourceProfile, ResourceParent{ResourceGroup: p.resourceGroup}, graph.Profile},
		{ResourceEndpoint, ResourceParent{ResourceGroup: p.resourceGroup, ProfileName: graph.Profile.Name}, graph.Endpoint},
	}
	for _, resource := range graph.OriginGroups {
		resources = append(resources, struct {
			kind   ResourceKind
			parent ResourceParent
			value  Resource
		}{ResourceOriginGroup, ResourceParent{ResourceGroup: p.resourceGroup, ProfileName: graph.Profile.Name}, resource})
	}
	for _, resource := range graph.Origins {
		resources = append(resources, struct {
			kind   ResourceKind
			parent ResourceParent
			value  Resource
		}{ResourceOrigin, ResourceParent{ResourceGroup: p.resourceGroup, ProfileName: graph.Profile.Name, OriginGroupName: originGroupNameForOrigin(graph, resource.Name)}, resource})
	}
	for _, resource := range graph.Routes {
		resources = append(resources, struct {
			kind   ResourceKind
			parent ResourceParent
			value  Resource
		}{ResourceRoute, ResourceParent{ResourceGroup: p.resourceGroup, ProfileName: graph.Profile.Name, EndpointName: graph.Endpoint.Name}, resource})
	}
	resources = append(resources, struct {
		kind   ResourceKind
		parent ResourceParent
		value  Resource
	}{ResourceSecurityPolicy, ResourceParent{ResourceGroup: p.resourceGroup, ProfileName: graph.Profile.Name}, graph.SecurityPolicy})

	result := Result{Ready: true}
	for _, item := range resources {
		current, getErr := p.clients.Get(ctx, item.kind, item.parent, item.value.Name)
		if getErr != nil && !errors.Is(getErr, ErrNotFound) {
			return result, retryable(fmt.Errorf("get %s %q: %w", item.kind, item.value.Name, getErr))
		}
		if getErr == nil {
			if item.kind == ResourceEndpoint {
				result.EndpointHostName = current.HostName
			}
			if supportsTags(item.kind) && !ownedBy(current.Tags, item.value.Tags) {
				return result, fmt.Errorf("%s %q ownership tags do not match Gateway %s/%s", item.kind, item.value.Name, desired.Namespace, desired.Name)
			}
			if equalDesired(current, item.value) {
				if current.ProvisioningState != ProvisioningStateSucceeded {
					if strings.EqualFold(string(current.ProvisioningState), "Failed") {
						// Retry terminal failures after dependencies or permissions are corrected.
					} else {
						result.Ready = false
						result.Pending = append(result.Pending, fmt.Sprintf("%s/%s", item.kind, item.value.Name))
						continue
					}
				}
				if current.ProvisioningState == ProvisioningStateSucceeded {
					continue
				}
			}
		}
		updated, updateErr := p.clients.Upsert(ctx, item.kind, item.parent, item.value)
		if updateErr != nil {
			return result, retryable(fmt.Errorf("upsert %s %q: %w", item.kind, item.value.Name, updateErr))
		}
		result.Ready = false
		if item.kind == ResourceEndpoint {
			result.EndpointHostName = updated.HostName
		}
		if updated.ProvisioningState != ProvisioningStateSucceeded {
			result.Pending = append(result.Pending, fmt.Sprintf("%s/%s", item.kind, item.value.Name))
		}
	}
	sort.Strings(result.Pending)
	return result, nil
}

// Delete removes the owned profile. Azure cascades deletion to its controller-owned child graph.
// The external WAF policy cannot be deleted through the provider interface.
func (p *Provider) Delete(ctx context.Context, desired gatewaymodel.GlobalGateway) error {
	name := ProfileName(desired)
	parent := ResourceParent{ResourceGroup: p.resourceGroup}
	current, err := p.clients.Get(ctx, ResourceProfile, parent, name)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return retryable(fmt.Errorf("get profile %q before deletion: %w", name, err))
	}
	tags := ownershipTags(p.hubIdentity, desired)
	if !ownedBy(current.Tags, tags) {
		return fmt.Errorf("profile %q ownership tags do not match Gateway %s/%s", name, desired.Namespace, desired.Name)
	}
	if err := p.clients.Delete(ctx, ResourceProfile, parent, name); err != nil && !errors.Is(err, ErrNotFound) {
		return retryable(fmt.Errorf("delete profile %q: %w", name, err))
	}
	return nil
}

// WithdrawOrigins removes exact controller-derived origins after verifying the tagged parent.
// It never mutates the origin group, member infrastructure, or external WAF policy.
func (p *Provider) WithdrawOrigins(
	ctx context.Context,
	desired gatewaymodel.GlobalGateway,
	originGroupName string,
	originNames []string,
) error {
	profileName := ProfileName(desired)
	profileParent := ResourceParent{ResourceGroup: p.resourceGroup}
	current, err := p.clients.Get(ctx, ResourceProfile, profileParent, profileName)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return retryable(fmt.Errorf("get profile %q before origin withdrawal: %w", profileName, err))
	}
	if !ownedBy(current.Tags, ownershipTags(p.hubIdentity, desired)) {
		return fmt.Errorf("profile %q ownership tags do not match Gateway %s/%s", profileName, desired.Namespace, desired.Name)
	}
	parent := ResourceParent{
		ResourceGroup: p.resourceGroup, ProfileName: profileName, OriginGroupName: originGroupName,
	}
	for _, name := range originNames {
		if err := p.clients.Delete(ctx, ResourceOrigin, parent, name); err != nil && !errors.Is(err, ErrNotFound) {
			return retryable(fmt.Errorf("delete withdrawn origin %q: %w", name, err))
		}
	}
	return nil
}

// BuildResourceGraph converts normalized Gateway state into deterministic Azure resources.
func BuildResourceGraph(subscriptionID, resourceGroup string, desired gatewaymodel.GlobalGateway) (ResourceGraph, error) {
	return BuildResourceGraphWithHubIdentity(subscriptionID, resourceGroup, "subscription/"+subscriptionID, desired)
}

// BuildResourceGraphWithHubIdentity converts Gateway state using an explicit ownership identity.
func BuildResourceGraphWithHubIdentity(subscriptionID, resourceGroup, hubIdentity string, desired gatewaymodel.GlobalGateway) (ResourceGraph, error) {
	normalized, err := gatewaymodel.Normalize(desired)
	if err != nil {
		return ResourceGraph{}, fmt.Errorf("normalize Gateway model: %w", err)
	}
	if normalized.UID == "" {
		return ResourceGraph{}, fmt.Errorf("Gateway UID must not be empty")
	}
	if normalized.WAFPolicyID == "" {
		return ResourceGraph{}, fmt.Errorf("pre-created WAF policy ID must not be empty")
	}
	if _, _, err := parseWAFPolicyID(normalized.WAFPolicyID); err != nil {
		return ResourceGraph{}, err
	}

	profileName := ProfileName(normalized)
	endpointName := deterministicName("endpoint", normalized.Name, normalized.UID)
	tags := ownershipTags(hubIdentity, normalized)
	graph := ResourceGraph{
		Profile: Resource{
			Name:     profileName,
			ID:       profileID(subscriptionID, resourceGroup, profileName),
			Tags:     cloneTags(tags),
			Location: "global",
			SKU:      "Premium_AzureFrontDoor",
		},
		Endpoint: Resource{
			Name:     endpointName,
			ID:       endpointID(subscriptionID, resourceGroup, profileName, endpointName),
			Tags:     cloneTags(tags),
			Location: "global",
			Enabled:  true,
		},
	}
	for _, route := range normalized.Routes {
		if len(route.Backends) != 1 {
			return ResourceGraph{}, fmt.Errorf("route %s/%s must contain exactly one backend for the Phase 4 provider", route.Namespace, route.Name)
		}
		backend := route.Backends[0]
		if backend.OriginGroupName == "" || len(backend.Origins) == 0 {
			return ResourceGraph{}, fmt.Errorf("route %s/%s requires a non-empty private origin group", route.Namespace, route.Name)
		}
		originGroupID := originGroupID(subscriptionID, resourceGroup, profileName, backend.OriginGroupName)
		graph.OriginGroups = append(graph.OriginGroups, Resource{
			Name:            backend.OriginGroupName,
			ID:              originGroupID,
			HealthProbePath: backend.HealthProbePath,
		})
		for _, origin := range backend.Origins {
			if origin.Connectivity != "PrivateLink" || origin.PrivateLinkResourceID == "" ||
				origin.PrivateLinkLocation == "" || origin.RequestMessage == "" {
				return ResourceGraph{}, fmt.Errorf("origin %q requires Private Link service, location, and request message", origin.Cluster)
			}
			graph.Origins = append(graph.Origins, Resource{
				Name:     origin.Name,
				ID:       originID(subscriptionID, resourceGroup, profileName, backend.OriginGroupName, origin.Name),
				HostName: origin.Endpoint,
				HTTPPort: backend.Port,
				// Model validation constrains origin weights to [0, 1000].
				Weight:               int32(origin.Weight), // #nosec G115
				Priority:             1,
				Enabled:              true,
				PrivateLinkServiceID: origin.PrivateLinkResourceID,
				PrivateLinkLocation:  origin.PrivateLinkLocation,
				RequestMessage:       origin.RequestMessage,
				OriginGroupID:        originGroupID,
			})
		}
		patterns := []string{"/*"}
		routeResourceName := deterministicName("route", route.Name, route.Namespace+"/"+route.Name)
		graph.Routes = append(graph.Routes, Resource{
			Name:                routeResourceName,
			ID:                  routeID(subscriptionID, resourceGroup, profileName, endpointName, routeResourceName),
			Enabled:             true,
			OriginGroupID:       originGroupID,
			Patterns:            patterns,
			Protocols:           []string{"HTTP", "HTTPS"},
			ForwardingProtocol:  "HttpOnly",
			LinkToDefaultDomain: true,
		})
	}
	sort.Slice(graph.OriginGroups, func(i, j int) bool { return graph.OriginGroups[i].Name < graph.OriginGroups[j].Name })
	sort.Slice(graph.Origins, func(i, j int) bool { return graph.Origins[i].Name < graph.Origins[j].Name })
	sort.Slice(graph.Routes, func(i, j int) bool { return graph.Routes[i].Name < graph.Routes[j].Name })
	securityPolicyName := deterministicName("security", normalized.Name, normalized.UID)
	graph.SecurityPolicy = Resource{
		Name:        securityPolicyName,
		ID:          securityPolicyID(subscriptionID, resourceGroup, profileName, securityPolicyName),
		WAFPolicyID: normalized.WAFPolicyID,
		DomainIDs:   []string{graph.Endpoint.ID},
		Patterns:    []string{"/*"},
	}
	return graph, nil
}

// ProfileName returns the deterministic profile name for a Gateway.
func ProfileName(desired gatewaymodel.GlobalGateway) string {
	return deterministicName("afd", desired.Name, desired.Namespace+"/"+desired.Name+"/"+desired.UID)
}

func supportsTags(kind ResourceKind) bool {
	return kind == ResourceProfile || kind == ResourceEndpoint
}

func equalDesired(current, desired Resource) bool {
	current.ID = desired.ID
	current.ProvisioningState = ""
	desired.ProvisioningState = ""
	if strings.EqualFold(current.Location, desired.Location) {
		current.Location = desired.Location
	}
	if desired.PrivateLinkServiceID != "" {
		current.OriginGroupID = desired.OriginGroupID
	}
	// HostName on an endpoint is Azure-assigned output, not desired input.
	if desired.HostName == "" {
		current.HostName = ""
	}
	return reflect.DeepEqual(current, desired)
}

func ownedBy(current, desired map[string]string) bool {
	for key, value := range desired {
		if current[key] != value {
			return false
		}
	}
	return true
}

func ownershipTags(hubIdentity string, gateway gatewaymodel.GlobalGateway) map[string]string {
	return map[string]string{
		TagHubIdentity:      hubIdentity,
		TagGatewayNamespace: gateway.Namespace,
		TagGatewayName:      gateway.Name,
		TagGatewayUID:       gateway.UID,
		TagController:       ControllerIdentifier,
	}
}

func cloneTags(tags map[string]string) map[string]string {
	result := make(map[string]string, len(tags))
	for key, value := range tags {
		result[key] = value
	}
	return result
}

func originGroupNameForOrigin(graph ResourceGraph, originName string) string {
	for _, origin := range graph.Origins {
		if origin.Name == originName {
			for _, group := range graph.OriginGroups {
				if origin.OriginGroupID == group.ID {
					return group.Name
				}
			}
		}
	}
	return ""
}

func parseWAFPolicyID(id string) (string, string, error) {
	parts := strings.Split(strings.Trim(id, "/"), "/")
	if len(parts) != 8 ||
		!strings.EqualFold(parts[0], "subscriptions") ||
		!strings.EqualFold(parts[2], "resourceGroups") ||
		!strings.EqualFold(parts[4], "providers") ||
		!strings.EqualFold(parts[5], "Microsoft.Network") ||
		!strings.EqualFold(parts[6], "frontdoorWebApplicationFirewallPolicies") {
		return "", "", fmt.Errorf("WAF policy ID %q is not a Microsoft.Network/frontdoorWebApplicationFirewallPolicies resource ID", id)
	}
	return parts[3], parts[7], nil
}

type retryableError struct{ error }

func retryable(err error) error {
	return retryableError{error: err}
}

// IsRetryable reports whether an error represents a retryable Azure operation failure.
func IsRetryable(err error) bool {
	var target retryableError
	return errors.As(err, &target)
}

func deterministicName(prefix, readable, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	hash := hex.EncodeToString(sum[:4])
	readable = sanitizeNamePart(readable)
	maxReadableLength := maxAzureResourceNameLength - len(prefix) - len(hash) - 2
	if len(readable) > maxReadableLength {
		readable = strings.Trim(readable[:maxReadableLength], "-")
	}
	if readable == "" {
		readable = "resource"
	}
	return fmt.Sprintf("%s-%s-%s", prefix, readable, hash)
}

func sanitizeNamePart(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	lastDash := false
	for _, char := range strings.ToLower(value) {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		if valid {
			result.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func profileID(subscriptionID, resourceGroup, profileName string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Cdn/profiles/%s", subscriptionID, resourceGroup, profileName)
}

func endpointID(subscriptionID, resourceGroup, profileName, endpointName string) string {
	return profileID(subscriptionID, resourceGroup, profileName) + "/afdEndpoints/" + endpointName
}

func originGroupID(subscriptionID, resourceGroup, profileName, originGroupName string) string {
	return profileID(subscriptionID, resourceGroup, profileName) + "/originGroups/" + originGroupName
}

func originID(subscriptionID, resourceGroup, profileName, originGroupName, originName string) string {
	return originGroupID(subscriptionID, resourceGroup, profileName, originGroupName) + "/origins/" + originName
}

func routeID(subscriptionID, resourceGroup, profileName, endpointName, routeName string) string {
	return endpointID(subscriptionID, resourceGroup, profileName, endpointName) + "/routes/" + routeName
}

func securityPolicyID(subscriptionID, resourceGroup, profileName, name string) string {
	return profileID(subscriptionID, resourceGroup, profileName) + "/securityPolicies/" + name
}
