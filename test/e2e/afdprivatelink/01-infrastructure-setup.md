# Part 1: reusable infrastructure setup

[Previous: cleanup (for retained runs)](04-cleanup.md) ·
[Next: global Service scenario](02-global-service-scenario.md)

> **Status: Validated.** Fresh run `p7-10092240` completed this reusable platform setup and real
> networking-agent join on 2026-10-09. The environment remains active and ready for Part 2.
> Phase 7 is not complete because the fail-static lifecycle assertion remains outstanding.

This document builds only the reusable platform for the real-Azure Phase 7 human-run POC. It
finishes before any echo workload, Service, WAF policy, `MultiClusterBackend`, `Gateway`,
`HTTPRoute`, or AFD profile exists. Commands marked
**READ ONLY** do not change Azure or Kubernetes. Commands marked **MUTATING** create, update, or
delete billable resources and must not be run without explicit approval.

The fixed target is:

- subscription: `AKS Fleet Development/Test`
- subscription ID: `d712bfad-d238-486f-8f1b-bf61a831b712`
- branch: `poc/gateway-api-afd-private-link`
- required ancestor: `5093b017e6df521a08af424b497701d3658381a6`

No credentials belong in this repository, command history, manifests, or state inventory.

## 1. Prerequisites

Run from the repository root:

```bash
cd /home/weiweng/fleet-networking
```

Required tools are Azure CLI, Docker with a running daemon and buildx, Go, kubectl, Helm, jq, curl,
git, and make. The preparation was validated with the following versions; use these or compatible
newer patch releases:

| Tool | Validated version / requirement |
|---|---|
| Azure CLI | 2.87.0 |
| Docker Engine | 29.8.2 |
| Docker buildx | 0.37.1 |
| Go | module requires 1.25.12; validated with 1.26.5 |
| kubectl | 1.36.1 |
| Helm | 3.16.4 |
| jq/curl/git/make | available on `PATH` |

**READ ONLY — check tools and Docker:**

```bash
for tool in az docker go kubectl helm jq curl git make; do
  command -v "$tool" || exit 1
done
docker info >/dev/null
docker buildx version
go version
kubectl version --client
HELM_NO_PLUGINS=1 helm version
```

The signed-in principal must be able to create resource groups, AKS, ACR, managed identities,
role assignments, networking resources, AFD Premium, and WAF resources in the fixed
subscription. The setup uses `az aks get-credentials --admin`; it never writes credentials to the
repository.

## 2. Sign in and select the exact subscription

`az login` changes only local Azure CLI authentication, but may open a browser. Do not paste tokens
or passwords into this runbook.

```bash
az login
az account set --subscription d712bfad-d238-486f-8f1b-bf61a831b712
```

**READ ONLY — verify both ID and name:**

```bash
az account show --query '{name:name,id:id,tenantId:tenantId}' -o table
```

The name must be `AKS Fleet Development/Test` and the ID must be
`d712bfad-d238-486f-8f1b-bf61a831b712`.

## 3. Choose one unique run ID and location

Use 3–15 lowercase alphanumeric/hyphen characters. Keep the same values for preflight, setup,
test, debugging, and cleanup.

```bash
export AZURE_SUBSCRIPTION_ID=d712bfad-d238-486f-8f1b-bf61a831b712
export AFD_PLS_E2E_RUN_ID="p7-$(date -u +%m%d%H%M)"
export AFD_PLS_E2E_LOCATION=eastus2
printf 'run=%s location=%s\n' "$AFD_PLS_E2E_RUN_ID" "$AFD_PLS_E2E_LOCATION"
```

Do not set `AFD_PLS_E2E_APPROVED` yet.

## 4. Run and review preflight

**READ ONLY:**

```bash
make phase7-e2e-preflight
```

Preflight verifies:

- exact subscription name/ID, provider registrations, regional and DSv3 quota;
- deterministic ACR-name availability or ownership by the same tagged run;
- absence of conflicting tagged resource groups, or exact ownership recorded for a safe rerun;
- Docker daemon/buildx and all four Docker build inputs;
- both Helm charts and pinned Fleet `v0.14.0` / Gateway API `v1.2.1` CRD inputs;
- exact branch and required commit ancestry; and
- no merge conflicts or staged files.

It performs Azure queries and local validation only. It does not create a Docker builder, publish
images, render files, or call mutating Azure/Kubernetes operations.

The getting-started hub chart requires populated member values. Its default `helm lint` currently
reports the pre-existing `templates/ns.yaml: invalid Yaml document separator: apiVersion: v1`
issue; Phase 7 validates the chart with both member IDs/principal IDs populated, and that render
must pass.

### Billable/resource plan to approve

The complete four-part run creates the following. This part creates only the platform items in the
first six bullets; the application and AFD items in the final two bullets are deferred to
[Part 2](02-global-service-scenario.md):

- primary RG `fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}`;
- Basic ACR `fleetp7${AFD_PLS_E2E_RUN_ID//-/}d712`;
- four image repositories/builds: hub Gateway controller, member controller, CRD installer, echo;
- one hub-controller user-assigned identity/federated credential, three AKS control-plane
  identities, three AKS kubelet identities, seven built-in resource-scoped assignments, and three
  AKS-created AcrPull assignments;
- three one-node `Standard_D2as_v4` workload-identity-enabled AKS clusters;
- three tagged AKS node RGs, one VNet, three AKS subnets, and two PLS NAT subnets;
- two internal Standard load balancers and two Private Link Services from the echo Services;
- one AFD Premium profile graph and one Front Door WAF policy.

Because this subscription has exhausted its custom-role-definition quota, the approved live run
uses the built-in `Contributor` role for the hub identity scoped only to the disposable primary RG
and `Network Contributor` for each member identity scoped only to its exact disposable node RG.
No role is assigned at subscription scope.

## 5. Explicitly acknowledge mutation

Stop here until the preflight output and billable plan have been explicitly approved.

**MUTATING acknowledgement:**

```bash
export AFD_PLS_E2E_APPROVED=true
```

Every mutating entry point rejects any value other than the exact lowercase string `true`.

## 6. Build the reusable platform

These copy/paste commands are the authoritative platform workflow. Run them in order in one Bash
shell. On a retry, reinitialize the shell, repeat the **READ ONLY** part of the failed subsection,
and run only its necessary conditional **MUTATING** commands. Existing stage scripts still combine
platform and application operations and therefore do **not** match this product boundary.

Initialize deterministic names, paths, state, tags, and a context-aware kubectl helper. These
operations are local only:

```bash
source test/e2e/afdprivatelink/scripts/common.sh
# Keep the operator's interactive shell alive when an individual command fails.
set +e
set +u
set +o pipefail
initialize_names
validate_subscription
initialize_state
tags=("source=${SOURCE_TAG}" "phase=7" "run-id=${AFD_PLS_E2E_RUN_ID}")
k() {
  local context="$1"
  shift
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" "$@"
}
test "${AZURE_SUBSCRIPTION_ID:-}" = "$EXPECTED_SUBSCRIPTION_ID"
test "${AFD_PLS_E2E_APPROVED:-}" = true
```

### 6.1 Stage 01 — tagged resource group, registry, and immutable images

**READ ONLY — inspect before creating anything:**

```bash
az group show --name "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --query '{name:name,location:location,tags:tags}' -o json 2>/dev/null || true
az acr show --name "$AFD_PLS_E2E_ACR" \
  --query '{name:name,resourceGroup:resourceGroup,sku:sku.name,loginServer:loginServer,tags:tags}' \
  -o json 2>/dev/null || true
docker info >/dev/null
docker buildx version
```

Expected: absent resources print nothing. Retained resources must be in the deterministic RG and
have `source=fleet-networking-afd-pls-e2e`, `phase=7`, and the current `run-id`; a retained ACR
must use `Basic`.

**MUTATING — conditionally create the RG/ACR, log in, and build/push all four images:**

```bash
test "$AFD_PLS_E2E_APPROVED" = true
if az group show --name "$AFD_PLS_E2E_RESOURCE_GROUP" --output none 2>/dev/null; then
  validate_resource_group_boundary
else
  az group create --name "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --location "$AFD_PLS_E2E_LOCATION" --tags "${tags[@]}" --output none
fi
resource_group_id="/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_RESOURCE_GROUP}"
record_resource resourceGroup "$AFD_PLS_E2E_RESOURCE_GROUP" "$resource_group_id"

if acr_json="$(az acr show --name "$AFD_PLS_E2E_ACR" -o json 2>/dev/null)"; then
  validate_resource_tags "$(jq -r '.tags.source // ""' <<<"$acr_json")" \
    "$(jq -r '.tags["run-id"] // ""' <<<"$acr_json")" "ACR $AFD_PLS_E2E_ACR"
  test "$(jq -r '.resourceGroup' <<<"$acr_json")" = "$AFD_PLS_E2E_RESOURCE_GROUP"
  test "$(jq -r '.sku.name' <<<"$acr_json")" = Basic
else
  az acr create --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --name "$AFD_PLS_E2E_ACR" --location "$AFD_PLS_E2E_LOCATION" --sku Basic \
    --admin-enabled false --tags "${tags[@]}" --output none
fi
acr_id="$(az acr show --name "$AFD_PLS_E2E_ACR" --query id -o tsv)"
acr_login_server="$(az acr show --name "$AFD_PLS_E2E_ACR" --query loginServer -o tsv)"
record_resource containerRegistry "$AFD_PLS_E2E_ACR" "$acr_id"
az acr login --name "$AFD_PLS_E2E_ACR" --output none

docker buildx build --file docker/hub-gateway-controller-manager.Dockerfile \
  --output=type=registry --platform=linux/amd64 --pull \
  --tag "${acr_login_server}/hub-gateway-controller-manager:${AFD_PLS_E2E_RUN_ID}" \
  --progress=plain --build-arg GOARCH=amd64 --build-arg GOOS=linux .
docker buildx build --file docker/member-net-controller-manager.Dockerfile \
  --output=type=registry --platform=linux/amd64 --pull \
  --tag "${acr_login_server}/member-net-controller-manager:${AFD_PLS_E2E_RUN_ID}" \
  --progress=plain --build-arg GOARCH=amd64 --build-arg GOOS=linux .
docker buildx build --file docker/net-crd-installer.Dockerfile \
  --output=type=registry --platform=linux/amd64 --pull \
  --tag "${acr_login_server}/net-crd-installer:${AFD_PLS_E2E_RUN_ID}" \
  --progress=plain --build-arg GOARCH=amd64 --build-arg GOOS=linux .
docker buildx build --file test/e2e/afdprivatelink/echo/Dockerfile \
  --output=type=registry --platform=linux/amd64 --pull \
  --tag "${acr_login_server}/afd-pls-echo:${AFD_PLS_E2E_RUN_ID}" --progress=plain .
```

Resolve all five immutable references, including the external refresh-token image, and update
state:

```bash
resolve_image() {
  local repository="$1" digest
  digest="$(az acr repository show --name "$AFD_PLS_E2E_ACR" \
    --image "${repository}:${AFD_PLS_E2E_RUN_ID}" --query digest -o tsv)"
  [[ "$digest" =~ ^sha256:[[:xdigit:]]{64}$ ]]
  printf '%s/%s@%s' "$acr_login_server" "$repository" "$digest"
}
hub_image="$(resolve_image hub-gateway-controller-manager)"
member_image="$(resolve_image member-net-controller-manager)"
crd_image="$(resolve_image net-crd-installer)"
echo_image="$(resolve_image afd-pls-echo)"
refresh_repo=ghcr.io/azure/fleet/refresh-token
refresh_digest="$(docker buildx imagetools inspect "${refresh_repo}:v0.1.0" \
  --format '{{json .Manifest.Digest}}' | tr -d '"')"
[[ "$refresh_digest" =~ ^sha256:[[:xdigit:]]{64}$ ]]
jq --arg hub "$hub_image" --arg member "$member_image" --arg crd "$crd_image" \
  --arg echo "$echo_image" --arg refresh "${refresh_repo}@${refresh_digest}" \
  '.images = {hubGateway:$hub,member:$member,crdInstaller:$crd,echo:$echo,refreshToken:$refresh}' \
  "$AFD_PLS_E2E_STATE_FILE" >"${AFD_PLS_E2E_STATE_FILE}.next"
mv "${AFD_PLS_E2E_STATE_FILE}.next" "$AFD_PLS_E2E_STATE_FILE"
initialize_names
```

**READ ONLY — verify before Stage 02:**

```bash
jq -e '.images | length == 5 and all(.[]; test("@sha256:[0-9a-fA-F]{64}$"))' \
  "$AFD_PLS_E2E_STATE_FILE"
az acr repository list --name "$AFD_PLS_E2E_ACR" -o table
```

Expected: jq prints `true`; the four run-built repositories are listed.

### 6.2 Stage 02 — safe VNet and subnet reuse

**READ ONLY — inspect the VNet and every required subnet:**

