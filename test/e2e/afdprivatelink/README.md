# Phase 7 AFD Private Link human-validation runbook

> **Status: In progress.** Retained run `p7-10082105` has completed setup, real networking-agent
> join, two private-origin approval, Gateway programming, two-member HTTP 200 sampling, and the WAF
> HTTP 403 assertion. Label withdrawal, fail-static outage, Gateway deletion ownership, and final
> cleanup still require human execution. Do not interpret this document or its commit as complete
> Phase 7 validation.

This is the standalone runbook for the real-Azure Phase 7 human-run POC validation. Commands marked
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

One run creates:

- primary RG `fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}`;
- Basic ACR `fleetp7${AFD_PLS_E2E_RUN_ID//-/}d712`;
- four image repositories/builds: hub Gateway controller, member controller, CRD installer, echo;
- one hub-controller user-assigned identity/federated credential, three AKS control-plane
  identities, three AKS kubelet identities, five built-in resource-scoped assignments, and three
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

## 6. Run the seven literal setup stages

These copy/paste commands are the authoritative setup workflow. Run them in order in one Bash
shell. On a retry, reinitialize the shell, repeat the **READ ONLY** part of the failed subsection,
and run only its necessary conditional **MUTATING** commands. The stage scripts and Make targets
are secondary reference implementations; they are listed in Appendix A.

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
  test "$(jq -r '.workloadIdentityProfile.enabled' <<<"$json")" = true
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
  hub_role_id member_role_id hub_gateway_principal_id \
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
your prompt. Do not continue to kubeconfig commands unless all five print `Recorded`.

The two `member-*-aks-vnet` assignments grant each member AKS cloud-provider identity
`Network Contributor` on the exact test VNet. This is required for internal load balancer and PLS
reconciliation because the PLS NAT subnets live in the primary resource group rather than the AKS
node resource groups.

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

Expected: all three exact contexts, the hub controller and AKS-managed identities, and five scoped
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

**READ ONLY — verify before Stage 06:**

```bash
k "$AFD_PLS_E2E_HUB_CONTEXT" get memberclusters
test "$(k "$AFD_PLS_E2E_HUB_CONTEXT" get internalmemberclusters -A \
  --no-headers 2>/dev/null | wc -l)" -eq 0
```

Expected: both MemberClusters exist and no IMC exists.

### 6.6 Stage 06 — WAF and digest-only manifests

**READ ONLY — inspect retained WAF objects and rendering inputs:**

```bash
waf_json="$(az network front-door waf-policy show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "$AFD_PLS_E2E_WAF_POLICY" -o json 2>/dev/null || true)"
if [[ -n "$waf_json" ]]; then
  validate_resource_tags "$(jq -r '.tags.source // ""' <<<"$waf_json")" \
    "$(jq -r '.tags["run-id"] // ""' <<<"$waf_json")" "WAF $AFD_PLS_E2E_WAF_POLICY"
  test "$(jq -r '.sku.name' <<<"$waf_json")" = Premium_AzureFrontDoor
  test "$(jq -r '.policySettings.mode' <<<"$waf_json")" = Prevention
fi
rule_json="$(az network front-door waf-policy rule show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --policy-name "$AFD_PLS_E2E_WAF_POLICY" -n BlockPOCHeader -o json 2>/dev/null || true)"
if [[ -n "$rule_json" ]]; then
  test "$(jq -r '.priority' <<<"$rule_json")" = 1
  test "$(jq -r '.action' <<<"$rule_json")" = Block
fi
jq -e '.images | length == 5 and all(.[]; contains("@sha256:"))' "$AFD_PLS_E2E_STATE_FILE"
```

Expected: absent WAF objects print nothing; retained objects validate exactly; image check is true.

**MUTATING — conditionally create the WAF policy and rule:**

```bash
test "$AFD_PLS_E2E_APPROVED" = true
if [[ -z "$waf_json" ]]; then
  az network front-door waf-policy create -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    -n "$AFD_PLS_E2E_WAF_POLICY" --sku Premium_AzureFrontDoor --mode Prevention \
    --location Global --tags "${tags[@]}" --output none
fi
if [[ -z "$rule_json" ]]; then
  az network front-door waf-policy rule create -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --policy-name "$AFD_PLS_E2E_WAF_POLICY" -n BlockPOCHeader --priority 1 \
    --rule-type MatchRule --action Block --match-variable RequestHeader.X-POC-Block \
    --operator Equal --values true --output none
fi
waf_id="$(az network front-door waf-policy show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "$AFD_PLS_E2E_WAF_POLICY" --query id -o tsv)"
record_resource wafPolicy "$AFD_PLS_E2E_WAF_POLICY" "$waf_id"
```

