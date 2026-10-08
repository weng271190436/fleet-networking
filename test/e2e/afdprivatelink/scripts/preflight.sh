#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

initialize_names
require_env AZURE_SUBSCRIPTION_ID
[[ "${AZURE_SUBSCRIPTION_ID}" == "${EXPECTED_SUBSCRIPTION_ID}" ]] || {
    echo "error: AZURE_SUBSCRIPTION_ID must be ${EXPECTED_SUBSCRIPTION_ID}" >&2
    exit 1
}

for tool in az jq kubectl helm go git curl docker make; do
    require_command "${tool}"
done
docker info >/dev/null
docker buildx version >/dev/null

build_inputs=(
    docker/hub-gateway-controller-manager.Dockerfile
    docker/member-net-controller-manager.Dockerfile
    docker/net-crd-installer.Dockerfile
    test/e2e/afdprivatelink/echo/Dockerfile
    test/e2e/afdprivatelink/echo/entrypoint.sh
    charts/hub-gateway-controller-manager/Chart.yaml
    charts/member-net-controller-manager/Chart.yaml
)
for input in "${build_inputs[@]}"; do
    [[ -f "${REPO_ROOT}/${input}" ]] || {
        echo "error: required build input ${input} is missing" >&2
        exit 1
    }
done
grep -Fq 'cmd/hub-gateway-controller-manager/main.go' \
    "${REPO_ROOT}/docker/hub-gateway-controller-manager.Dockerfile"
grep -Fq 'MEMBER_NAME' "${REPO_ROOT}/test/e2e/afdprivatelink/echo/entrypoint.sh"
HELM_NO_PLUGINS=1 helm lint "${REPO_ROOT}/charts/hub-gateway-controller-manager" \
    --set image.digest="sha256:$(printf '0%.0s' {1..64})" \
    --set crdInstaller.image.digest="sha256:$(printf '1%.0s' {1..64})" \
    --set azure.clientId=00000000-0000-0000-0000-000000000000 \
    --set azure.location=eastus2 >/dev/null
HELM_NO_PLUGINS=1 helm lint "${REPO_ROOT}/charts/member-net-controller-manager" \
    --set fullnameOverride=member-net-controller-manager \
    --set image.digest="sha256:$(printf '2%.0s' {1..64})" \
    --set crdInstaller.enabled=true \
    --set crdInstaller.image.digest="sha256:$(printf '3%.0s' {1..64})" \
    --set config.provider=azure \
    --set refreshtoken.digest="sha256:$(printf '4%.0s' {1..64})" \
    --set azure.clientid=00000000-0000-0000-0000-000000000000 >/dev/null
HELM_NO_PLUGINS=1 helm template phase7-registration "${REPO_ROOT}/examples/getting-started/charts/hub" \
    --namespace fleet-system --set-string userNS=afd-pls-e2e \
    --set-string 'memberClusterConfigs[0].memberID=member-1' \
    --set-string 'memberClusterConfigs[0].principalID=00000000-0000-0000-0000-000000000001' \
    --set-string 'memberClusterConfigs[1].memberID=member-2' \
    --set-string 'memberClusterConfigs[1].principalID=00000000-0000-0000-0000-000000000002' \
    >/dev/null
docker buildx imagetools inspect ghcr.io/azure/fleet/refresh-token:v0.1.0 >/dev/null
module_cache="$(go env GOMODCACHE)"
for crd_input in \
    "${module_cache}/go.goms.io/fleet@v0.14.0/config/crd/bases/cluster.kubernetes-fleet.io_memberclusters.yaml" \
    "${module_cache}/go.goms.io/fleet@v0.14.0/config/crd/bases/cluster.kubernetes-fleet.io_internalmemberclusters.yaml" \
    "${module_cache}/sigs.k8s.io/gateway-api@v1.2.1/config/crd/standard/gateway.networking.k8s.io_gateways.yaml"; do
    [[ -f "${crd_input}" ]] || {
        echo "error: pinned CRD input ${crd_input} is missing; run go mod download" >&2
        exit 1
    }
done

validate_subscription
for resource_group in \
    "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"; do
    if az group show --name "${resource_group}" --output none 2>/dev/null; then
        validate_tagged_resource_group "${resource_group}"
    fi
done
[[ "$(az role definition list --name Contributor --query 'length(@)' -o tsv)" == "1" ]] || {
    echo "error: built-in Contributor role is unavailable" >&2
    exit 1
}
[[ "$(az role definition list --name 'Network Contributor' --query 'length(@)' -o tsv)" == "1" ]] || {
    echo "error: built-in Network Contributor role is unavailable" >&2
    exit 1
}
for provider in Microsoft.ContainerRegistry Microsoft.ContainerService Microsoft.Network Microsoft.Cdn Microsoft.ManagedIdentity; do
    state="$(az provider show --namespace "${provider}" --query registrationState -o tsv)"
    [[ "${state}" == "Registered" ]] || {
        echo "error: provider ${provider} is ${state}, not Registered" >&2
        exit 1
    }
done

