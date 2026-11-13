/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package scripts

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestServiceGatewayCIMatrixContract(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Environment string
			Env         map[string]string
			Strategy    struct {
				Matrix struct {
					Include []struct {
						Network string `json:"network-setting"`
						SG      bool   `json:"enable-service-gateway"`
						AFD     bool   `json:"enable-afd"`
						ATM     bool   `json:"enable-traffic-manager"`
					}
				}
			}
			Steps []struct {
				Name, ID, If, Run string
				Env               map[string]string
				With              map[string]string
			}
		}
	}
	path := filepath.Join("..", "..", ".github", "workflows", "e2e-tests.yml")
	if err := yaml.Unmarshal([]byte(readAFDFile(t, path)), &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["e2e-tests"]
	count := 0
	for _, row := range job.Strategy.Matrix.Include {
		if !row.SG {
			continue
		}
		count++
		if row.Network != "shared-vnet" || row.AFD || row.ATM {
			t.Fatal("SG row is not isolated")
		}
	}
	if count != 1 {
		t.Fatalf("expected one SG matrix row, got %d", count)
	}
	if job.Env["ENABLE_SERVICE_GATEWAY_PROOF"] != "${{ matrix.enable-service-gateway }}" ||
		!strings.Contains(job.Env["SERVICE_GATEWAY_PRINCIPAL_TYPE"], "ServicePrincipal") ||
		!strings.Contains(job.Env["SERVICE_GATEWAY_CUTOVER_APPROVED"], "vars.SERVICE_GATEWAY_E2E_CUTOVER_APPROVED") ||
		!strings.Contains(job.Environment, "service-gateway-e2e") {
		t.Fatal("CI lacks explicit identity/preview/cutover gate")
	}
	setup, proof, report, cleanup := -1, -1, -1, -1
	for i, step := range job.Steps {
		switch step.Name {
		case "Setup e2e Environment":
			setup = i
			if step.ID != "setup" || !strings.Contains(step.Run, "make e2e-setup") {
				t.Fatal("setup not reused")
			}
		case "Run e2e tests":
			proof = i
			if step.ID != "proof" || !strings.Contains(step.Run, "make e2e-tests") {
				t.Fatal("proof not reused")
			}
		case "Collect Service Gateway report":
			report = i
			if !strings.Contains(step.If, "always()") || !strings.Contains(step.Run, "report.json") {
				t.Fatal("failure report not retained")
			}
		case "Upload agent logs":
			if !strings.Contains(step.With["name"], "matrix.enable-service-gateway") {
				t.Fatal("SG/AFD artifacts collide")
			}
		case "Cleanup e2e":
			cleanup = i
			if !strings.Contains(step.If, "!matrix.enable-service-gateway") ||
				!strings.Contains(step.If, "steps.setup.outcome == 'success'") ||
				!strings.Contains(step.If, "steps.proof.outcome == 'success'") ||
				!strings.Contains(step.Env["SERVICE_GATEWAY_CLEANUP_APPROVED"], "matrix.enable-service-gateway") {
				t.Fatal("CI can clean up a failed SG environment")
			}
		}
	}
	if setup < 0 || proof <= setup || report <= proof || cleanup <= report {
		t.Fatal("setup/proof/report/cleanup ordering is unsafe")
	}
}

func TestServiceGatewayCIPrincipalUsesARMClaimsWithoutGraph(t *testing.T) {
	for _, tt := range []struct {
		name, kind, token, explicit string
		fail                        bool
	}{
		{name: "app token", kind: "ServicePrincipal", token: `{"oid":"33333333-3333-3333-3333-333333333333","idtyp":"app"}`},
		{name: "explicit service principal", kind: "ServicePrincipal", explicit: "33333333-3333-3333-3333-333333333333"},
		{name: "user token rejected", kind: "ServicePrincipal", token: `{"oid":"33333333-3333-3333-3333-333333333333","idtyp":"user"}`, fail: true},
		{name: "missing claim rejected", kind: "ServicePrincipal", token: `{}`, fail: true},
		{name: "invalid kind", kind: "Group", fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serviceGatewayScriptEnvironment(t)
			t.Setenv("SERVICE_GATEWAY_USER_OBJECT_ID", "")
			t.Setenv("SERVICE_GATEWAY_PRINCIPAL_TYPE", tt.kind)
			t.Setenv("SERVICE_GATEWAY_PRINCIPAL_OBJECT_ID", tt.explicit)
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(tt.token)) + ".signature"
			t.Setenv("SG_TEST_ARM_TOKEN", token)
			log := filepath.Join(t.TempDir(), "commands")
			t.Setenv("SG_TEST_LOG", log)
			bin := t.TempDir()
			writeAFDCommand(t, filepath.Join(bin, "az"), `#!/bin/bash
echo "$*" >> "$SG_TEST_LOG"
[[ "$*" == "account get-access-token"* && "$*" == *"https://management.azure.com/"* ]] || exit 91
printf '%s\n' "$SG_TEST_ARM_TOKEN"
`)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := runAFDScript(t, "-ec", "source ./e2e-config.sh; service_gateway_principal_id")
			if (err != nil) != tt.fail {
				t.Fatalf("identity result: %v %s", err, output)
			}
			if !tt.fail && strings.TrimSpace(output) != "33333333-3333-3333-3333-333333333333" {
				t.Fatalf("unexpected identity: %s", output)
			}
			if strings.Contains(output, token) {
				t.Fatal("ARM token exposed")
			}
			if tt.explicit != "" {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatal("explicit Object ID triggered a lookup")
				}
			}
		})
	}
}

