/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package servicegateway

import (
	"encoding/json"
	"testing"
)

func TestConfigurationRejectsUnapprovedOrCrossVNetCutover(t *testing.T) {
	_, _, config, _ := fixture(t)
	data, _ := json.Marshal(config)
	if _, err := ParseConfig(string(data)); err != nil {
		t.Fatal(err)
	}
	config.ApproveSubnetEgressCutover = false
	data, _ = json.Marshal(config)
	if _, err := ParseConfig(string(data)); err == nil {
		t.Fatal("unapproved cutover accepted")
	}
	config.ApproveSubnetEgressCutover = true
	config.SubnetIDs = []string{config.VNetID + "-other/subnets/a"}
	data, _ = json.Marshal(config)
	if _, err := ParseConfig(string(data)); err == nil {
		t.Fatal("cross-VNet subnet accepted")
	}
}

func TestObservedAzureDefaultsDoNotCausePerpetualUpdates(t *testing.T) {
	actual := json.RawMessage(`{"rules":[{"name":"tcp","properties":{"backendPort":8080,"etag":"server","id":"server-id"}}]}`)
	desired := json.RawMessage(`{"rules":[{"name":"tcp","properties":{"backendPort":8080}}]}`)
	if !desiredSubset(actual, desired) {
		t.Fatal("server fields caused false nonconvergence")
	}
	if desiredSubset(actual, json.RawMessage(`{"rules":[{"name":"tcp","properties":{"backendPort":9000}}]}`)) {
		t.Fatal("port change ignored")
	}
}