Rendering below is local-only. Load identity, cluster, and immutable-image values:

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
echo_image="$(jq -r '.images.echo' "$AFD_PLS_E2E_STATE_FILE")"
refresh_image="$(jq -r '.images.refreshToken' "$AFD_PLS_E2E_STATE_FILE")"
hub_repo="${hub_image%@*}"; hub_digest="${hub_image##*@}"
member_repo="${member_image%@*}"; member_digest="${member_image##*@}"
crd_repo="${crd_image%@*}"; crd_digest="${crd_image##*@}"
refresh_repo="${refresh_image%@*}"; refresh_digest="${refresh_image##*@}"
```

Render the hub and both member charts with exact digest fields:

```bash
HELM_NO_PLUGINS=1 helm template phase7 charts/hub-gateway-controller-manager \
  --namespace fleet-system --set-string image.repository="$hub_repo" \
  --set-string image.digest="$hub_digest" --set-string crdInstaller.image.repository="$crd_repo" \
  --set-string crdInstaller.image.digest="$crd_digest" --set-string azure.clientId="$hub_client" \
  --set-string azure.tenantId="$tenant_id" --set-string azure.subscriptionId="$EXPECTED_SUBSCRIPTION_ID" \
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
    --set-string refreshtoken.repository="$refresh_repo" --set-string refreshtoken.digest="$refresh_digest" \
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

Append each echo workload and internal Service. This is still local rendering, not an apply:

```bash
append_echo() {
  local manifest="$1" member="$2" subnet="$3"
  cat >>"$manifest" <<EOF
---
apiVersion: v1
kind: Namespace
metadata: {name: afd-pls-e2e}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: echo, namespace: afd-pls-e2e}
spec:
  replicas: 1
  selector: {matchLabels: {app: echo}}
  template:
    metadata: {labels: {app: echo}}
    spec:
      containers:
      - name: echo
        image: ${echo_image}
        env: [{name: MEMBER_NAME, value: "${member}"}]
        ports: [{containerPort: 8080}]
---
apiVersion: v1
kind: Service
metadata:
  name: echo
  namespace: afd-pls-e2e
  annotations:
    service.beta.kubernetes.io/azure-load-balancer-internal: "true"
    service.beta.kubernetes.io/azure-pls-create: "true"
    service.beta.kubernetes.io/azure-pls-ip-configuration-subnet: "${subnet}"
    service.beta.kubernetes.io/azure-pls-visibility: "*"
spec:
  type: LoadBalancer
  selector: {app: echo}
  ports: [{name: http, port: 80, targetPort: 8080}]
EOF
}
append_echo "$AFD_PLS_E2E_MEMBER1_MANIFEST" member-1 pls-1
append_echo "$AFD_PLS_E2E_MEMBER2_MANIFEST" member-2 pls-2
```

Append the Gateway API resources:

```bash
cat >"$AFD_PLS_E2E_GATEWAY_MANIFEST" <<EOF
apiVersion: v1
kind: Namespace
metadata: {name: afd-pls-e2e}
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: azure-fleet-afd}
spec: {controllerName: networking.fleet.azure.com/afd}
---
apiVersion: networking.fleet.azure.com/v1alpha1
kind: MultiClusterBackend
metadata: {name: echo, namespace: afd-pls-e2e}
spec:
  service: {name: echo, port: 80}
  clusterSelector: {matchLabels: {networking.fleet.azure.com/afd-poc: "true"}}
  healthProbe: {path: /}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: global
  namespace: afd-pls-e2e
  annotations:
    networking.fleet.azure.com/afd-sku: Premium_AzureFrontDoor
    networking.fleet.azure.com/afd-waf-policy-id: "${waf_id}"
spec:
  gatewayClassName: azure-fleet-afd
  listeners: [{name: http, protocol: HTTP, port: 80}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: echo, namespace: afd-pls-e2e}
spec:
  parentRefs: [{name: global}]
  rules:
  - matches: [{path: {type: PathPrefix, value: /}}]
    backendRefs: [{group: networking.fleet.azure.com, kind: MultiClusterBackend, name: echo}]
EOF
```

