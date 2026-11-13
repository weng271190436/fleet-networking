/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package scripts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func serviceGatewayScriptEnvironment(t *testing.T) {
	t.Helper()
	afdTestEnvironment(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ENABLE_AZURE_FRONT_DOOR", "false")
	t.Setenv("ENABLE_TRAFFIC_MANAGER", "false")
	t.Setenv("RETAIN_AFD_E2E_SETUP", "false")
	t.Setenv("ENABLE_SERVICE_GATEWAY_PROOF", "true")
	t.Setenv("AZURE_NETWORK_SETTING", "shared-vnet")
	t.Setenv("AZURE_SUBSCRIPTION_ID", "11111111-1111-1111-1111-111111111111")
	t.Setenv("AZURE_RESOURCE_GROUP", "sg-proof-test")
	t.Setenv("SERVICE_GATEWAY_CUTOVER_APPROVED", "true")
	t.Setenv("SERVICE_GATEWAY_USER_OBJECT_ID", "22222222-2222-2222-2222-222222222222")
}

func TestServiceGatewayMakeScenarioConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, variable, value string
		fail                  bool
	}{
		{name: "isolated"},
		{name: "AFD rejected", variable: "ENABLE_AZURE_FRONT_DOOR", value: "true", fail: true},
		{name: "ATM rejected", variable: "ENABLE_TRAFFIC_MANAGER", value: "true", fail: true},
		{name: "peered rejected", variable: "AZURE_NETWORK_SETTING", value: "peered-vnet", fail: true},
		{name: "missing group", variable: "AZURE_RESOURCE_GROUP", value: "", fail: true},
		{name: "unsafe local path", variable: "AZURE_RESOURCE_GROUP", value: "../elsewhere", fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serviceGatewayScriptEnvironment(t)
			if tt.variable != "" {
				t.Setenv(tt.variable, tt.value)
			}
			output, err := runAFDScript(t, "-ec", `source ./e2e-config.sh
printf '%s|%s\n' "$KUBECONFIG" "$SERVICE_GATEWAY_PROOF_CONFIG"`)
			if (err != nil) != tt.fail {
				t.Fatalf("configuration result: %v\n%s", err, output)
			}
			if !tt.fail && (!strings.Contains(output, "/sg-proof-test/kubeconfig") || !strings.Contains(output, "/sg-proof-test/network.json")) {
				t.Fatalf("setup/test do not derive isolated state: %s", output)
			}
		})
	}
}

func TestServiceGatewaySetupRequiresApprovalBeforeAzure(t *testing.T) {
	serviceGatewayScriptEnvironment(t)
	t.Setenv("SERVICE_GATEWAY_CUTOVER_APPROVED", "false")
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "azure-called")
	t.Setenv("SG_TEST_LOG", log)
	writeAFDCommand(t, filepath.Join(bin, "az"), "#!/bin/bash\necho called > \"$SG_TEST_LOG\"\nexit 91\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := runAFDScript(t, "./bootstrap.sh")
	if err == nil || !strings.Contains(output, "SERVICE_GATEWAY_CUTOVER_APPROVED") {
		t.Fatalf("missing approval was not enforced: %v\n%s", err, output)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("setup contacted Azure without approval")
	}
}

func TestServiceGatewayRunnerRequiresSuccessfulSetup(t *testing.T) {
	serviceGatewayScriptEnvironment(t)
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "go-called")
	t.Setenv("SG_TEST_LOG", log)
	writeAFDCommand(t, filepath.Join(bin, "go"), "#!/bin/bash\nprintf '%s\\n' \"$*\" > \"$SG_TEST_LOG\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := runAFDScript(t, "./run-e2e.sh")
	if err == nil || !strings.Contains(output, "make e2e-setup") {
		t.Fatalf("runner accepted missing setup: %v\n%s", err, output)
	}
	state := filepath.Join(os.Getenv("HOME"), ".local/state/fleet-networking/service-gateway",
		os.Getenv("AZURE_SUBSCRIPTION_ID"), os.Getenv("AZURE_RESOURCE_GROUP"))
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"setup-complete", "network.json", "kubeconfig"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output, err = runAFDScript(t, "./run-e2e.sh")
	if err != nil {
		t.Fatalf("runner failed: %v\n%s", err, output)
	}
	args := readAFDFile(t, log)
	if !strings.Contains(args, "-ginkgo.label-filter=service-gateway-proof") ||
		!strings.Contains(args, "-ginkgo.json-report="+filepath.Join(state, "report.json")) {
		t.Fatalf("runner omitted isolated proof or durable report: %s", args)
	}
}

