/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package servicegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"

	"go.goms.io/fleet-networking/pkg/common/azureerrors"
)

var ErrPending = errors.New("Azure resource operation is pending")

type Provider struct{ API API }

func missing(err error) bool {
	return azureerrors.IsNotFound(err)
}

func field[T any](resource Resource, name string) (T, error) {
	var value T
	raw := resource.Properties[name]
	if len(raw) == 0 || string(raw) == "null" {
		return value, nil
	}
	err := json.Unmarshal(raw, &value)
	return value, err
}

func linkedID(resource Resource, name string) (string, error) {
	value, err := field[struct {
		ID string `json:"id"`
	}](resource, name)
	return value.ID, err
}

func owned(resource Resource, owner string) error {
	if owner == "" || resource.Tags[OwnerTag] != owner {
		return fmt.Errorf("resource %s is not owned by Gateway UID %s", resource.ID, owner)
	}
	return nil
}

// Prepare validates external subnet ownership boundaries and records rollback
// intent. The caller must persist the returned journal before Reconcile.
func (p *Provider) Prepare(ctx context.Context, owner string, config Config) (Journal, error) {
	journal := Journal{Config: config, PreviousGatewayIDs: map[string]string{}}
	if err := validateConfig(config); err != nil {
		return journal, err
	}
	if owner == "" {
		return journal, fmt.Errorf("Gateway UID is required")
	}
	for _, id := range []string{config.GatewayID, config.NATGatewayID, config.EgressPublicIPID} {
		resource, err := p.API.Get(ctx, id)
		if err != nil && !missing(err) {
			return journal, err
		}
		if err == nil {
			if err := owned(resource, owner); err != nil {
				return journal, err
			}
		}
	}
	for _, subnetID := range config.SubnetIDs {
		subnet, err := p.API.Get(ctx, subnetID)
		if err != nil {
			return journal, err
		}
		link, err := linkedID(subnet, "serviceGateway")
		if err != nil {
			return journal, err
		}
		if link != "" {
			return journal, fmt.Errorf("subnet %s already has a gateway association; refusing adoption", subnetID)
		}
		nat, err := linkedID(subnet, "natGateway")
		if err != nil {
			return journal, err
		}
		if nat != "" {
			return journal, fmt.Errorf("subnet %s has a directly attached NAT Gateway; mixed egress enrollment is unsupported", subnetID)
		}
		journal.PreviousGatewayIDs[subnetID] = ""
	}
	return journal, nil
}

func desiredSubset(actual, desired json.RawMessage) bool {
	var a, d any
	if json.Unmarshal(actual, &a) != nil || json.Unmarshal(desired, &d) != nil {
		return false
	}
	return subset(a, d)
}

func subset(actual, desired any) bool {
	if id, ok := desired.(string); ok && strings.HasPrefix(strings.ToLower(id), "/subscriptions/") {
		current, ok := actual.(string)
		return ok && strings.EqualFold(current, id)
	}
	if wanted, ok := desired.([]any); ok {
		current, ok := actual.([]any)
		if !ok || len(current) != len(wanted) {
			return false
		}
		for i := range wanted {
			if !subset(current[i], wanted[i]) {
				return false
			}
		}
		return true
	}
	object, ok := desired.(map[string]any)
	if !ok {
		return reflect.DeepEqual(actual, desired)
	}
	current, ok := actual.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range object {
		if !subset(current[key], value) {
			return false
		}
	}
	return true
}

func (p *Provider) ensure(ctx context.Context, owner, id string, desired Resource) error {
	desired.Tags = map[string]string{OwnerTag: owner}
	current, err := p.API.Get(ctx, id)
	if missing(err) {
		if err := p.API.Put(ctx, id, desired, "*"); err != nil {
			return err
		}
		return ErrPending
	}
	if err != nil {
		return err
	}
	if err := owned(current, owner); err != nil {
		return err
	}
	state, err := field[string](current, "provisioningState")
	if err != nil {
		return err
	}
	if state == "Failed" || state == "Canceled" {
		return fmt.Errorf("resource %s provisioning failed: %s", id, state)
	}
	if state != "Succeeded" {
		return ErrPending
	}
	converged := current.Location == desired.Location &&
		(desired.SKU == nil || reflect.DeepEqual(current.SKU, desired.SKU)) &&
		(desired.Zones == nil || reflect.DeepEqual(current.Zones, desired.Zones))
	for key, value := range desired.Properties {
		converged = converged && desiredSubset(current.Properties[key], value)
	}
	if converged {
		return nil
	}
	if current.ETag == "" {
		return fmt.Errorf("resource %s has no ETag", id)
	}
	// Preserve unknown tags, but never transfer ownership.
	desired.Tags = current.Tags
	if err := p.API.Put(ctx, id, desired, current.ETag); err != nil {
		return err
	}
	return ErrPending
}