**READ ONLY — verify before Stage 07:**

```bash
for manifest in "$AFD_PLS_E2E_HUB_MANIFEST" "$AFD_PLS_E2E_MEMBER1_MANIFEST" \
  "$AFD_PLS_E2E_MEMBER2_MANIFEST" "$AFD_PLS_E2E_GATEWAY_MANIFEST"; do
  test -s "$manifest"
  ! grep -E '^[[:space:]]*image:' "$manifest" |
    grep -Ev '@sha256:[[:xdigit:]]{64}"?[[:space:]]*$'
done
```

Expected: all manifests are non-empty and no tag-only workload image is printed.

### 6.7 Stage 07 — apply, wait, then join

**READ ONLY — preconditions:**

```bash
for manifest in "$AFD_PLS_E2E_HUB_MANIFEST" "$AFD_PLS_E2E_MEMBER1_MANIFEST" \
  "$AFD_PLS_E2E_MEMBER2_MANIFEST" "$AFD_PLS_E2E_GATEWAY_MANIFEST"; do
  test -s "$manifest"
done
k "$AFD_PLS_E2E_HUB_CONTEXT" get membercluster "$AFD_PLS_E2E_MEMBER1_CLUSTER"
k "$AFD_PLS_E2E_HUB_CONTEXT" get membercluster "$AFD_PLS_E2E_MEMBER2_CLUSTER"
for context in "$AFD_PLS_E2E_HUB_CONTEXT" "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  echo "=== $context nodes ==="
  k "$context" get nodes
  test "$(k "$context" get nodes -o json |
    jq '[.items[].status.conditions[] | select(.type == "Ready" and .status == "True")] | length')" -ge 1 ||
    echo "STOP: $context has no Ready node; return to the VMSS recovery in Section 6.3."
done
test "$(k "$AFD_PLS_E2E_HUB_CONTEXT" -n kube-system get endpoints \
  azure-wi-webhook-webhook-service -o json |
  jq '[.subsets[]?.addresses[]?] | length')" -ge 1 ||
  echo "STOP: hub Azure Workload Identity webhook has no endpoint; wait for the hub node and webhook."
```

Expected: manifests exist, both MemberClusters are readable, every cluster has a Ready node, and
the hub Workload Identity webhook has at least one endpoint. Do not apply deployments while any
`STOP` message is printed.

**MUTATING — apply manifests and explicitly wait before creating IMCs:**

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
  k "$context" -n afd-pls-e2e wait --for=condition=Available deployment/echo --timeout=20m
done
```

Grant the hub identity `Reader` on each exact discovered PLS before AFD links the origins:

```bash
hub_principal_id="$(jq -r --arg n "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" \
  '.identities[] | select(.name==$n).principalId' "$AFD_PLS_E2E_STATE_FILE")"
reader_role_id="$(az role definition list --name Reader --query '[0].name' -o tsv)"
ensure_pls_reader() {
  local node_rg="$1" logical_name="$2" pls_json pls_id assignments assignment_id
  pls_json="$(az network private-link-service list -g "$node_rg" -o json)"
  if [[ "$(jq 'length' <<<"$pls_json")" -ne 1 ]]; then
    echo "ERROR: expected exactly one PLS in $node_rg" >&2
    return 1
  fi
  pls_id="$(jq -r '.[0].id' <<<"$pls_json")"
  assignments="$(az role assignment list --assignee-object-id "$hub_principal_id" \
    --scope "$pls_id" --fill-principal-name false -o json |
    jq --arg role "$reader_role_id" --arg scope "$pls_id" \
      '[.[] | select((.roleDefinitionId|ascii_downcase|endswith("/"+($role|ascii_downcase)))
        and (.scope|ascii_downcase)==($scope|ascii_downcase))]')"
  if [[ "$(jq 'length' <<<"$assignments")" -eq 0 ]]; then
    assignment_id="$(az role assignment create --assignee-object-id "$hub_principal_id" \
      --assignee-principal-type ServicePrincipal --role "$reader_role_id" \
      --scope "$pls_id" --query id -o tsv)"
  else
    assignment_id="$(jq -r '.[0].id' <<<"$assignments")"
  fi
  record_role_assignment "$logical_name" "$hub_principal_id" "$reader_role_id" \
    "$pls_id" "$assignment_id"
  echo "Recorded $logical_name on $pls_id"
}
ensure_pls_reader "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" hub-read-member-1-pls
ensure_pls_reader "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP" hub-read-member-2-pls
```

Wait for chart-owned networking CRDs, then apply the Gateway/backend resources:

```bash
k "$AFD_PLS_E2E_HUB_CONTEXT" wait --for=condition=Established \
  crd/multiclusterbackends.networking.fleet.azure.com \
  crd/serviceoriginassignments.networking.fleet.azure.com \
  --timeout=5m