```bash
subnet_specs=(
  "hub:10.70.0.0/22" "member-1:10.70.4.0/22" "member-2:10.70.8.0/22"
  "pls-1:10.70.12.0/24" "pls-2:10.70.13.0/24"
)
vnet_exists=false
if vnet_json="$(az network vnet show --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --name "$AFD_PLS_E2E_VNET" -o json 2>/dev/null)"; then
  vnet_exists=true
  test "$(jq -r '.addressSpace.addressPrefixes | sort | join(",")' <<<"$vnet_json")" \
    = "10.70.0.0/16"
  vnet_source="$(jq -r '.tags.source // ""' <<<"$vnet_json")"
  vnet_run="$(jq -r '.tags["run-id"] // ""' <<<"$vnet_json")"
  if [[ -z "$vnet_source" && -z "$vnet_run" ]]; then
    expected_vnet_id="/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_RESOURCE_GROUP}/providers/Microsoft.Network/virtualNetworks/${AFD_PLS_E2E_VNET}"
    jq -e --arg id "$expected_vnet_id" \
      'any(.resources[]?; .type=="virtualNetwork" and
        (.id|ascii_downcase)==($id|ascii_downcase))' "$AFD_PLS_E2E_STATE_FILE"
    vnet_needs_tags=true
  else
    validate_resource_tags "$vnet_source" "$vnet_run" "VNet $AFD_PLS_E2E_VNET"
    vnet_needs_tags=false
  fi
  for spec in "${subnet_specs[@]}"; do
    name="${spec%%:*}"; prefix="${spec#*:}"
    if subnet_json="$(az network vnet subnet show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
      --vnet-name "$AFD_PLS_E2E_VNET" -n "$name" -o json 2>/dev/null)"; then
      test "$(jq -r '[.addressPrefix // empty,.addressPrefixes[]?] |
        map(select(length > 0)) | sort | join(",")' <<<"$subnet_json")" = "$prefix"
      test "$(jq -r '.privateLinkServiceNetworkPolicies' <<<"$subnet_json")" = Disabled
      printf 'reuse subnet %s (%s)\n' "$name" "$prefix"
    else
      printf 'missing subnet %s (%s)\n' "$name" "$prefix"
    fi
  done
else
  echo "VNet is absent; it may be created below"
fi
```

Expected: either the VNet is absent, or its exact address space/tags and every retained subnet's
prefix/policy validate. Stop on any mismatch. Do not update or delete an existing subnet.

**MUTATING — create only what is absent:**

```bash
test "$AFD_PLS_E2E_APPROVED" = true
if [[ "$vnet_exists" == true && "${vnet_needs_tags:-false}" == true ]]; then
  az network vnet update --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --name "$AFD_PLS_E2E_VNET" --tags "${tags[@]}" --output none
fi
if [[ "$vnet_exists" == false ]]; then
  az network vnet create --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --name "$AFD_PLS_E2E_VNET" --location "$AFD_PLS_E2E_LOCATION" \
    --address-prefixes 10.70.0.0/16 --tags "${tags[@]}" --output none
fi
for spec in "${subnet_specs[@]}"; do
  name="${spec%%:*}"; prefix="${spec#*:}"
  if ! az network vnet subnet show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --vnet-name "$AFD_PLS_E2E_VNET" -n "$name" --output none 2>/dev/null; then
    az network vnet subnet create -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
      --vnet-name "$AFD_PLS_E2E_VNET" -n "$name" --address-prefixes "$prefix" \
      --disable-private-link-service-network-policies true --output none
  fi
done
vnet_id="$(az network vnet show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "$AFD_PLS_E2E_VNET" --query id -o tsv)"
record_resource virtualNetwork "$AFD_PLS_E2E_VNET" "$vnet_id"
for spec in "${subnet_specs[@]}"; do
  name="${spec%%:*}"
  subnet_id="$(az network vnet subnet show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --vnet-name "$AFD_PLS_E2E_VNET" -n "$name" --query id -o tsv)"
  record_resource subnet "$name" "$subnet_id"
done
```

The `az network vnet create` command is reachable only when the preceding `show` proved the VNet
absent. It must never be run against a retained VNet.

**READ ONLY — verify before Stage 03:**

```bash
az network vnet subnet list -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --vnet-name "$AFD_PLS_E2E_VNET" \
  --query '[].{name:name,prefix:addressPrefix,plsPolicy:privateLinkServiceNetworkPolicies}' -o table
```

Expected: the five exact names/prefixes appear and every PLS policy is `Disabled`.

### 6.3 Stage 03 — inspect and conditionally create AKS

**READ ONLY — validate subnets and retained clusters before any cluster creation:**

```bash
cluster_specs=(
  "$AFD_PLS_E2E_HUB_CLUSTER:hub:$AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP"
  "$AFD_PLS_E2E_MEMBER1_CLUSTER:member-1:$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP"
  "$AFD_PLS_E2E_MEMBER2_CLUSTER:member-2:$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"
)
validate_cluster() {
  local cluster="$1" subnet="$2" node_rg="$3" json pool_subnet
  json="$(az aks show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" -o json)"
  validate_resource_tags "$(jq -r '.tags.source // ""' <<<"$json")" \
    "$(jq -r '.tags["run-id"] // ""' <<<"$json")" "AKS $cluster"
  test "$(jq -r '.resourceGroup' <<<"$json")" = "$AFD_PLS_E2E_RESOURCE_GROUP"
  test "$(jq -r '.location' <<<"$json")" = "$AFD_PLS_E2E_LOCATION"
  test "$(jq -r '.nodeResourceGroup' <<<"$json")" = "$node_rg"
  test "$(jq -r '.networkProfile.loadBalancerSku' <<<"$json")" = standard
  test "$(jq -r '.aadProfile.managed' <<<"$json")" = true
  test "$(jq -r '.aadProfile.enableAzureRbac' <<<"$json")" = true
  test "$(jq -r '.oidcIssuerProfile.enabled' <<<"$json")" = true
  test "$(jq -r '.securityProfile.workloadIdentity.enabled //
    .workloadIdentityProfile.enabled // false' <<<"$json")" = true
  test "$(az aks nodepool show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --cluster-name "$cluster" -n nodepool1 --query vmSize -o tsv)" = Standard_D2as_v4
  pool_subnet="$(az aks nodepool show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --cluster-name "$cluster" -n nodepool1 --query vnetSubnetId -o tsv)"
  test "${pool_subnet,,}" = "${vnet_id,,}/subnets/${subnet}"
}
for spec in "${cluster_specs[@]}"; do
  cluster="${spec%%:*}"; rest="${spec#*:}"; subnet="${rest%%:*}"; node_rg="${rest#*:}"
  az network vnet subnet show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --vnet-name "$AFD_PLS_E2E_VNET" -n "$subnet" --output none
  if az aks show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" --output none 2>/dev/null; then
    validate_cluster "$cluster" "$subnet" "$node_rg"
    az group show --name "$node_rg" --output none
    node_source="$(az group show --name "$node_rg" --query tags.source -o tsv)"
    node_run="$(az group show --name "$node_rg" --query 'tags."run-id"' -o tsv)"
    if [[ -n "$node_source" || -n "$node_run" ]]; then
      validate_resource_tags "$node_source" "$node_run" "node resource group $node_rg"
    fi
  else
    printf 'missing AKS %s\n' "$cluster"
  fi
done
```