func TestServiceGatewaySetupStopsOnAzureFailureWithoutCleanup(t *testing.T) {
	serviceGatewayScriptEnvironment(t)
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "commands")
	t.Setenv("SG_TEST_LOG", log)
	writeAFDCommand(t, filepath.Join(bin, "az"), `#!/bin/bash
printf '%s\n' "$*" >> "$SG_TEST_LOG"
case "$1 $2" in
  "feature show"|"provider show") echo Registered ;;
  "group exists") echo false ;;
  "group create") echo "injected group-create failure" >&2; exit 23 ;;
  *) echo "unexpected Azure command" >&2; exit 91 ;;
esac
`)
	writeAFDCommand(t, filepath.Join(bin, "curl"), "#!/bin/bash\necho 203.0.113.10\n")
	for _, name := range []string{"docker", "go", "kubectl", "kubelogin", "helm", "ssh-keygen", "timeout"} {
		writeAFDCommand(t, filepath.Join(bin, name), "#!/bin/bash\nexit 0\n")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := runAFDScript(t, "./bootstrap.sh")
	if err == nil || !strings.Contains(output, "injected group-create failure") || !strings.Contains(output, "retained") {
		t.Fatalf("setup failed unsafely: %v\n%s", err, output)
	}
	commands := readAFDFile(t, log)
	if strings.Contains(commands, "deployment group create") || strings.Contains(commands, "group delete") {
		t.Fatalf("work or deletion continued after failure: %s", commands)
	}
}

func TestServiceGatewayCleanupRefusesImplicitDeletion(t *testing.T) {
	serviceGatewayScriptEnvironment(t)
	bin := t.TempDir()
	writeAFDCommand(t, filepath.Join(bin, "az"), "#!/bin/bash\necho UNEXPECTED_AZURE_DELETE\nexit 91\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := runAFDScript(t, "./cleanup.sh")
	if err == nil || !strings.Contains(output, "automatic resource-group deletion is disabled") || strings.Contains(output, "UNEXPECTED_AZURE_DELETE") {
		t.Fatalf("unsafe cleanup: %v\n%s", err, output)
	}
}

func TestServiceGatewaySetupTemplates(t *testing.T) {
	script := readAFDFile(t, "bootstrap-service-gateway.sh")
	expressions := regexp.MustCompile(`(?s)--arg dns "\$dns" '(\{.*?\})' >`).FindStringSubmatch(script)
	if len(expressions) != 2 {
		t.Fatal("could not find actual cluster request generator")
	}
	for _, cluster := range []string{"hub", "member-1", "member-2"} {
		t.Run(cluster, func(t *testing.T) {
			subnet := ""
			if cluster != "hub" {
				subnet = "/subscriptions/test/resourceGroups/test/providers/Microsoft.Network/virtualNetworks/members/subnets/" + cluster
			}
			output, err := runAFDScript(t, "-ec", `jq -n --arg name "$1" --arg location eastus2euap \
--arg version 1.36.3 --arg identity /identity --arg subnet "$2" --arg key public-key \
--arg cidr 10.100.0.0/16 --arg dns 10.100.0.10 "$3"`, "_", cluster, subnet, expressions[1])
			if err != nil {
				t.Fatalf("template generator: %v\n%s", err, output)
			}
			var template struct {
				Resources []struct {
					Properties struct {
						DisableLocalAccounts bool `json:"disableLocalAccounts"`
						AAD                  struct {
							Managed bool `json:"managed"`
							RBAC    bool `json:"enableAzureRBAC"`
						} `json:"aadProfile"`
						Pools []struct {
							Count  int    `json:"count"`
							Subnet string `json:"vnetSubnetID"`
						} `json:"agentPoolProfiles"`
						Network struct {
							SKU      string `json:"loadBalancerSku"`
							Outbound string `json:"outboundType"`
						} `json:"networkProfile"`
					} `json:"properties"`
				} `json:"resources"`
			}
			if err := json.Unmarshal([]byte(output), &template); err != nil {
				t.Fatal(err)
			}
			p := template.Resources[0].Properties
			count := 2
			if cluster == "hub" {
				count = 1
			}
			if p.Pools[0].Count != count || p.Pools[0].Subnet != subnet || !p.DisableLocalAccounts ||
				!p.AAD.Managed || !p.AAD.RBAC || p.Network.SKU != "standard" || p.Network.Outbound != "loadBalancer" {
				t.Fatalf("unexpected cluster contract: %+v", p)
			}
		})
	}
}