k "$AFD_PLS_E2E_HUB_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "$AFD_PLS_E2E_GATEWAY_MANIFEST"
```

Create each IMC, wait for a real `ServiceExportImportAgent` heartbeat plus `Joined=True`, and only
then patch the aggregate MemberCluster condition:

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

**READ ONLY — verify setup before beginning Section 7:**

```bash
k "$AFD_PLS_E2E_HUB_CONTEXT" get memberclusters
for member in "$AFD_PLS_E2E_MEMBER1_CLUSTER" "$AFD_PLS_E2E_MEMBER2_CLUSTER"; do
  k "$AFD_PLS_E2E_HUB_CONTEXT" -n "fleet-member-${member}" get \
    internalmembercluster "$member" -o json | jq -e 'any(.status.agentStatus[]?;
      .type=="ServiceExportImportAgent" and .lastReceivedHeartbeat!=null and
      any(.conditions[]?; .type=="Joined" and .status=="True"))'
done
```

Expected: both MemberClusters show joined, and both jq checks print `true`. Phase 7 is still not
complete; continue with human validation and cleanup.

Generated files are mode-protected and run-scoped:

```text
.phase7-${AFD_PLS_E2E_RUN_ID}.state.json
.phase7-${AFD_PLS_E2E_RUN_ID}.kubeconfig
.phase7-${AFD_PLS_E2E_RUN_ID}/hub.yaml
.phase7-${AFD_PLS_E2E_RUN_ID}/member-1.yaml
.phase7-${AFD_PLS_E2E_RUN_ID}/member-2.yaml
.phase7-${AFD_PLS_E2E_RUN_ID}/gateway-resources.yaml
```

Generated paths are always recomputed from the current run ID. This prevents a new run in the same
shell from accidentally reusing an earlier run's state or kubeconfig path.

The state inventory records ACR/image digests, identities, federated credentials, role assignments,
AKS-created assignments, resource IDs, and RGs. It contains no bearer tokens.

The hub and member chart renders use reduced 25m controller CPU requests in this single-node
validation topology so rolling updates fit beside AKS system add-ons; default production chart
requests are unchanged.

If a literal subsection fails, preserve all resources, inspect the failed command and Section 8,
then resume at that subsection's **READ ONLY** block. Do not restart from Stage 01.

### Recovery for retained run `p7-10082105`

The previous monolithic rerun called `az network vnet create` on an existing VNet. Azure treated
the supplied single hub subnet as desired VNet state and attempted to remove `member-1`, which was
attached to a Kubernetes internal load balancer. Azure correctly returned
`InUseSubnetCannotBeDeleted`. **Do not delete or detach that subnet.**

After exporting the fixed subscription, run ID, location, and approval variables, resume at
Section 6.2's **READ ONLY** block. It validates the retained VNet and every existing subnet before
the conditional mutation block creates only genuinely absent subnets. It never updates/deletes an
in-use subnet. Any prefix, policy, ownership, or tag conflict stops before subnet creation; inspect
the resource rather than changing or deleting it.

### Fleet registration limitation

This repository ships networking controllers, not Fleet's production hub/member registration
agents or their deployment chart (`cmd/` and `charts/` contain only networking managers).
Therefore setup cannot test the production core Fleet `MemberCluster` controller. It does reuse the
repository's existing E2E registration path: reserved namespaces and Azure-principal RoleBindings
from `examples/getting-started/charts/hub`, Azure refresh-token authentication from the member
chart, and `InternalMemberCluster` join requests observed by the real networking agents. Setup
waits for `ServiceExportImportAgent/Joined=True` and a heartbeat before setting the aggregate
selector-facing `MemberCluster/Joined=True` condition. Only that aggregate condition remains a
validation-scoped substitute.

Authentication also follows the existing E2E split:

- the hub Gateway controller uses its dedicated federated workload identity; the cloud config sets
  the compatibility `useManagedIdentityExtension=true` flag required by Fleet v0.14.0 validation,
  while the Azure SDK selects the injected federated credential first;
- each member controller and its pinned refresh-token sidecar use the member AKS kubelet managed
  identity through IMDS, matching `test/scripts/bootstrap.sh`; and
- the member AKS control-plane identities receive `Network Contributor` only on the exact test
  VNet so the AKS cloud provider can reconcile ILB and PLS subnets.

AFD creates managed private endpoints from a Microsoft-managed subscription that is distinct from
`AKS Fleet Development/Test`. The POC therefore sets PLS visibility to `"*"` but does not enable
PLS auto-approval. The member controller's optional requester-subscription allowlist is left empty;
approval still requires the exact active assignment, UID, generation, PLS ID, pending state, and
complete `fleet:<assignment-uid>:<request-token>` message. Production deployments may configure an
allowlist once their actual AFD-managed requester subscription is known and trusted.

## 7. Perform the human-run POC validation

There is intentionally no Go/Ginkgo runner and no `phase7-e2e-run` target. The operator performs
and witnesses every assertion below. Commands marked **MUTATING** stay inside the approved run
boundary. Run them in order and stop on any unexpected output.

Initialize names once in the validation shell:

```bash
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names
```

### 7.1 Verify contexts, controllers, and immutable images

**READ ONLY:**

```bash
for context in \
  "$AFD_PLS_E2E_HUB_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  echo "=== $context ==="
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" cluster-info
done
```

Expected: each block contains `Kubernetes control plane is running at`.

```bash
for context in \
  "$AFD_PLS_E2E_HUB_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" \
    -n fleet-system get pods -o json |
    jq -r '.items[].spec | ([.initContainers[]?.image] + [.containers[].image])[]'