Expected: each retained cluster validates exactly; missing clusters are named.

**MUTATING — create only missing clusters, then record clusters and identities:**

```bash
test "$AFD_PLS_E2E_APPROVED" = true
for spec in "${cluster_specs[@]}"; do
  cluster="${spec%%:*}"; rest="${spec#*:}"; subnet="${rest%%:*}"; node_rg="${rest#*:}"
  if existing_json="$(az aks show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" -o json 2>/dev/null)"; then
    validate_resource_tags "$(jq -r '.tags.source // ""' <<<"$existing_json")" \
      "$(jq -r '.tags["run-id"] // ""' <<<"$existing_json")" "AKS $cluster"
    if [[ "$(jq -r '.aadProfile.managed // false' <<<"$existing_json")" != true ||
          "$(jq -r '.aadProfile.enableAzureRbac // false' <<<"$existing_json")" != true ]]; then
      echo "Enabling Microsoft Entra integration and Azure RBAC on retained cluster $cluster"
      az aks update -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" \
        --enable-aad --enable-azure-rbac --output none
    fi
  else
    az aks create -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" \
      --location "$AFD_PLS_E2E_LOCATION" --node-count 1 --node-vm-size Standard_D2as_v4 \
      --network-plugin azure --load-balancer-sku standard \
      --vnet-subnet-id "${vnet_id}/subnets/${subnet}" --enable-managed-identity \
      --enable-aad --enable-azure-rbac \
      --enable-oidc-issuer --enable-workload-identity --attach-acr "$AFD_PLS_E2E_ACR" \
      --node-resource-group "$node_rg" --generate-ssh-keys --tags "${tags[@]}" --output none
  fi
  validate_cluster "$cluster" "$subnet" "$node_rg"
  cluster_json="$(az aks show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" -o json)"
  record_resource managedCluster "$cluster" "$(jq -r '.id' <<<"$cluster_json")"
  record_identity "${cluster}-control-plane" "" "$(jq -r '.identity.principalId' <<<"$cluster_json")" \
    "$(jq -r '.id' <<<"$cluster_json")"
  record_identity "${cluster}-kubelet" \
    "$(jq -r '.identityProfile.kubeletidentity.clientId' <<<"$cluster_json")" \
    "$(jq -r '.identityProfile.kubeletidentity.objectId' <<<"$cluster_json")" \
    "$(jq -r '.identityProfile.kubeletidentity.resourceId' <<<"$cluster_json")"
  node_source="$(az group show -n "$node_rg" --query tags.source -o tsv)"
  node_run="$(az group show -n "$node_rg" --query 'tags."run-id"' -o tsv)"
  if [[ -z "$node_source" && -z "$node_run" ]]; then
    az group update -n "$node_rg" --tags "${tags[@]}" --output none
  else
    validate_resource_tags "$node_source" "$node_run" "node resource group $node_rg"
  fi
  validate_tagged_resource_group "$node_rg"
  record_resource nodeResourceGroup "$node_rg" "$(az group show -n "$node_rg" --query id -o tsv)"
done
```

AKS can report `Running` even when a development-subscription policy has deallocated the
underlying VMSS. Inspect and start only deallocated run-scoped VMSS instances:

```bash
for node_rg in \
  "$AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  vmss="$(az vmss list -g "$node_rg" --query '[0].name' -o tsv)"
  test -n "$vmss" || { echo "ERROR: no VMSS found in $node_rg"; continue; }
  power="$(az vmss list-instances -g "$node_rg" -n "$vmss" --expand instanceView \
    --query '[0].instanceView.statuses[?starts_with(code, `PowerState/`)].code | [0]' -o tsv)"
  echo "$node_rg/$vmss: $power"
  if [[ "$power" != PowerState/running ]]; then
    test "$AFD_PLS_E2E_APPROVED" = true
    echo "Starting $node_rg/$vmss"
    az vmss start -g "$node_rg" -n "$vmss" --no-wait
    for _ in $(seq 1 60); do
      power="$(az vmss list-instances -g "$node_rg" -n "$vmss" --expand instanceView \
        --query '[0].instanceView.statuses[?starts_with(code, `PowerState/`)].code | [0]' -o tsv)"
      [[ "$power" == PowerState/running ]] && break
      sleep 15
    done
    [[ "$power" == PowerState/running ]] ||
      echo "ERROR: $node_rg/$vmss did not reach running state"
  fi
done
```

**READ ONLY — verify before Stage 04:**

```bash
az aks list -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --query '[].{name:name,nodeResourceGroup:nodeResourceGroup,power:powerState.code}' -o table
for node_rg in \
  "$AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  vmss="$(az vmss list -g "$node_rg" --query '[0].name' -o tsv)"
  az vmss list-instances -g "$node_rg" -n "$vmss" --expand instanceView \
    --query '[].{name:name,power:instanceView.statuses[?starts_with(code, `PowerState/`)].displayStatus|[0]}' \
    -o table
done
```

Expected: exactly the hub and two members are `Running`, and every VMSS instance is
`VM running`.

### 6.4 Stage 04 — identities, federations, scoped RBAC, and contexts

**READ ONLY — inspect retained identities, federations, roles, and cluster issuers:**

```bash
identity_specs=(
  "hub_gateway:afd-hub-id-${AFD_PLS_E2E_RUN_ID}:$AFD_PLS_E2E_HUB_CLUSTER:hub-gateway-controller-manager"
)
federation_name="fleet-system-${AFD_PLS_E2E_RUN_ID}"
for spec in "${identity_specs[@]}"; do
  IFS=: read -r key name cluster service_account <<<"$spec"
  az aks show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" \
    --query '{name:name,issuer:oidcIssuerProfile.issuerUrl}' -o table
  if json="$(az identity show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$name" -o json 2>/dev/null)"; then
    validate_resource_tags "$(jq -r '.tags.source // ""' <<<"$json")" \
      "$(jq -r '.tags["run-id"] // ""' <<<"$json")" "identity $name"
    test "$(jq -r '.location' <<<"$json")" = "$AFD_PLS_E2E_LOCATION"
    az identity federated-credential show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
      --identity-name "$name" -n "$federation_name" -o json 2>/dev/null || true
  else
    printf 'missing identity %s\n' "$name"
  fi
done
hub_role_id="$(az role definition list --name Contributor --query '[0].name' -o tsv)"
member_role_id="$(az role definition list --name 'Network Contributor' --query '[0].name' -o tsv)"
test -n "$hub_role_id"; test -n "$member_role_id"
```

