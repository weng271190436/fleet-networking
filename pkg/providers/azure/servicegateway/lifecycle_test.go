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
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

type memoryAPI struct {
	resources map[string]Resource
	services  map[string]Resource
	locations []Resource
	writes    []string
	fail      string
}

func (a *memoryAPI) Get(_ context.Context, id string) (Resource, error) {
	r, ok := a.resources[id]
	if !ok {
		return Resource{}, &azcore.ResponseError{StatusCode: 404}
	}
	data, _ := json.Marshal(r)
	var result Resource
	_ = json.Unmarshal(data, &result)
	return result, nil
}
func (a *memoryAPI) List(_ context.Context, id string) ([]Resource, error) {
	var result []Resource
	if strings.HasSuffix(id, "/addressLocations") {
		return a.locations, nil
	}
	if strings.HasSuffix(id, "/services") {
		for _, s := range a.services {
			result = append(result, s)
		}
		return result, nil
	}
	for key, r := range a.resources {
		if strings.HasPrefix(key, id+"/") && !strings.Contains(strings.TrimPrefix(key, id+"/"), "/") {
			result = append(result, r)
		}
	}
	return result, nil
}
func (a *memoryAPI) Put(_ context.Context, id string, r Resource, etag string) error {
	if a.fail == id {
		return fmt.Errorf("injected Azure failure")
	}
	if _, exists := a.resources[id]; exists && etag == "*" {
		return fmt.Errorf("create race")
	}
	if r.Properties == nil {
		r.Properties = map[string]json.RawMessage{}
	}
	r.Properties["provisioningState"] = json.RawMessage(`"Succeeded"`)
	r.ID = id
	r.Name = id[strings.LastIndex(id, "/")+1:]
	r.ETag = "version"
	a.resources[id] = r
	a.writes = append(a.writes, "put "+id)
	return nil
}
func (a *memoryAPI) Delete(_ context.Context, id, etag string) error {
	if a.fail == id {
		return fmt.Errorf("injected Azure delete failure")
	}
	delete(a.resources, id)
	a.writes = append(a.writes, "delete "+id)
	return nil
}
func (a *memoryAPI) Post(_ context.Context, id string, body any) error {
	data, _ := json.Marshal(body)
	var request struct {
		ServiceRequests []struct {
			IsDelete bool     `json:"isDelete"`
			Service  Resource `json:"service"`
		} `json:"serviceRequests"`
		AddressLocations []Resource `json:"addressLocations"`
	}
	_ = json.Unmarshal(data, &request)
	if strings.HasSuffix(id, "/updateAddressLocations") {
		a.locations = request.AddressLocations
	}
	for _, entry := range request.ServiceRequests {
		if entry.IsDelete {
			delete(a.services, entry.Service.Name)
		} else {
			entry.Service.Properties["provisioningState"] = json.RawMessage(`"Succeeded"`)
			a.services[entry.Service.Name] = entry.Service
		}
	}
	a.writes = append(a.writes, "post "+id)
	return nil
}

func fixture(t *testing.T) (*Provider, *memoryAPI, Config, Journal) {
	t.Helper()
	base := "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/test/providers/Microsoft.Network"
	config := Config{Location: "eastus2euap", GatewayID: base + "/serviceGateways/fleet", VNetID: base + "/virtualNetworks/vnet",
		SubnetIDs: []string{base + "/virtualNetworks/vnet/subnets/a"}, NATGatewayID: base + "/natGateways/egress",
		EgressPublicIPID: base + "/publicIPAddresses/egress", ApproveSubnetEgressCutover: true}
	a := &memoryAPI{resources: map[string]Resource{}, services: map[string]Resource{}}
	props, _ := properties(map[string]any{"provisioningState": "Succeeded", "addressPrefix": "172.26.0.0/24",
		"networkSecurityGroup": map[string]string{"id": base + "/networkSecurityGroups/security"}})
	a.resources[config.SubnetIDs[0]] = Resource{ID: config.SubnetIDs[0], ETag: "version", Properties: props}
	p := &Provider{API: a}
	journal, err := p.Prepare(context.Background(), "owner", config)
	if err != nil {
		t.Fatal(err)
	}
	return p, a, config, journal
}

func converge(t *testing.T, p *Provider, a *memoryAPI, j Journal, apps []Application) {
	t.Helper()
	for n := 0; n < 30; n++ {
		for id, r := range a.resources {
			if strings.Contains(id, "/publicIPAddresses/") {
				r.Properties["ipAddress"] = json.RawMessage(`"48.1.2.3"`)
				a.resources[id] = r
			}
		}
		_, err := p.Reconcile(context.Background(), "owner", j, apps)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
	}
	t.Fatal("did not converge")
}