done
```

Expected: every printed image ends in `@sha256:` followed by 64 hexadecimal characters; no tag-only
reference is present. The hub has `deployment/hub-gateway-controller-manager`; both members have
`deployment/member-net-controller-manager`.

### 7.2 Verify echo Services, internal LBs, and PLS resources

**READ ONLY:**

```bash
for context in "$AFD_PLS_E2E_MEMBER1_CONTEXT" "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" \
    -n afd-pls-e2e wait --for=condition=Available deployment/echo --timeout=10m
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" \
    -n afd-pls-e2e get service/echo \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}{"\n"}'
done
```

Expected: both waits print `deployment.apps/echo condition met`; both following lines are non-empty
private IP addresses.

```bash
for rg in "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  az resource list --resource-group "$rg" \
    --query "[?type=='Microsoft.Network/loadBalancers' || type=='Microsoft.Network/privateLinkServices'].{name:name,type:type}" \
    -o table
done
```

Expected: each node RG lists at least one load balancer and one Private Link Service.

### 7.3 Verify selection, discovery, approval, and programmed status

**READ ONLY:**

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  get memberclusters -l networking.fleet.azure.com/afd-poc=true
```

Expected: exactly `afd-m1-${AFD_PLS_E2E_RUN_ID}` and `afd-m2-${AFD_PLS_E2E_RUN_ID}` are listed.

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  wait --for=condition=PrivateLinkApproved \
  serviceoriginassignments.networking.fleet.azure.com --all --all-namespaces --timeout=30m
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e wait --for=condition=Programmed gateway/global --timeout=30m
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e wait --for=condition=Programmed \
  multiclusterbackend.networking.fleet.azure.com/echo --timeout=30m
```

Expected: two assignment lines and both programmed resources print `condition met`.

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e get multiclusterbackend/echo \
  -o jsonpath='{.status.selectedClusters}/{.status.readyOrigins}{"\n"}'
```

Expected exact output: `2/2`.

Capture the first evidence snapshot (**READ ONLY except for the local JSONL append**):

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=programmed
```

Expected: `evidence appended to ... with label programmed`.

### 7.4 Verify normal traffic reaches both members

**READ ONLY data-plane requests:**

```bash
HOSTNAME="$(
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
    -n afd-pls-e2e get gateway/global -o jsonpath='{.status.addresses[0].value}'
)"
test -n "$HOSTNAME"
declare -A SEEN=()
for _ in $(seq 1 60); do
  BODY="$(curl --silent --show-error --max-time 30 "https://${HOSTNAME}/" | tr -d '\r\n')"
  printf '%s\n' "$BODY"
  SEEN["$BODY"]=1
  [[ -n "${SEEN[member-1]:-}" && -n "${SEEN[member-2]:-}" ]] && break
  sleep 5