Expected: retained identities have exact tags/location. Any retained federation must have the
cluster issuer, `system:serviceaccount:fleet-system:<service-account>` subject, and
`api://AzureADTokenExchange` audience.

**MUTATING — conditionally create identities and exact federations:**

```bash
test "$AFD_PLS_E2E_APPROVED" = true
for spec in "${identity_specs[@]}"; do
  IFS=: read -r key name cluster service_account <<<"$spec"
  if ! az identity show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$name" --output none 2>/dev/null; then
    az identity create -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$name" \
      --location "$AFD_PLS_E2E_LOCATION" --tags "${tags[@]}" --output none
  fi
  json="$(az identity show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$name" -o json)"
  client_id="$(jq -r '.clientId' <<<"$json")"; principal_id="$(jq -r '.principalId' <<<"$json")"
  record_identity "$name" "$client_id" "$principal_id" "$(jq -r '.id' <<<"$json")"
  printf -v "${key}_client_id" '%s' "$client_id"
  printf -v "${key}_principal_id" '%s' "$principal_id"
  issuer="$(az aks show -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" \
    --query oidcIssuerProfile.issuerUrl -o tsv)"
  subject="system:serviceaccount:fleet-system:${service_account}"
  if fed="$(az identity federated-credential show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --identity-name "$name" -n "$federation_name" -o json 2>/dev/null)"; then
    test "$(jq -r '.issuer' <<<"$fed")" = "$issuer"
    test "$(jq -r '.subject' <<<"$fed")" = "$subject"
    test "$(jq -r '.audiences | sort | join(",")' <<<"$fed")" = api://AzureADTokenExchange
  else
    az identity federated-credential create -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
      --identity-name "$name" -n "$federation_name" --issuer "$issuer" \
      --subject "$subject" --audiences api://AzureADTokenExchange --output none
  fi
  fed_id="$(az identity federated-credential show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --identity-name "$name" -n "$federation_name" --query id -o tsv)"
  record_resource federatedCredential "${name}/${federation_name}" "$fed_id"
done
```

Create missing assignments only at the approved RG scopes:

```bash
# Reload prerequisites so this block is safe to run independently in a new shell.
hub_role_id="$(az role definition list --name Contributor --query '[0].name' -o tsv)"
member_role_id="$(az role definition list --name 'Network Contributor' --query '[0].name' -o tsv)"
reader_role_id="$(az role definition list --name Reader --query '[0].name' -o tsv)"
hub_gateway_principal_id="$(az identity show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" --query principalId -o tsv)"
member_1_principal_id="$(jq -r --arg n "${AFD_PLS_E2E_MEMBER1_CLUSTER}-kubelet" \
  '.identities[] | select(.name==$n).principalId' "$AFD_PLS_E2E_STATE_FILE")"
member_2_principal_id="$(jq -r --arg n "${AFD_PLS_E2E_MEMBER2_CLUSTER}-kubelet" \
  '.identities[] | select(.name==$n).principalId' "$AFD_PLS_E2E_STATE_FILE")"
member_1_aks_principal_id="$(az aks show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "$AFD_PLS_E2E_MEMBER1_CLUSTER" --query identity.principalId -o tsv)"
member_2_aks_principal_id="$(az aks show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "$AFD_PLS_E2E_MEMBER2_CLUSTER" --query identity.principalId -o tsv)"
vnet_id="$(az network vnet show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "$AFD_PLS_E2E_VNET" --query id -o tsv)"

for required_name in \
  hub_role_id member_role_id reader_role_id hub_gateway_principal_id \
  member_1_principal_id member_2_principal_id \
  member_1_aks_principal_id member_2_aks_principal_id vnet_id; do
  if [[ -z "${!required_name:-}" ]]; then
    echo "ERROR: ${required_name} is empty. Stop before role-assignment commands." >&2
  else
    printf '%s=%s\n' "$required_name" "${!required_name}"
  fi
done

ensure_assignment() {
  local principal="$1" role_id="$2" scope="$3" logical_name="$4"
  local all_assignments matches count assignment created

  if [[ -z "$principal" || -z "$role_id" || -z "$scope" || -z "$logical_name" ]]; then
    echo "ERROR: ensure_assignment received an empty argument:" >&2
    printf '  principal=%q\n  role_id=%q\n  scope=%q\n  logical_name=%q\n' \
      "$principal" "$role_id" "$scope" "$logical_name" >&2
    return 1
  fi

  echo "Inspecting ${logical_name} assignment at ${scope}"
  if ! all_assignments="$(az role assignment list --assignee-object-id "$principal" \
      --scope "$scope" --fill-principal-name false -o json 2>&1)"; then
    echo "ERROR: Azure could not list assignments at ${scope}:" >&2
    printf '%s\n' "$all_assignments" >&2
    return 1
  fi
  if ! matches="$(jq --arg principal "$principal" --arg role "$role_id" --arg scope "$scope" '
      [.[] | select(
        ((.principalId // "") | ascii_downcase) == ($principal | ascii_downcase) and
        ((.roleDefinitionId // "") | ascii_downcase | endswith("/" + ($role | ascii_downcase))) and
        ((.scope // "") | ascii_downcase) == ($scope | ascii_downcase)
      )]' <<<"$all_assignments" 2>&1)"; then
    echo "ERROR: Could not filter Azure role assignments:" >&2
    printf '%s\n' "$matches" >&2
    return 1
  fi
  count="$(jq 'length' <<<"$matches")"
  if (( count > 1 )); then
    echo "ERROR: Found ${count} matching assignments for ${logical_name}; refusing to choose one." >&2
    jq . <<<"$matches" >&2
    return 1
  fi

  if (( count == 0 )); then
    echo "Creating ${logical_name} assignment"
    if ! created="$(az role assignment create --assignee-object-id "$principal" \
        --assignee-principal-type ServicePrincipal --role "$role_id" \
        --scope "$scope" -o json 2>&1)"; then
      echo "ERROR: Azure could not create ${logical_name}:" >&2
      printf '%s\n' "$created" >&2
      return 1
    fi
    assignment="$(jq -r '.id // empty' <<<"$created")"
  else
    assignment="$(jq -r '.[0].id // empty' <<<"$matches")"
    echo "Reusing ${logical_name}: ${assignment}"
  fi
  if [[ -z "$assignment" ]]; then
    echo "ERROR: Azure returned no assignment ID for ${logical_name}." >&2
    return 1
  fi
  if ! record_role_assignment "$logical_name" "$principal" "$role_id" "$scope" "$assignment"; then
    echo "ERROR: Could not record ${logical_name} in $AFD_PLS_E2E_STATE_FILE." >&2
    return 1
  fi
  echo "Recorded ${logical_name}"
}
primary_scope="/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_RESOURCE_GROUP}"
if ! ensure_assignment "$hub_gateway_principal_id" "$hub_role_id" "$primary_scope" hub-afd; then
  echo "STOP: Fix hub-afd above before continuing."
fi
if ! ensure_assignment "$hub_gateway_principal_id" "$reader_role_id" \
    "/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" \
    hub-read-member-1-network; then
  echo "STOP: Fix hub-read-member-1-network above before continuing."
fi
if ! ensure_assignment "$hub_gateway_principal_id" "$reader_role_id" \
    "/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}" \
    hub-read-member-2-network; then
  echo "STOP: Fix hub-read-member-2-network above before continuing."
fi
if ! ensure_assignment "$member_1_principal_id" "$member_role_id" \
    "/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" \
    member-1-pls; then
  echo "STOP: Fix member-1-pls above before continuing."
fi
if ! ensure_assignment "$member_2_principal_id" "$member_role_id" \
    "/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}" \
    member-2-pls; then
  echo "STOP: Fix member-2-pls above before continuing."
fi
if ! ensure_assignment "$member_1_aks_principal_id" "$member_role_id" "$vnet_id" \
    member-1-aks-vnet; then
  echo "STOP: Fix member-1-aks-vnet above before continuing."
fi
if ! ensure_assignment "$member_2_aks_principal_id" "$member_role_id" "$vnet_id" \
    member-2-aks-vnet; then
  echo "STOP: Fix member-2-aks-vnet above before continuing."
fi
```

