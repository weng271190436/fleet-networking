/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT license.
*/

package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
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
	t.Setenv("SERVICE_GATEWAY_RESUME_INSTALL", "false")
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
	if !strings.HasPrefix(args, "test -v ") {
		t.Fatalf("runner must stream live test progress with go test -v: %s", args)
	}
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

func TestServiceGatewayResumeInstallSkipsProvisioning(t *testing.T) {
	for _, tt := range []struct {
		name, tags     string
		failBeforeHelm bool
	}{
		{"existing images", `["sg-existing"]`, false},
		{"ambiguous images", `["sg-old","sg-new"]`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serviceGatewayScriptEnvironment(t)
			t.Setenv("SERVICE_GATEWAY_RESUME_INSTALL", "true")
			t.Setenv("SG_TEST_TAGS", tt.tags)
			state := filepath.Join(os.Getenv("HOME"), ".local/state/fleet-networking/service-gateway",
				os.Getenv("AZURE_SUBSCRIPTION_ID"), os.Getenv("AZURE_RESOURCE_GROUP"))
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "kubeconfig"), []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "commands")
			t.Setenv("SG_TEST_LOG", log)
			bin := t.TempDir()
			writeAFDCommand(t, filepath.Join(bin, "az"), `#!/bin/bash
	printf 'az %s\n' "$*" >> "$SG_TEST_LOG"
	case "$1 $2" in
	  "feature show"|"provider show") echo Registered ;;
	  "group exists") echo true ;;
	  "group show") echo '{"tags":{"purpose":"fleet-service-gateway-e2e"},"location":"eastus2euap"}' ;;
	  "aks show")
	    while [[ $# -gt 0 ]]; do if [[ "$1" == -n ]]; then cluster="$2"; break; fi; shift; done
	    printf '{"provisioningState":"Succeeded","location":"eastus2euap","fqdn":"%s.example","networkProfile":{"loadBalancerSku":"standard","outboundType":"loadBalancer"},"identityProfile":{"kubeletidentity":{"clientId":"client","objectId":"principal"}},"nodeResourceGroup":"nodes"}\n' "$cluster" ;;
	  "acr show") echo registry.azurecr.io ;;
	  "acr repository") echo "$SG_TEST_TAGS" ;;
	  *) echo "Unexpected Azure write: $*" >&2; exit 91 ;;
	esac
	`)
			writeAFDCommand(t, filepath.Join(bin, "kubectl"), `#!/bin/bash
	printf 'kubectl %s\n' "$*" >> "$SG_TEST_LOG"
	if [[ "$*" == *"config view"* ]]; then echo "https://${2%-admin}.example"; fi
	`)
			writeAFDCommand(t, filepath.Join(bin, "helm"), `#!/bin/bash
	[[ -d "$HELM_PLUGINS" && -z "$(ls -A "$HELM_PLUGINS")" ]] || exit 92
	printf 'helm %s\n' "$*" >> "$SG_TEST_LOG"
	echo "injected first Helm install failure" >&2
	exit 23
	`)
			writeAFDCommand(t, filepath.Join(bin, "curl"), "#!/bin/bash\necho 203.0.113.10\n")
			writeAFDCommand(t, filepath.Join(bin, "go"), "#!/bin/bash\necho /module-dir\n")
			writeAFDCommand(t, filepath.Join(bin, "docker"), `#!/bin/bash
	echo "docker $*" >> "$SG_TEST_LOG"
	[[ "$*" == info ]]
	`)
			for _, tool := range []string{"kubelogin", "ssh-keygen", "timeout"} {
				writeAFDCommand(t, filepath.Join(bin, tool), "#!/bin/bash\nexit 91\n")
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := runAFDScript(t, "./bootstrap.sh")
			if err == nil {
				t.Fatalf("expected stop at injected failure: %s", output)
			}
			commands := readAFDFile(t, log)
			if tt.failBeforeHelm {
				if strings.Contains(commands, "helm ") || !strings.Contains(output, "ambiguous resume") {
					t.Fatalf("ambiguous images allowed install: %s\n%s", commands, output)
				}
			} else if !strings.Contains(output, "injected first Helm install failure") ||
				!strings.Contains(commands, "--set createSystemNamespace=false") {
				t.Fatalf("resume did not reuse externally managed namespaces: %s\n%s", commands, output)
			}
			for _, forbidden := range []string{"deployment group create", "group create", "identity create", "docker build", "acr create", "group delete"} {
				if strings.Contains(commands, forbidden) {
					t.Fatalf("resume performed %s: %s", forbidden, commands)
				}
			}
			matches, _ := filepath.Glob(filepath.Join(state, "helm-plugins.*"))
			if len(matches) != 0 {
				t.Fatalf("temporary Helm plugin directory leaked: %v", matches)
			}
		})
	}
}

func TestFixtureChartsCanReuseSystemNamespace(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm required for fixture chart regression")
	}
	t.Setenv("HELM_PLUGINS", t.TempDir())
	for _, chart := range []string{"hub", "members"} {
		for _, create := range []string{"true", "false"} {
			t.Run(chart+"/"+create, func(t *testing.T) {
				args := []string{"template", "fixture", filepath.Join("..", "..", "examples", "getting-started", "charts", chart),
					"--set", "createSystemNamespace=" + create, "--set", "memberID=member-1",
					"--set", "memberClusterConfigs[0].memberID=member-1", "--set", "memberClusterConfigs[0].principalID=principal"}
				output, err := exec.Command("helm", args...).CombinedOutput()
				if err != nil {
					t.Fatalf("fixture render: %v\n%s", err, output)
				}
				count := 0
				for _, doc := range strings.Split(string(output), "\n---") {
					var object struct {
						Kind     string
						Metadata struct{ Name string }
					}
					if err := yaml.Unmarshal([]byte(doc), &object); err != nil {
						t.Fatal(err)
					}
					if object.Kind != "Namespace" {
						continue
					}
					if object.Metadata.Name == "" {
						t.Fatal("chart rendered empty Namespace name")
					}
					if object.Metadata.Name == "fleet-system" {
						count++
					}
				}
				want := 0
				if create == "true" {
					want = 1
				}
				if count != want {
					t.Fatalf("system namespaces=%d want %d", count, want)
				}
			})
		}
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
