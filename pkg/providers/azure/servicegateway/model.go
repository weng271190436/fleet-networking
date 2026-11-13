/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package servicegateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
)

const (
	ControllerName         = "networking.fleet.azure.com/service-gateway"
	ConfigAnnotation       = "networking.fleet.azure.com/service-gateway-config"
	JournalAnnotation      = "networking.fleet.azure.com/service-gateway-journal"
	RetiredRootsAnnotation = "networking.fleet.azure.com/service-gateway-retired-roots"
	OwnerTag               = "networking.fleet.azure.com.gateway-uid"
)

var identityPattern = regexp.MustCompile(`^fleet-[a-z0-9-]{1,48}$`)

// Config explicitly authorizes a subnet-wide egress cutover in one VNet.
type Config struct {
	Location                   string   `json:"location"`
	GatewayID                  string   `json:"gatewayID"`
	VNetID                     string   `json:"vnetID"`
	SubnetIDs                  []string `json:"subnetIDs"`
	NATGatewayID               string   `json:"natGatewayID"`
	EgressPublicIPID           string   `json:"egressPublicIPID"`
	ApproveSubnetEgressCutover bool     `json:"approveSubnetEgressCutover"`
}

type Backend struct {
	PodIP   string
	NodeIP  string
	Cluster string
}

type Application struct {
	Identity     string
	FrontendPort int32
	BackendPort  int32
	Backends     []Backend
}

// Journal is saved on the Gateway before any Azure write.
type Journal struct {
	Config Config `json:"config"`
	// PreviousGatewayIDs records the association to restore for every subnet.
	PreviousGatewayIDs map[string]string `json:"previousGatewayIDs"`
}

func ParseConfig(value string) (Config, error) {
	var config Config
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode Service Gateway configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return config, fmt.Errorf("configuration must contain one JSON object")
	}
	if !config.ApproveSubnetEgressCutover || config.Location == "" || len(config.SubnetIDs) == 0 {
		return config, fmt.Errorf("location, subnet IDs and explicit egress cutover approval are required")
	}
	ids := []struct{ value, kind string }{
		{config.GatewayID, "Microsoft.Network/serviceGateways"},
		{config.VNetID, "Microsoft.Network/virtualNetworks"},
		{config.NATGatewayID, "Microsoft.Network/natGateways"},
		{config.EgressPublicIPID, "Microsoft.Network/publicIPAddresses"},
	}
	var subscription string
	seen := map[string]bool{}
	for _, entry := range ids {
		id, err := arm.ParseResourceID(entry.value)
		if err != nil || !strings.EqualFold(id.ResourceType.String(), entry.kind) ||
			id.ResourceGroupName == "" || id.SubscriptionID == "" {
			return config, fmt.Errorf("invalid %s resource ID %q", entry.kind, entry.value)
		}
		if subscription != "" && !strings.EqualFold(subscription, id.SubscriptionID) {
			return config, fmt.Errorf("cross-subscription gateway configuration is unsupported")
		}
		subscription = id.SubscriptionID
		if seen[strings.ToLower(entry.value)] {
			return config, fmt.Errorf("duplicate resource ID %q", entry.value)
		}
		seen[strings.ToLower(entry.value)] = true
	}
	for _, subnet := range config.SubnetIDs {
		id, err := arm.ParseResourceID(subnet)
		if err != nil || !strings.EqualFold(id.ResourceType.String(), "Microsoft.Network/virtualNetworks/subnets") ||
			!strings.EqualFold(id.Parent.String(), config.VNetID) || seen[strings.ToLower(subnet)] {
			return config, fmt.Errorf("subnet %q must be unique and belong to the configured VNet", subnet)
		}
		seen[strings.ToLower(subnet)] = true
	}
	return config, nil
}

func Identity(owner, key string) string {
	hash := sha256.Sum256([]byte(owner + "/" + key))
	return "fleet-" + hex.EncodeToString(hash[:12])
}

func (c Config) ApplicationIDs(identity string) (lb, pip string) {
	base := c.GatewayID[:strings.LastIndex(strings.ToLower(c.GatewayID), "/servicegateways/")]
	return base + "/loadBalancers/" + identity, base + "/publicIPAddresses/" + identity
}

func validateBackends(apps []Application, prefixes []netip.Prefix) error {
	pods := map[string]Backend{}
	identities := map[string]bool{}
	for _, app := range apps {
		if !identityPattern.MatchString(app.Identity) || identities[app.Identity] ||
			app.FrontendPort < 1 || app.FrontendPort > 65535 ||
			app.BackendPort < 1 || app.BackendPort > 65535 {
			return fmt.Errorf("invalid application identity or TCP port")
		}
		identities[app.Identity] = true
		for _, backend := range app.Backends {
			for _, address := range []string{backend.PodIP, backend.NodeIP} {
				ip, err := netip.ParseAddr(address)
				if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
					return fmt.Errorf("invalid IPv4 backend address %q", address)
				}
				found := false
				for _, prefix := range prefixes {
					found = found || prefix.Contains(ip)
				}
				if !found {
					return fmt.Errorf("backend %q is outside enrolled subnets", address)
				}
			}
			if previous, ok := pods[backend.PodIP]; ok &&
				(previous.NodeIP != backend.NodeIP || previous.Cluster != backend.Cluster) {
				return fmt.Errorf("pod IP %s has conflicting cluster/location ownership", backend.PodIP)
			}
			pods[backend.PodIP] = backend
		}
	}
	return nil
}