Each invocation prints either `Reusing`, `Creating`, or a complete `ERROR` message and returns to
your prompt. Do not continue to kubeconfig commands unless all seven print `Recorded`.

The two `member-*-aks-vnet` assignments grant each member AKS cloud-provider identity
`Network Contributor` on the exact test VNet. This is required for internal load balancer and PLS
reconciliation because the PLS NAT subnets live in the primary resource group rather than the AKS
node resource groups.

The two `hub-read-member-*-network` assignments grant the hub Gateway controller built-in
`Reader` on each member AKS node resource group. Azure Front Door linked-resource authorization
requires the controller identity to read customer-created PLS resources, whose generated names do
not exist during platform setup. Resource-group scope is an intentional POC tradeoff that keeps
Part 2 self-service; it permits read-only access to other Azure resources in those node resource
groups. A production deployment should replace built-in `Reader` with a validated purpose-built
role containing only the required Azure network read operations.

`--fill-principal-name false` is required in this environment. It prevents Azure CLI from querying
Microsoft Graph, which is blocked by the organization's conditional-access token protection policy
in a headless terminal.

`az aks get-credentials` changes only the run-scoped local kubeconfig:

```bash
for spec in \
  "$AFD_PLS_E2E_HUB_CLUSTER:$AFD_PLS_E2E_HUB_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER1_CLUSTER:$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER2_CLUSTER:$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  cluster="${spec%%:*}"; context="${spec#*:}"
  az aks get-credentials -g "$AFD_PLS_E2E_RESOURCE_GROUP" -n "$cluster" --admin \
    --file "$AFD_PLS_E2E_KUBECONFIG" --context "$context" --overwrite-existing
  if kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" config get-contexts \
    "${context}-admin" --no-headers >/dev/null 2>&1; then
    if kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" config get-contexts \
        "$context" --no-headers >/dev/null 2>&1; then
      echo "Normalized context $context already exists; deleting duplicate ${context}-admin"
      kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" config delete-context \
        "${context}-admin"
    else
      kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" config rename-context \
        "${context}-admin" "$context"
    fi
  fi
done
```

On a retained run, messages saying the normalized context already exists are expected. The command
deletes only the duplicate context entry; it does not delete or modify an AKS cluster.

**READ ONLY — verify before Stage 05:**

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" config get-contexts
jq '.identities,.roleAssignments' "$AFD_PLS_E2E_STATE_FILE"
```

Expected: all three exact contexts, the hub controller and AKS-managed identities, and seven scoped
assignments appear.

### 6.5 Stage 05 — pinned CRDs and Fleet registration

**READ ONLY — resolve pinned inputs, principals, and verify APIs:**

```bash
module_cache="$(go env GOMODCACHE)"
fleet_crd_dir="${module_cache}/go.goms.io/fleet@v0.14.0/config/crd/bases"
gateway_crd_dir="${module_cache}/sigs.k8s.io/gateway-api@v1.2.1/config/crd/standard"
test -f "${fleet_crd_dir}/cluster.kubernetes-fleet.io_memberclusters.yaml"
test -f "${fleet_crd_dir}/cluster.kubernetes-fleet.io_internalmemberclusters.yaml"
test -f "${gateway_crd_dir}/gateway.networking.k8s.io_gateways.yaml"
for context in "$AFD_PLS_E2E_HUB_CONTEXT" "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  k "$context" get --raw=/readyz
done
member_1_principal="$(jq -r --arg n "${AFD_PLS_E2E_MEMBER1_CLUSTER}-kubelet" \
  '.identities[] | select(.name==$n).principalId' "$AFD_PLS_E2E_STATE_FILE")"
member_2_principal="$(jq -r --arg n "${AFD_PLS_E2E_MEMBER2_CLUSTER}-kubelet" \
  '.identities[] | select(.name==$n).principalId' "$AFD_PLS_E2E_STATE_FILE")"
test -n "$member_1_principal"; test -n "$member_2_principal"
```

Expected: every `/readyz` returns `ok`, pinned files exist, and both principals are non-empty.

**MUTATING — apply namespaces and pinned CRDs:**

```bash
test "$AFD_PLS_E2E_APPROVED" = true
for context in "$AFD_PLS_E2E_HUB_CONTEXT" "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  k "$context" create namespace fleet-system --dry-run=client -o yaml | k "$context" apply -f -
done
k "$AFD_PLS_E2E_HUB_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "${fleet_crd_dir}/cluster.kubernetes-fleet.io_memberclusters.yaml" \
  -f "${fleet_crd_dir}/cluster.kubernetes-fleet.io_internalmemberclusters.yaml" \
  -f "$gateway_crd_dir"
