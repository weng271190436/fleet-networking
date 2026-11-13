/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package e2e

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	sg "go.goms.io/fleet-networking/pkg/providers/azure/servicegateway"
)

func serviceGatewayVIP(gateway *gatewayv1.Gateway) (string, error) {
	for _, name := range []string{"Accepted", "Programmed"} {
		condition := meta.FindStatusCondition(gateway.Status.Conditions, name)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != gateway.Generation {
			return "", fmt.Errorf("Gateway lacks current %s=True", name)
		}
	}
	if len(gateway.Status.Addresses) != 1 || gateway.Status.Addresses[0].Type == nil ||
		*gateway.Status.Addresses[0].Type != gatewayv1.IPAddressType {
		return "", fmt.Errorf("expected exactly one IPAddress on Gateway")
	}
	address := gateway.Status.Addresses[0].Value
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
		return "", fmt.Errorf("invalid Gateway IPv4 VIP %q", address)
	}
	return address, nil
}

func serviceGatewayMembership(locations []sg.Resource, identity string, expected []sg.Backend) error {
	want := make(map[string]string, len(expected))
	for _, backend := range expected {
		if _, found := want[backend.PodIP]; found {
			return fmt.Errorf("ambiguous expected location for %s", backend.PodIP)
		}
		want[backend.PodIP] = backend.NodeIP
	}
	got := map[string]string{}
	for _, location := range locations {
		for _, address := range location.Addresses {
			for _, service := range address.Services {
				if service != identity {
					continue
				}
				if _, found := got[address.Address]; found {
					return fmt.Errorf("duplicate mapping for %s", address.Address)
				}
				got[address.Address] = location.AddressLocation
			}
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("gateway membership count=%d, expected=%d", len(got), len(want))
	}
	for pod, node := range want {
		if got[pod] != node {
			return fmt.Errorf("pod %s has location %q, expected %q", pod, got[pod], node)
		}
	}
	return nil
}

func serviceGatewayReplies(replies []string, members []string) error {
	if len(replies) != 100 {
		return fmt.Errorf("expected 100 successful replies, got %d", len(replies))
	}
	seen := map[string]bool{}
	for _, reply := range replies {
		fields := strings.Fields(reply)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "pod=") || len(fields[1]) == len("pod=") {
			return fmt.Errorf("malformed response identity %q", reply)
		}
		known := false
		for _, member := range members {
			if fields[0] == "cluster="+member {
				seen[member], known = true, true
				break
			}
		}
		if !known {
			return fmt.Errorf("unexpected response identity %q", reply)
		}
	}
	for _, member := range members {
		if !seen[member] {
			return fmt.Errorf("no traffic reached member %s", member)
		}
	}
	return nil
}

func TestServiceGatewayProofAssertions(t *testing.T) {
	identity := "fleet-application"
	backends := []sg.Backend{{PodIP: "172.26.0.10", NodeIP: "172.26.0.5"}, {PodIP: "172.26.4.10", NodeIP: "172.26.4.5"}}
	locations := []sg.Resource{
		{AddressLocation: backends[0].NodeIP, Addresses: []sg.GatewayAddress{{Address: backends[0].PodIP, Services: []string{identity}}}},
		{AddressLocation: backends[1].NodeIP, Addresses: []sg.GatewayAddress{{Address: backends[1].PodIP, Services: []string{identity}}}},
	}
	if err := serviceGatewayMembership(locations, identity, backends); err != nil {
		t.Fatal(err)
	}
	if err := serviceGatewayMembership(locations[:1], identity, backends); err == nil {
		t.Fatal("partial membership passed")
	}
	wrong := append([]sg.Resource(nil), locations...)
	wrong[1].AddressLocation = backends[0].NodeIP
	if err := serviceGatewayMembership(wrong, identity, backends); err == nil {
		t.Fatal("wrong node location passed")
	}
	if err := serviceGatewayMembership(locations, identity, backends[:1]); err == nil {
		t.Fatal("stale withdrawn endpoint passed")
	}
	if err := serviceGatewayMembership(append(locations, locations[0]), identity, backends); err == nil {
		t.Fatal("duplicate observed mapping passed")
	}
	if err := serviceGatewayMembership(locations, identity, append(backends, backends[0])); err == nil {
		t.Fatal("duplicate expected pod address passed")
	}
	if err := serviceGatewayMembership(locations, "different-service", nil); err != nil {
		t.Fatal(err)
	}
	replies := make([]string, 100)
	for i := range replies {
		replies[i] = "cluster=a pod=web-a"
	}
	if err := serviceGatewayReplies(replies, []string{"a", "b"}); err == nil {
		t.Fatal("single-member traffic passed two-member proof")
	}
	replies[99] = "cluster=b pod=web-b"
	if err := serviceGatewayReplies(replies, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	replies[0] = "cluster=foreign pod=other"
	if err := serviceGatewayReplies(replies, []string{"a", "b"}); err == nil {
		t.Fatal("unknown backend passed")
	}
	replies[0] = "cluster=a pod="
	if err := serviceGatewayReplies(replies, []string{"a", "b"}); err == nil {
		t.Fatal("missing pod identity passed")
	}
}

func TestServiceGatewayVIPRejectsStaleReadiness(t *testing.T) {
	kind := gatewayv1.IPAddressType
	gateway := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Status: gatewayv1.GatewayStatus{Addresses: []gatewayv1.GatewayStatusAddress{{Type: &kind, Value: "48.1.2.3"}}}}
	for _, name := range []string{"Accepted", "Programmed"} {
		meta.SetStatusCondition(&gateway.Status.Conditions, metav1.Condition{
			Type: name, Status: metav1.ConditionTrue, Reason: "Accepted", ObservedGeneration: 1})
	}
	if _, err := serviceGatewayVIP(gateway); err == nil {
		t.Fatal("stale Gateway readiness accepted")
	}
	for i := range gateway.Status.Conditions {
		gateway.Status.Conditions[i].ObservedGeneration = 2
	}
	if _, err := serviceGatewayVIP(gateway); err != nil {
		t.Fatal(err)
	}
}