func resource(location string, sku map[string]string, zones []string, value any) (Resource, error) {
	props, err := properties(value)
	return Resource{Location: location, SKU: sku, Zones: zones, Properties: props}, err
}

func (p *Provider) Reconcile(ctx context.Context, owner string, journal Journal, apps []Application) (map[string]string, error) {
	config := journal.Config
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if owner == "" {
		return nil, fmt.Errorf("Gateway UID is required")
	}
	vips := map[string]string{}
	if len(apps) == 0 {
		return vips, p.Withdraw(ctx, owner, journal)
	}
	var prefixes []netip.Prefix
	for _, subnetID := range config.SubnetIDs {
		subnet, err := p.API.Get(ctx, subnetID)
		if err != nil {
			return nil, err
		}
		state, err := field[string](subnet, "provisioningState")
		if err != nil {
			return nil, err
		}
		if state != "Succeeded" {
			return nil, ErrPending
		}
		prefix, err := field[string](subnet, "addressPrefix")
		if err != nil {
			return nil, err
		}
		parsed, err := netip.ParsePrefix(prefix)
		if err != nil || !parsed.Addr().Is4() {
			return nil, fmt.Errorf("subnet %s requires a single IPv4 addressPrefix", subnetID)
		}
		prefixes = append(prefixes, parsed)
	}
	if err := validateBackends(apps, prefixes); err != nil {
		return nil, err
	}
	for _, app := range apps {
		_, pipID := config.ApplicationIDs(app.Identity)
		if strings.EqualFold(pipID, config.EgressPublicIPID) {
			return nil, fmt.Errorf("application PIP conflicts with default egress PIP")
		}
	}
	steps := []struct {
		id         string
		sku        map[string]string
		zones      []string
		properties any
	}{
		{config.GatewayID, map[string]string{"name": "Standard", "tier": "Regional"}, nil,
			map[string]any{"virtualNetwork": map[string]string{"id": config.VNetID},
				"routeTargetAddress": map[string]any{"privateIPAllocationMethod": "Dynamic", "subnet": map[string]string{"id": config.SubnetIDs[0]}}}},
		{config.EgressPublicIPID, map[string]string{"name": "StandardV2", "tier": "Regional"}, []string{"1", "2", "3"},
			map[string]any{"publicIPAllocationMethod": "Static", "publicIPAddressVersion": "IPv4"}},
		{config.NATGatewayID, map[string]string{"name": "StandardV2", "tier": "Regional"}, []string{"1", "2", "3"},
			map[string]any{"idleTimeoutInMinutes": 4, "publicIpAddresses": []map[string]string{{"id": config.EgressPublicIPID}},
				"serviceGateway": map[string]string{"id": config.GatewayID}}},
	}
	for _, step := range steps {
		desired, err := resource(config.Location, step.sku, step.zones, step.properties)
		if err != nil {
			return nil, err
		}
		if err := p.ensure(ctx, owner, step.id, desired); err != nil {
			return nil, err
		}
	}
	// Establish explicit outbound connectivity before attaching any subnet.
	outbound := map[string]any{"action": "PartialUpdate", "serviceRequests": []any{
		map[string]any{"service": map[string]any{"name": "fleet-default-egress", "properties": map[string]any{
			"serviceType": "Outbound", "isDefault": true, "publicNatGatewayId": config.NATGatewayID}}}}}
	services, err := p.API.List(ctx, config.GatewayID+"/services")
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{"fleet-default-egress": true}
	lbBase, _ := config.ApplicationIDs("")
	lbs, err := p.API.List(ctx, strings.TrimSuffix(lbBase, "/"))
	if err != nil {
		return nil, err
	}
	for _, lb := range lbs {
		if lb.Tags[OwnerTag] == owner {
			allowed[lb.Name] = true
		}
	}
	for _, service := range services {
		if !allowed[service.Name] {
			return nil, fmt.Errorf("gateway contains unowned service identity %s", service.Name)
		}
		if service.Name != "fleet-default-egress" {
			pools, err := field[[]struct {
				ID string `json:"id"`
			}](service, "loadBalancerBackendPools")
			if err != nil {
				return nil, err
			}
			lbID, _ := config.ApplicationIDs(service.Name)
			if len(pools) != 1 || !strings.EqualFold(pools[0].ID, lbID+"/backendAddressPools/pods") {
				return nil, fmt.Errorf("service %s references a pool outside its owned LB", service.Name)
			}
		}
	}
	found := false
	for _, service := range services {
		if service.Name != "fleet-default-egress" {
			continue
		}
		nat, err := field[string](service, "publicNatGatewayId")
		if err != nil || !strings.EqualFold(nat, config.NATGatewayID) {
			return nil, fmt.Errorf("default egress registration has unexpected NAT ownership")
		}
		isDefault, err := field[bool](service, "isDefault")
		if err != nil || !isDefault {
			return nil, fmt.Errorf("default egress registration is not default")
		}
		found = true
		state, err := field[string](service, "provisioningState")
		if err != nil {
			return nil, err
		}
		if state != "Succeeded" {
			return nil, ErrPending
		}
	}
	if !found {
		if err := p.API.Post(ctx, config.GatewayID+"/updateServices", outbound); err != nil {
			return nil, err
		}
		return nil, ErrPending
	}
	for _, subnetID := range config.SubnetIDs {
		subnet, err := p.API.Get(ctx, subnetID)
		if err != nil {
			return nil, err
		}
		link, err := linkedID(subnet, "serviceGateway")
		if err != nil {
			return nil, err
		}
		if link != "" && !strings.EqualFold(link, config.GatewayID) {
			return nil, fmt.Errorf("subnet association changed externally: %s", subnetID)
		}
		if link == "" {
			if err := p.setSubnet(ctx, subnetID, subnet, config.GatewayID); err != nil {
				return nil, err
			}
			return nil, ErrPending
		}
	}
	desiredNames := map[string]bool{}
	var requests []any
	for _, app := range apps {
		desiredNames[app.Identity] = true
		lbID, pipID := config.ApplicationIDs(app.Identity)
		pip, err := resource(config.Location, map[string]string{"name": "StandardV2", "tier": "Regional"}, []string{"1", "2", "3"},
			map[string]string{"publicIPAllocationMethod": "Static", "publicIPAddressVersion": "IPv4"})
		if err != nil {
			return nil, err
		}
		if err := p.ensure(ctx, owner, pipID, pip); err != nil {
			return nil, err
		}
		poolID := lbID + "/backendAddressPools/pods"
		lb, err := resource(config.Location, map[string]string{"name": "Service", "tier": "Regional"}, nil, map[string]any{
			"scope": "Public", "frontendIPConfigurations": []any{map[string]any{"name": "frontend", "properties": map[string]any{"publicIPAddress": map[string]string{"id": pipID}}}},
			"backendAddressPools": []any{map[string]any{"name": "pods", "properties": map[string]any{}}},
			"loadBalancingRules": []any{map[string]any{"name": "tcp", "properties": map[string]any{
				"protocol": "Tcp", "frontendPort": app.FrontendPort, "backendPort": app.BackendPort, "enableFloatingIP": false, "idleTimeoutInMinutes": 4,
				"frontendIPConfiguration": map[string]string{"id": lbID + "/frontendIPConfigurations/frontend"},
				"backendAddressPool":      map[string]string{"id": poolID}}}},
		})
		if err != nil {
			return nil, err
		}
		if err := p.ensure(ctx, owner, lbID, lb); err != nil {
			return nil, err
		}
		registered := false
		for _, service := range services {
			if service.Name != app.Identity {
				continue
			}
			kind, err := field[string](service, "serviceType")
			if err != nil {
				return nil, err
			}
			if kind != "Inbound" {
				return nil, fmt.Errorf("service %s has unexpected type %s", app.Identity, kind)
			}
			// Its sole owned pool ID was verified above. Do not refresh an
			// unchanged registration merely because a poll/watch fired.
			registered = true
		}
		if !registered {
			requests = append(requests, map[string]any{"isDelete": false, "service": map[string]any{"name": app.Identity, "properties": map[string]any{
				"serviceType": "Inbound", "loadBalancerBackendPools": []any{map[string]any{"id": poolID, "name": "pods", "properties": map[string]any{"virtualNetwork": map[string]string{"id": config.VNetID}}}}}}})
		}
		observedPIP, err := p.API.Get(ctx, pipID)
		if err != nil {
			return nil, err
		}
		ip, err := field[string](observedPIP, "ipAddress")
		if err != nil || ip == "" {
			return nil, ErrPending
		}
		vips[app.Identity] = ip
	}
	var removals []any
	for _, service := range services {
		if service.Name != "fleet-default-egress" && !desiredNames[service.Name] {
			removals = append(removals, map[string]any{"isDelete": true, "service": map[string]any{"name": service.Name, "properties": map[string]string{"serviceType": "Inbound"}}})
		}
	}
	if len(requests) != 0 {
		if err := p.API.Post(ctx, config.GatewayID+"/updateServices", map[string]any{"action": "PartialUpdate", "serviceRequests": requests}); err != nil {
			return nil, err
		}
	}
	locations := map[string]map[string][]string{}
	for _, app := range apps {
		for _, backend := range app.Backends {
			if locations[backend.NodeIP] == nil {
				locations[backend.NodeIP] = map[string][]string{}
			}
			if !slices.Contains(locations[backend.NodeIP][backend.PodIP], app.Identity) {
				locations[backend.NodeIP][backend.PodIP] = append(locations[backend.NodeIP][backend.PodIP], app.Identity)
			}
		}
	}
	for _, pods := range locations {
		for _, identities := range pods {
			slices.Sort(identities)
		}
	}
	observedLocations, err := p.API.List(ctx, config.GatewayID+"/addressLocations")
	if err != nil {
		return nil, err
	}
	addressLocations := []any{}
	for node, pods := range locations {
		var addresses []any
		for pod, identities := range pods {
			addresses = append(addresses, map[string]any{"address": pod, "services": identities})
		}
		addressLocations = append(addressLocations, map[string]any{"addressLocation": node, "addressUpdateAction": "FullUpdate", "addresses": addresses})
	}
	if !reflect.DeepEqual(locationState(observedLocations), locations) {
		if err := p.API.Post(ctx, config.GatewayID+"/updateAddressLocations", map[string]any{"action": "FullUpdate", "addressLocations": addressLocations}); err != nil {
			return nil, err
		}
	}
	if len(removals) != 0 {
		if err := p.API.Post(ctx, config.GatewayID+"/updateServices", map[string]any{"action": "PartialUpdate", "serviceRequests": removals}); err != nil {
			return nil, err
		}
	}
	for _, lb := range lbs {
		if lb.Tags[OwnerTag] == owner && !desiredNames[lb.Name] {
			if err := p.remove(ctx, owner, lb.ID); err != nil {
				return nil, err
			}
			_, pipID := config.ApplicationIDs(lb.Name)
			if err := p.remove(ctx, owner, pipID); err != nil {
				return nil, err
			}
		}
	}
	_, pipBase := config.ApplicationIDs("")
	pips, err := p.API.List(ctx, strings.TrimSuffix(pipBase, "/"))
	if err != nil {
		return nil, err
	}
	for _, pip := range pips {
		if pip.Tags[OwnerTag] != owner || strings.EqualFold(pip.ID, config.EgressPublicIPID) || desiredNames[pip.Name] {
			continue
		}
		lbID, _ := config.ApplicationIDs(pip.Name)
		if _, err := p.API.Get(ctx, lbID); err == nil {
			continue
		} else if !missing(err) {
			return nil, err
		}
		if err := p.remove(ctx, owner, pip.ID); err != nil {
			return nil, err
		}
	}
	observedServices, err := p.API.List(ctx, config.GatewayID+"/services")
	if err != nil {
		return nil, err
	}
	for _, app := range apps {
		found := false
		for _, service := range observedServices {
			if service.Name != app.Identity {
				continue
			}
			state, err := field[string](service, "provisioningState")
			if err != nil {
				return nil, err
			}
			if state == "Failed" || state == "Canceled" {
				return nil, fmt.Errorf("inbound service %s provisioning failed", app.Identity)
			}
			found = state == "Succeeded"
		}
		if !found {
			return nil, ErrPending
		}
	}
	observedLocations, err = p.API.List(ctx, config.GatewayID+"/addressLocations")
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(locationState(observedLocations), locations) {
		return nil, ErrPending
	}
	return vips, nil
}