```

Do not apply `config/crd/bases` directly here. The hub and member chart init containers install the
appropriate networking CRDs using `net-crd-installer --mode=hub|member`, matching production chart
ownership and keeping hub-only APIs out of member clusters.

Render the existing getting-started chart with both Azure principals, apply it, and create only
`MemberCluster`s. Do not create `InternalMemberCluster`s yet:

```bash
registration_manifest="${AFD_PLS_E2E_ARTIFACT_DIR}/hub-member-registration.yaml"
HELM_NO_PLUGINS=1 helm template phase7-registration examples/getting-started/charts/hub \
  --namespace fleet-system --set-string userNS=afd-pls-e2e \
  --set-string memberClusterConfigs[0].memberID="$AFD_PLS_E2E_MEMBER1_CLUSTER" \
  --set-string memberClusterConfigs[0].principalID="$member_1_principal" \
  --set-string memberClusterConfigs[1].memberID="$AFD_PLS_E2E_MEMBER2_CLUSTER" \
  --set-string memberClusterConfigs[1].principalID="$member_2_principal" \
  >"$registration_manifest"
k "$AFD_PLS_E2E_HUB_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "$registration_manifest"
for item in \
  "$AFD_PLS_E2E_MEMBER1_CLUSTER:$member_1_principal" \
  "$AFD_PLS_E2E_MEMBER2_CLUSTER:$member_2_principal"; do
  member="${item%%:*}"; principal="${item#*:}"
  cat <<EOF | k "$AFD_PLS_E2E_HUB_CONTEXT" apply -f -
apiVersion: cluster.kubernetes-fleet.io/v1beta1
kind: MemberCluster
metadata:
  name: ${member}
  labels:
    networking.fleet.azure.com/afd-poc: "true"
spec:
  identity:
    apiGroup: rbac.authorization.k8s.io
    kind: User
    name: ${principal}
EOF
done
```

**READ ONLY — verify before controller deployment:**

```bash
k "$AFD_PLS_E2E_HUB_CONTEXT" get memberclusters
test "$(k "$AFD_PLS_E2E_HUB_CONTEXT" get internalmemberclusters -A \
  --no-headers 2>/dev/null | wc -l)" -eq 0
```

Expected: both MemberClusters exist and no IMC exists.

### 6.6 Render and deploy only the controllers

Load the identity, cluster, and immutable-image values and render the production charts. The chart
init containers install the hub/member networking CRDs. Do not append application resources to
these manifests.

```bash
tenant_id="$(az account show --query tenantId -o tsv)"
hub_client="$(jq -r --arg n "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" \
  '.identities[] | select(.name==$n).clientId' "$AFD_PLS_E2E_STATE_FILE")"
member_1_client="$(jq -r --arg n "${AFD_PLS_E2E_MEMBER1_CLUSTER}-kubelet" \
  '.identities[] | select(.name==$n).clientId' "$AFD_PLS_E2E_STATE_FILE")"
member_2_client="$(jq -r --arg n "${AFD_PLS_E2E_MEMBER2_CLUSTER}-kubelet" \
  '.identities[] | select(.name==$n).clientId' "$AFD_PLS_E2E_STATE_FILE")"
hub_server="$(k "$AFD_PLS_E2E_HUB_CONTEXT" config view --raw --minify \
  -o jsonpath='{.clusters[0].cluster.server}')"
hub_ca="$(k "$AFD_PLS_E2E_HUB_CONTEXT" config view --raw --minify \
  -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
hub_image="$(jq -r '.images.hubGateway' "$AFD_PLS_E2E_STATE_FILE")"
member_image="$(jq -r '.images.member' "$AFD_PLS_E2E_STATE_FILE")"
crd_image="$(jq -r '.images.crdInstaller' "$AFD_PLS_E2E_STATE_FILE")"
refresh_image="$(jq -r '.images.refreshToken' "$AFD_PLS_E2E_STATE_FILE")"
hub_repo="${hub_image%@*}"; hub_digest="${hub_image##*@}"
member_repo="${member_image%@*}"; member_digest="${member_image##*@}"
crd_repo="${crd_image%@*}"; crd_digest="${crd_image##*@}"
refresh_repo="${refresh_image%@*}"; refresh_digest="${refresh_image##*@}"
```

```bash
HELM_NO_PLUGINS=1 helm template phase7 charts/hub-gateway-controller-manager \
  --namespace fleet-system --set-string image.repository="$hub_repo" \
  --set-string image.digest="$hub_digest" --set-string crdInstaller.image.repository="$crd_repo" \
  --set-string crdInstaller.image.digest="$crd_digest" --set-string azure.clientId="$hub_client" \
  --set-string azure.tenantId="$tenant_id" \
  --set-string azure.subscriptionId="$EXPECTED_SUBSCRIPTION_ID" \
  --set-string azure.resourceGroup="$AFD_PLS_E2E_RESOURCE_GROUP" \
  --set-string resources.requests.cpu=25m \
  --set-string azure.location="$AFD_PLS_E2E_LOCATION" >"$AFD_PLS_E2E_HUB_MANIFEST"

render_member() {
  local manifest="$1" member="$2" client="$3" node_rg="$4"
  HELM_NO_PLUGINS=1 helm template phase7 charts/member-net-controller-manager \
    --namespace fleet-system --set-string fullnameOverride=member-net-controller-manager \
    --set-string image.repository="$member_repo" --set-string image.digest="$member_digest" \
    --set crdInstaller.enabled=true --set-string crdInstaller.image.repository="$crd_repo" \
    --set-string crdInstaller.image.digest="$crd_digest" --set crdInstaller.isE2ETest=true \
    --set-string config.hubURL="$hub_server" --set-string config.hubCA="$hub_ca" \
    --set-string config.memberClusterName="$member" --set-string config.provider=azure \
    --set-string refreshtoken.repository="$refresh_repo" \
    --set-string refreshtoken.digest="$refresh_digest" \
    --set-string resources.requests.cpu=25m --set-string resources.requests.memory=64Mi \
    --set tlsClientInsecure=false --set-string azure.clientid="$client" \
    --set azure.workloadIdentityEnabled=false --set enableTrafficManagerFeature=false \
    --set enableAFDPrivateLinkFeature=true \
    --set-string azureCloudConfig.tenantId="$tenant_id" \
    --set-string azureCloudConfig.subscriptionId="$EXPECTED_SUBSCRIPTION_ID" \
    --set-string azureCloudConfig.aadClientId="$client" \
    --set azureCloudConfig.useManagedIdentityExtension=true \
    --set-string azureCloudConfig.userAssignedIdentityID="$client" \
    --set azureCloudConfig.cloudProviderRateLimit=false \
    --set-string azureCloudConfig.resourceGroup="$node_rg" \
    --set-string azureCloudConfig.location="$AFD_PLS_E2E_LOCATION" >"$manifest"
}
render_member "$AFD_PLS_E2E_MEMBER1_MANIFEST" "$AFD_PLS_E2E_MEMBER1_CLUSTER" \
  "$member_1_client" "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP"