func TestServiceGatewayCICleanupRequiresPassedProofAndDetachedNetwork(t *testing.T) {
	for _, tt := range []struct {
		name                                                  string
		failed, linked, resourcePresent, wrongTag, unlabelled bool
		wantDelete                                            bool
	}{
		{name: "passed and detached", wantDelete: true},
		{name: "failed proof", failed: true},
		{name: "subnet still enrolled", linked: true},
		{name: "stale report resources reappeared", resourcePresent: true},
		{name: "foreign resource group", wrongTag: true},
		{name: "unrelated passed suite", unlabelled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serviceGatewayScriptEnvironment(t)
			t.Setenv("SERVICE_GATEWAY_CLEANUP_APPROVED", "true")
			base := "/subscriptions/" + os.Getenv("AZURE_SUBSCRIPTION_ID") + "/resourceGroups/" + os.Getenv("AZURE_RESOURCE_GROUP")
			network := base + "/providers/Microsoft.Network"
			state := filepath.Join(os.Getenv("HOME"), ".local/state/fleet-networking/service-gateway", os.Getenv("AZURE_SUBSCRIPTION_ID"), os.Getenv("AZURE_RESOURCE_GROUP"))
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			config := map[string]any{"gatewayID": network + "/serviceGateways/e2e", "natGatewayID": network + "/natGateways/e2e",
				"egressPublicIPID": network + "/publicIPAddresses/egress", "vnetID": network + "/virtualNetworks/members",
				"subnetIDs": []string{network + "/virtualNetworks/members/subnets/a"}}
			labels := [][]string{{"Serial", "service-gateway-proof"}}
			if tt.unlabelled {
				labels = [][]string{{"other-proof"}}
			}
			report := []any{map[string]any{"SuiteSucceeded": !tt.failed, "SpecReports": []any{map[string]any{
				"ContainerHierarchyLabels": labels, "State": "passed"}}}}
			for name, value := range map[string]any{"proof-network.json": config, "report.json": report} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			marker := "subscription=" + os.Getenv("AZURE_SUBSCRIPTION_ID") + "\nresourceGroup=" + os.Getenv("AZURE_RESOURCE_GROUP") + "\n"
			if err := os.WriteFile(filepath.Join(state, "setup-complete"), []byte(marker), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SG_TEST_BASE", base)
			t.Setenv("SG_TEST_LINKED", map[bool]string{true: "true", false: "false"}[tt.linked])
			t.Setenv("SG_TEST_RESOURCE_PRESENT", map[bool]string{true: "true", false: "false"}[tt.resourcePresent])
			purpose := "fleet-service-gateway-e2e"
			if tt.wrongTag {
				purpose = "foreign"
			}
			t.Setenv("SG_TEST_PURPOSE", purpose)
			log := filepath.Join(t.TempDir(), "commands")
			t.Setenv("SG_TEST_LOG", log)
			bin := t.TempDir()
			writeAFDCommand(t, filepath.Join(bin, "az"), `#!/bin/bash
echo "$*" >> "$SG_TEST_LOG"
case "$1 $2" in
 "rest --subscription")
  if [[ "$SG_TEST_LINKED" == true ]]; then echo '{"properties":{"provisioningState":"Succeeded","serviceGateway":{"id":"linked"}}}'
  else echo '{"properties":{"provisioningState":"Succeeded"}}';fi ;;
 "resource list")
  if [[ "$SG_TEST_RESOURCE_PRESENT" == true ]];then printf '[{"id":"%s/providers/Microsoft.Network/natGateways/e2e"}]\n' "$SG_TEST_BASE";else echo '[]';fi ;;
 "group show") printf '{"id":"%s","tags":{"purpose":"%s"}}\n' "$SG_TEST_BASE" "$SG_TEST_PURPOSE" ;;
 "group delete") exit 0 ;;
 *) exit 91 ;;
esac
`)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := runAFDScript(t, "./cleanup.sh")
			if (err == nil) != tt.wantDelete {
				t.Fatalf("cleanup result: %v\n%s", err, output)
			}
			commands, _ := os.ReadFile(log)
			deleted := strings.Contains(string(commands), "group delete")
			if deleted != tt.wantDelete {
				t.Fatalf("unexpected delete=%v commands=%s", deleted, commands)
			}
		})
	}
}