func locationState(observedLocations []Resource) map[string]map[string][]string {
	actual := map[string]map[string][]string{}
	for _, location := range observedLocations {
		if len(location.Addresses) == 0 {
			continue
		}
		if actual[location.AddressLocation] == nil {
			actual[location.AddressLocation] = map[string][]string{}
		}
		for _, address := range location.Addresses {
			names := append([]string(nil), address.Services...)
			slices.Sort(names)
			actual[location.AddressLocation][address.Address] = names
		}
	}
	return actual
}

func (p *Provider) setSubnet(ctx context.Context, id string, subnet Resource, gateway string) error {
	if subnet.ETag == "" {
		return fmt.Errorf("subnet %s has no ETag", id)
	}
	props := subnet.Properties
	for _, key := range []string{"provisioningState", "ipConfigurations", "resourceNavigationLinks", "serviceAssociationLinks", "purpose"} {
		delete(props, key)
	}
	if gateway == "" {
		props["serviceGateway"] = json.RawMessage("null")
	} else {
		value, err := json.Marshal(map[string]string{"id": gateway})
		if err != nil {
			return err
		}
		props["serviceGateway"] = value
	}
	return p.API.Put(ctx, id, Resource{Properties: props}, subnet.ETag)
}

func (p *Provider) remove(ctx context.Context, owner, id string) error {
	current, err := p.API.Get(ctx, id)
	if missing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := owned(current, owner); err != nil {
		return err
	}
	if current.ETag == "" {
		return fmt.Errorf("resource %s has no ETag", id)
	}
	if err := p.API.Delete(ctx, id, current.ETag); err != nil && !missing(err) {
		return err
	}
	return ErrPending
}