acr_available="$(az acr check-name --name "${AFD_PLS_E2E_ACR}" --query nameAvailable -o tsv)"
if [[ "${acr_available}" != "true" ]]; then
    acr_id="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query id -o tsv 2>/dev/null || true)"
    acr_source="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query tags.source -o tsv 2>/dev/null || true)"
    acr_run="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query 'tags."run-id"' -o tsv 2>/dev/null || true)"
    expected_acr_id="/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_RESOURCE_GROUP}/providers/Microsoft.ContainerRegistry/registries/${AFD_PLS_E2E_ACR}"
    if [[ ! "${acr_id,,}" == "${expected_acr_id,,}" || "${acr_source}" != "${SOURCE_TAG}" || "${acr_run}" != "${AFD_PLS_E2E_RUN_ID}" ]]; then
        echo "error: deterministic ACR name ${AFD_PLS_E2E_ACR} is unavailable to this tagged run" >&2
        exit 1
    fi
fi

usage_json="$(az vm list-usage --location "${AFD_PLS_E2E_LOCATION}" \
    --query "[?name.value=='cores' || name.value=='standardDASv4Family'].{name:name.localizedValue,current:currentValue,limit:limit}" \
    -o json)"
printf '%s\n' "${usage_json}" | jq -r '"Name\tCurrent\tLimit", (.[] | [.name, .current, .limit] | @tsv)'
[[ "$(printf '%s\n' "${usage_json}" | jq 'length')" -eq 2 ]] || {
    echo "error: Azure did not return both regional and Standard DSv3 quota records" >&2
    exit 1
}
while IFS=$'\t' read -r quota_name current limit; do
    if (( limit - current < 6 )); then
        echo "error: ${quota_name} has fewer than 6 vCPUs available" >&2
        exit 1
    fi
done < <(printf '%s\n' "${usage_json}" | jq -r '.[] | [.name, .current, .limit] | @tsv')
afd_profile_count="$(az afd profile list --query 'length(@)' -o tsv)"

cd "${REPO_ROOT}"
branch="$(git branch --show-current)"
[[ "${branch}" == "poc/gateway-api-afd-private-link" ]] || {
    echo "error: expected branch poc/gateway-api-afd-private-link, found ${branch}" >&2
    exit 1
}
required_commit="37ca0ca9828d17b1b792e73be4bea207f9cbc36f"
git cat-file -e "${required_commit}^{commit}"
git merge-base --is-ancestor "${required_commit}" HEAD || {
    echo "error: branch does not contain required starting commit ${required_commit}" >&2
    exit 1
}
if [[ -n "$(git diff --name-only --diff-filter=U)" || -n "$(git diff --cached --name-only)" ]]; then
    echo "error: repository must have no conflicts or staged changes" >&2
    exit 1
fi

cat <<EOF
Phase 7 read-only preflight passed.
Subscription: ${EXPECTED_SUBSCRIPTION_NAME} (${EXPECTED_SUBSCRIPTION_ID})
Branch/ancestry: ${branch} contains ${required_commit}
Run ID: ${AFD_PLS_E2E_RUN_ID}
Region: ${AFD_PLS_E2E_LOCATION}
Billable plan:
  - tagged primary resource group (1): ${AFD_PLS_E2E_RESOURCE_GROUP}
  - Azure Container Registry Basic (1): ${AFD_PLS_E2E_ACR}
  - four image repositories/builds: hub-gateway-controller-manager, member-net-controller-manager,
    net-crd-installer, afd-pls-echo; setup records immutable digests and deploys only @sha256 refs
  - user-assigned managed identities (3) and federated credentials (3)
  - AKS-managed control-plane identities (3) and kubelet identities (3)
  - built-in RG-scoped role assignments (3) and AKS-created AcrPull assignments (3)
  - AKS clusters (3): ${AFD_PLS_E2E_HUB_CLUSTER}, ${AFD_PLS_E2E_MEMBER1_CLUSTER}, ${AFD_PLS_E2E_MEMBER2_CLUSTER}
  - AKS nodes (3 total): one Standard_D2as_v4 node per cluster
  - deterministic AKS node resource groups (3):
      ${AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP}
      ${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}
      ${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}
  - VNet (1), AKS subnets (3), PLS NAT subnets (2)
  - internal Standard load balancers (2, created by echo Services)
  - Private Link Services (2, created by echo Services)
  - Azure Front Door Premium profile (1), endpoint (1), origin group (1), origins (2), route (1)
  - Front Door WAF policy (1): ${AFD_PLS_E2E_WAF_POLICY}
Kubeconfig: ${AFD_PLS_E2E_KUBECONFIG}
Contexts: ${AFD_PLS_E2E_HUB_CONTEXT}, ${AFD_PLS_E2E_MEMBER1_CONTEXT}, ${AFD_PLS_E2E_MEMBER2_CONTEXT}
State inventory: ${AFD_PLS_E2E_STATE_FILE}
Cleanup target: only the exact tagged primary RG and three exact tagged AKS node RGs listed above
Mutation is disabled unless AFD_PLS_E2E_APPROVED=true.
Existing AFD profiles in subscription: ${afd_profile_count}
EOF