func TestNetworkCutoverAndWithdrawalOrder(t *testing.T) {
	p, a, config, j := fixture(t)
	apps := []Application{{Identity: "fleet-app", FrontendPort: 80, BackendPort: 8080, Backends: []Backend{{PodIP: "172.26.0.10", NodeIP: "172.26.0.5", Cluster: "a"}}}}
	converge(t, p, a, j, apps)
	natIndex, subnetIndex := -1, -1
	for i, w := range a.writes {
		if w == "put "+config.NATGatewayID && natIndex < 0 {
			natIndex = i
		}
		if w == "put "+config.SubnetIDs[0] {
			subnetIndex = i
		}
	}
	if natIndex < 0 || subnetIndex <= natIndex {
		t.Fatal("subnet attached before NAT")
	}
	subnet, _ := a.Get(context.Background(), config.SubnetIDs[0])
	if _, ok := subnet.Properties["networkSecurityGroup"]; !ok {
		t.Fatal("NSG lost during cutover")
	}
	// Readiness loss keeps network enrollment and resources alive.
	apps[0].Backends = nil
	converge(t, p, a, j, apps)
	if _, ok := a.resources[config.NATGatewayID]; !ok {
		t.Fatal("empty backends deleted NAT")
	}
	start := len(a.writes)
	for n := 0; n < 30; n++ {
		err := p.Withdraw(context.Background(), "owner", j)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		if n == 29 {
			t.Fatal("withdrawal did not converge")
		}
	}
	detach, natDelete := -1, -1
	for i, w := range a.writes[start:] {
		if w == "put "+config.SubnetIDs[0] {
			detach = i
		}
		if w == "delete "+config.NATGatewayID {
			natDelete = i
		}
	}
	if detach < 0 || natDelete <= detach {
		t.Fatal("NAT removed before detach")
	}
	if len(a.resources) != 1 {
		t.Fatalf("owned resources leaked: %v", a.resources)
	}
}

func TestRejectOwnershipConflictBeforeSubnetMutation(t *testing.T) {
	p, a, c, j := fixture(t)
	a.resources[c.NATGatewayID] = Resource{ID: c.NATGatewayID, Tags: map[string]string{OwnerTag: "another"}}
	if err := p.Withdraw(context.Background(), "owner", j); err == nil {
		t.Fatal("unowned NAT accepted")
	}
	if len(a.writes) != 0 {
		t.Fatal("changed subnet before validating resource ownership")
	}
}

func TestRejectInvalidBackendBeforeProvisioning(t *testing.T) {
	p, a, _, j := fixture(t)
	_, err := p.Reconcile(context.Background(), "owner", j, []Application{{Identity: "fleet-app", FrontendPort: 80, BackendPort: 8080,
		Backends: []Backend{{PodIP: "10.1.0.1", NodeIP: "172.26.0.5", Cluster: "a"}}}})
	if err == nil || len(a.writes) != 0 {
		t.Fatal("invalid backend did not stop before Azure writes")
	}
}

func TestAzureFailureDoesNotAdvanceNetworkCutover(t *testing.T) {
	p, a, c, j := fixture(t)
	a.fail = c.EgressPublicIPID
	apps := []Application{{Identity: "fleet-app", FrontendPort: 80, BackendPort: 8080}}
	_, _ = p.Reconcile(context.Background(), "owner", j, apps)
	_, err := p.Reconcile(context.Background(), "owner", j, apps)
	if err == nil || errors.Is(err, ErrPending) {
		t.Fatal("Azure failure suppressed")
	}
	subnet, _ := a.Get(context.Background(), c.SubnetIDs[0])
	link, _ := linkedID(subnet, "serviceGateway")
	if link != "" {
		t.Fatal("attached subnet after egress creation failed")
	}
}

func TestRemovingOneApplicationDeletesItsOrphanPIPOnly(t *testing.T) {
	p, a, c, j := fixture(t)
	apps := []Application{{Identity: "fleet-one", FrontendPort: 80, BackendPort: 8080},
		{Identity: "fleet-two", FrontendPort: 81, BackendPort: 8080}}
	converge(t, p, a, j, apps)
	converge(t, p, a, j, apps[1:])
	lb, pip := c.ApplicationIDs("fleet-one")
	if _, ok := a.resources[lb]; ok {
		t.Fatal("removed application LB leaked")
	}
	if _, ok := a.resources[pip]; ok {
		t.Fatal("removed application PIP leaked")
	}
	if _, ok := a.resources[c.NATGatewayID]; !ok {
		t.Fatal("remaining application lost shared NAT")
	}
}