func (p *Provider) Withdraw(ctx context.Context, owner string, journal Journal) error {
	config := journal.Config
	if err := validateConfig(config); err != nil {
		return err
	}
	if owner == "" {
		return fmt.Errorf("Gateway UID is required")
	}
	for _, id := range []string{config.GatewayID, config.NATGatewayID, config.EgressPublicIPID} {
		current, err := p.API.Get(ctx, id)
		if err != nil && !missing(err) {
			return err
		}
		if err == nil {
			if err := owned(current, owner); err != nil {
				return err
			}
		}
	}

	gateway, err := p.API.Get(ctx, config.GatewayID)
	if err != nil && !missing(err) {
		return err
	}
	if err == nil {
		if err := owned(gateway, owner); err != nil {
			return err
		}
		services, err := p.API.List(ctx, config.GatewayID+"/services")
		if err != nil {
			return err
		}
		if len(services) != 0 {
			var removals []any
			for _, service := range services {
				if service.Name != "fleet-default-egress" && !strings.HasPrefix(service.Name, "fleet-") {
					return fmt.Errorf("gateway contains unexpected service %s", service.Name)
				}
				// Keep the default egress registration until after subnets detach.
				if service.Name != "fleet-default-egress" {
					removals = append(removals, map[string]any{"isDelete": true, "service": map[string]any{"name": service.Name, "properties": map[string]string{"serviceType": "Inbound"}}})
				}
			}
			if len(removals) != 0 {
				if err := p.API.Post(ctx, config.GatewayID+"/updateAddressLocations", map[string]any{"action": "FullUpdate", "addressLocations": []any{}}); err != nil {
					return err
				}
				if err := p.API.Post(ctx, config.GatewayID+"/updateServices", map[string]any{"action": "PartialUpdate", "serviceRequests": removals}); err != nil {
					return err
				}
				return ErrPending
			}
		}
	}
	// First restore subnet associations; keep gateway egress alive during detach.
	for _, id := range config.SubnetIDs {
		previous, ok := journal.PreviousGatewayIDs[id]
		if !ok {
			return fmt.Errorf("rollback journal lacks subnet %s", id)
		}
		subnet, err := p.API.Get(ctx, id)
		if err != nil {
			return err
		}
		state, err := field[string](subnet, "provisioningState")
		if err != nil {
			return err
		}
		if state != "Succeeded" {
			return ErrPending
		}
		current, err := linkedID(subnet, "serviceGateway")
		if err != nil {
			return err
		}
		if strings.EqualFold(current, previous) {
			continue
		}
		if !strings.EqualFold(current, config.GatewayID) {
			return fmt.Errorf("subnet %s association has changed externally", id)
		}
		if err := p.setSubnet(ctx, id, subnet, previous); err != nil {
			return err
		}
		return ErrPending
	}
	lbBase, _ := config.ApplicationIDs("")
	lbs, err := p.API.List(ctx, strings.TrimSuffix(lbBase, "/"))
	if err != nil {
		return err
	}
	for _, lb := range lbs {
		if lb.Tags[OwnerTag] != owner {
			continue
		}
		if err := p.remove(ctx, owner, lb.ID); err != nil {
			return err
		}
	}
	// Deleting NAT first releases its gateway back-reference. Subnets are already detached.
	for _, id := range []string{config.NATGatewayID, config.GatewayID, config.EgressPublicIPID} {
		if err := p.remove(ctx, owner, id); err != nil {
			return err
		}
	}
	_, pipBase := config.ApplicationIDs("")
	pips, err := p.API.List(ctx, strings.TrimSuffix(pipBase, "/"))
	if err != nil {
		return err
	}
	for _, pip := range pips {
		if pip.Tags[OwnerTag] == owner {
			if err := p.remove(ctx, owner, pip.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConfig(config Config) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = ParseConfig(string(data))
	return err
}