render_member "$AFD_PLS_E2E_MEMBER2_MANIFEST" "$AFD_PLS_E2E_MEMBER2_CLUSTER" \
  "$member_2_client" "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"
```

Verify that only digest-pinned controller/chart objects were rendered:

```bash
for manifest in "$AFD_PLS_E2E_HUB_MANIFEST" "$AFD_PLS_E2E_MEMBER1_MANIFEST" \
  "$AFD_PLS_E2E_MEMBER2_MANIFEST"; do
  test -s "$manifest"
  ! grep -E '^[[:space:]]*image:' "$manifest" |
    grep -Ev '@sha256:[[:xdigit:]]{64}"?[[:space:]]*$'
  ! grep -Eq 'kind: (Service|Gateway|HTTPRoute|MultiClusterBackend)' "$manifest"
done
```

**MUTATING — deploy the three controller charts:**

```bash
test "$AFD_PLS_E2E_APPROVED" = true
k "$AFD_PLS_E2E_HUB_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "$AFD_PLS_E2E_HUB_MANIFEST"
k "$AFD_PLS_E2E_MEMBER1_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "$AFD_PLS_E2E_MEMBER1_MANIFEST"
k "$AFD_PLS_E2E_MEMBER2_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "$AFD_PLS_E2E_MEMBER2_MANIFEST"
k "$AFD_PLS_E2E_HUB_CONTEXT" -n fleet-system wait --for=condition=Available \
  deployment/hub-gateway-controller-manager --timeout=20m
for context in "$AFD_PLS_E2E_MEMBER1_CONTEXT" "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  k "$context" -n fleet-system wait --for=condition=Available \
    deployment/member-net-controller-manager --timeout=20m
done
k "$AFD_PLS_E2E_HUB_CONTEXT" wait --for=condition=Established \
  crd/multiclusterbackends.networking.fleet.azure.com \
  crd/serviceoriginassignments.networking.fleet.azure.com --timeout=5m
```

### 6.7 Join the real networking agents

Create each IMC, wait for a real `ServiceExportImportAgent` heartbeat plus `Joined=True`, and only
then patch the aggregate, selector-facing MemberCluster condition. This aggregate condition is the
validation-scoped substitute described under the Fleet registration limitation below.

```bash
join_member() {
  local member="$1"
  local namespace="fleet-member-${member}"
  local now
  cat <<EOF | k "$AFD_PLS_E2E_HUB_CONTEXT" apply -f -
apiVersion: cluster.kubernetes-fleet.io/v1beta1
kind: InternalMemberCluster
metadata:
  name: ${member}
  namespace: ${namespace}
spec:
  state: Join
  heartbeatPeriodSeconds: 10
EOF
  for _ in $(seq 1 60); do
    if k "$AFD_PLS_E2E_HUB_CONTEXT" -n "$namespace" get internalmembercluster "$member" \
      -o json | jq -e 'any(.status.agentStatus[]?;
        .type=="ServiceExportImportAgent" and .lastReceivedHeartbeat!=null and
        any(.conditions[]?; .type=="Joined" and .status=="True"))' >/dev/null; then
      now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      k "$AFD_PLS_E2E_HUB_CONTEXT" patch membercluster "$member" --subresource=status \
        --type=merge -p "{\"status\":{\"conditions\":[{\"type\":\"Joined\",\"status\":\"True\",
        \"reason\":\"NetworkingAgentJoined\",\"message\":\"Phase 7 networking agent joined through the existing E2E registration path\",
        \"lastTransitionTime\":\"${now}\"}]}}"
      return
    fi
    sleep 10
  done
  echo "networking agent for ${member} did not join within 10 minutes" >&2
  return 1
}
join_member "$AFD_PLS_E2E_MEMBER1_CLUSTER"
join_member "$AFD_PLS_E2E_MEMBER2_CLUSTER"
```

Install the reusable `GatewayClass`; do not create a namespaced Gateway yet:

```bash
cat <<'EOF' | k "$AFD_PLS_E2E_HUB_CONTEXT" apply --server-side \
  --field-manager=phase7-e2e -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: azure-fleet-afd
spec:
  controllerName: networking.fleet.azure.com/afd
EOF
```

## 7. Platform readiness verification

**READ ONLY:**

```bash
for member in "$AFD_PLS_E2E_MEMBER1_CLUSTER" "$AFD_PLS_E2E_MEMBER2_CLUSTER"; do
  k "$AFD_PLS_E2E_HUB_CONTEXT" -n "fleet-member-${member}" get \
    internalmembercluster "$member" -o json | jq -e 'any(.status.agentStatus[]?;
      .type=="ServiceExportImportAgent" and .lastReceivedHeartbeat!=null and
      any(.conditions[]?; .type=="Joined" and .status=="True"))'
done
k "$AFD_PLS_E2E_HUB_CONTEXT" get gatewayclass azure-fleet-afd
k "$AFD_PLS_E2E_HUB_CONTEXT" get crd \
  multiclusterbackends.networking.fleet.azure.com \
  serviceoriginassignments.networking.fleet.azure.com
test "$(k "$AFD_PLS_E2E_HUB_CONTEXT" get gateways -A --no-headers 2>/dev/null | wc -l)" -eq 0
test "$(k "$AFD_PLS_E2E_HUB_CONTEXT" get multiclusterbackends -A \
  --no-headers 2>/dev/null | wc -l)" -eq 0
for context in "$AFD_PLS_E2E_MEMBER1_CONTEXT" "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  test "$(k "$context" get services -A --no-headers |
    awk '$1 == "afd-pls-e2e" {count++} END {print count+0}')" -eq 0
done
test "$(az afd profile list -g "$AFD_PLS_E2E_RESOURCE_GROUP" --query 'length(@)' -o tsv)" = 0
test "$(az network front-door waf-policy list -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --query 'length(@)' -o tsv)" = 0
```

Expected: both heartbeat checks print `true`; the GatewayClass and networking CRDs exist; and all
application, Gateway, AFD, and WAF absence checks succeed.

### Optional automation/reference

The existing `step-06-waf-manifests.sh`, `step-07-deploy-join.sh`, and aggregate setup target still
combine platform setup with WAF, echo, Service, and Gateway resources. They are useful only as
implementation references; their boundaries do not match this four-part operator flow. The
literal commands above are authoritative.

[Previous: cleanup (for retained runs)](04-cleanup.md) ·
[Next: global Service scenario](02-global-service-scenario.md)