done
printf 'member-1=%s member-2=%s\n' "${SEEN[member-1]:-0}" "${SEEN[member-2]:-0}"
```

Expected final output: `member-1=1 member-2=1`. Any other response body is a failure.

### 7.5 Verify the deterministic WAF rule

**READ ONLY data-plane requests:**

```bash
curl --silent --show-error --max-time 30 --output /dev/null \
  --write-out '%{http_code}\n' "https://${HOSTNAME}/"
curl --silent --show-error --max-time 30 --output /dev/null \
  --header 'X-POC-Block: true' --write-out '%{http_code}\n' "https://${HOSTNAME}/"
```

Expected exact outputs, in order: `200` and `403`. Merely observing a WAF association is not
sufficient.

### 7.6 Withdraw member 2 and verify uninterrupted traffic

**MUTATING — removes one test label:**

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  label membercluster "$AFD_PLS_E2E_MEMBER2_CLUSTER" \
  networking.fleet.azure.com/afd-poc-
```

Expected: `membercluster.cluster.kubernetes-fleet.io/... unlabeled`.

**READ ONLY wait and traffic check:**

```bash
for _ in $(seq 1 60); do
  VALUE="$(
    kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
      -n afd-pls-e2e get multiclusterbackend/echo \
      -o jsonpath='{.status.selectedClusters}/{.status.readyOrigins}'
  )"
  echo "$VALUE"
  [[ "$VALUE" == "1/1" ]] && break
  sleep 15
done
test "$VALUE" = "1/1"
curl --silent --show-error --max-time 30 --output /dev/null \
  --write-out '%{http_code}\n' "https://${HOSTNAME}/"
```

Expected: status converges to exact `1/1`; curl prints `200`.

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=member-withdrawn
```

### 7.7 Verify fail-static traffic during a hub-controller outage

**MUTATING — temporarily scales the test controller to zero:**

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system scale deployment/hub-gateway-controller-manager --replicas=0
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system wait --for=delete pod -l app=hub-gateway-controller-manager --timeout=5m
```

Expected: scale reports `scaled`; wait reports the pod condition met.

**READ ONLY traffic check:**

```bash
curl --silent --show-error --max-time 30 --output /dev/null \
  --write-out '%{http_code}\n' "https://${HOSTNAME}/"
```

Expected exact output: `200`.

Always restore the controller (**MUTATING**):

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system scale deployment/hub-gateway-controller-manager --replicas=1
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system rollout status deployment/hub-gateway-controller-manager --timeout=5m
```

Expected: `scaled`, then `successfully rolled out`.

### 7.8 Verify Gateway deletion ownership

Capture pre-delete evidence:

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=before-gateway-delete
```

**MUTATING — deletes the test Gateway and its controller-owned AFD graph:**

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e delete gateway/global --wait=true --timeout=20m
```

Expected: `gateway.gateway.networking.k8s.io "global" deleted`.

**READ ONLY — wait for and verify ownership boundaries:**

```bash
for _ in $(seq 1 60); do
  CDN_COUNT="$(
    az resource list --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" \
      --query "[?contains(type, 'Microsoft.Cdn')] | length(@)" -o tsv
  )"
  echo "$CDN_COUNT"
  [[ "$CDN_COUNT" == "0" ]] && break
  sleep 20
done
test "$CDN_COUNT" = "0"

az resource list --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --resource-type Microsoft.Network/frontdoorWebApplicationFirewallPolicies \
  --query 'length(@)' -o tsv

for context in "$AFD_PLS_E2E_MEMBER1_CONTEXT" "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" \
    -n afd-pls-e2e get service/echo -o name
done

for rg in "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  az resource list --resource-group "$rg" \
    --query "[?type=='Microsoft.Network/loadBalancers'] | length(@)" -o tsv
  az resource list --resource-group "$rg" \
    --query "[?type=='Microsoft.Network/privateLinkServices'] | length(@)" -o tsv
done
```

Expected: CDN count reaches exact `0`; WAF count is exact `1`; both Service commands print
`service/echo`; every node-RG count is at least `1`.

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=after-gateway-delete
```

Snapshots are appended to:

```text
.phase7-${AFD_PLS_E2E_RUN_ID}.results.jsonl
```

Phase 7 is still incomplete until a human has witnessed every expected result, retained this
evidence, run cleanup, and verified deletion.

## 8. Monitor and debug

The following are **READ ONLY**. Initialize deterministic names in each new shell:

```bash
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names
```

Inspect inventory without displaying Kubernetes Secrets:

```bash
jq '{runId,resourceGroup,images,identities,roleAssignments,discoveredRoleAssignments,resources}' \
  "$AFD_PLS_E2E_STATE_FILE"
```

Inspect controllers and workloads:

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system get pods
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system logs deployment/hub-gateway-controller-manager --tail=200
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  -n fleet-system logs deployment/member-net-controller-manager --tail=200
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_MEMBER2_CONTEXT" \
  -n fleet-system logs deployment/member-net-controller-manager --tail=200
```

Inspect API status and Azure resources:

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e get gateway,multiclusterbackend,serviceoriginassignment -o wide
az resource list --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" -o table
az resource list --resource-group "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" -o table
az resource list --resource-group "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP" -o table
tail -n 20 "$AFD_PLS_E2E_RESULTS_FILE" | jq .
```

Do not print controller token volumes, use `kubectl get secret ... -o yaml`, or enable shell
tracing.

## 9. Safe reruns and failure recovery

Resume at the failed literal subsection in Section 6, not at an automation wrapper. Re-source
`common.sh`, initialize names/state and `k`, then run that subsection's **READ ONLY** block before
its conditional **MUTATING** block. Exact resources must match deterministic names, tags, state,
topology, and scope. A conflicting existing resource is never replaced; inspect it and resolve the
ownership/configuration mismatch manually. Never delete an in-use subnet as recovery.

Common fixes:

- missing image state or ACR: resume at Section 6.1;
- missing/wrong subnet: resume at Section 6.2 only when absent; a wrong existing prefix/policy requires
  investigation, not an update;
- missing AKS cluster: resume at Section 6.3; an existing topology mismatch is a hard stop;
- missing identity/context: resume at Section 6.4;
- missing CRD/MemberCluster: resume at Section 6.5;
- stale/missing manifests: resume at Section 6.6, then Section 6.7;
- controller timeout: inspect pod events/logs, then resume at Section 6.7.

Billable resources remain active until validation succeeds or you explicitly run:

```bash
# MUTATING / DESTRUCTIVE, but bounded to exact validated run resources
make phase7-e2e-cleanup
```

Never manually broaden the cleanup query, remove the approval guard, rename state entries, or use
subscription-wide deletion.

## 10. Bounded cleanup

Run cleanup after success or failure.

**MUTATING / DESTRUCTIVE:**

```bash
make phase7-e2e-cleanup
```

Cleanup validates subscription, state run ID, deterministic names, RG tags, and assignment IDs,
and then deletes:

- the exact run-recorded built-in role assignments;
- primary RG `fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}`; and
- the three exact tagged node RGs.

It waits for RG deletion, then removes the generated kubeconfig, manifest directory, and state
inventory. It intentionally preserves the JSONL results file.

## 11. Verify cleanup

**READ ONLY:**

```bash
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names

for rg in \
  "$AFD_PLS_E2E_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  printf '%s: ' "$rg"
  az group exists --name "$rg"
done

```

Every RG query must print `false`. ACR, images, identities, and scoped role assignments are inside
the deleted resource groups or are explicitly removed from the state inventory during cleanup.

## 12. Unset the run environment

Keep the results file if evidence is required, then clear all run variables:

```bash
unset AZURE_SUBSCRIPTION_ID
while IFS= read -r name; do unset "$name"; done < <(compgen -A variable AFD_PLS_E2E_)
```

Starting a new shell is an equivalent way to clear exported values.

## Appendix A. Optional automation/reference

The literal commands in Section 6 are authoritative. The following guarded entry points mirror
that workflow for maintainers and unattended fresh runs, but they hide individual mutations behind
scripts and must not be used as the primary operator instructions:

```bash
make phase7-e2e-step-01-registry-images
make phase7-e2e-step-02-network
make phase7-e2e-step-03-aks
make phase7-e2e-step-04-identities-rbac-kubeconfig
make phase7-e2e-step-05-crds-registration
make phase7-e2e-step-06-waf-manifests
make phase7-e2e-step-07-deploy-join
make phase7-e2e-setup
```

The corresponding direct scripts are under `test/e2e/afdprivatelink/scripts/step-*.sh`; invoking
them is also secondary automation. For debugging or retained-resource recovery, use the failed
literal subsection instead.
