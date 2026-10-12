#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq kubectl helm
install_stage_failure_trap step-06-platform-controllers
require_primary_resource_group
require_state_fields \
    '.images | length == 5 and all(.[]; test("@sha256:[0-9a-fA-F]{64}$"))' \
    "immutable image state is missing"
require_state_fields '.roleAssignments | length == 7' "expected role assignments are missing"
[[ -f "${AFD_PLS_E2E_KUBECONFIG}" ]] || {
    echo "error: kubeconfig is missing; run step 04" >&2
    exit 1
}

k() {
    local context="$1"
    shift
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" "$@"
}

for member in "${AFD_PLS_E2E_MEMBER1_CLUSTER}" "${AFD_PLS_E2E_MEMBER2_CLUSTER}"; do
    k "${AFD_PLS_E2E_HUB_CONTEXT}" get membercluster "${member}" >/dev/null || {
        echo "error: MemberCluster ${member} is missing; run step 05" >&2
        exit 1
    }
done
for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    ready_nodes="$(k "${context}" get nodes -o json |
        jq '[.items[] | select(any(.status.conditions[]?;
            .type == "Ready" and .status == "True"))] | length')"
    [[ "${ready_nodes}" -ge 1 ]] || {
        echo "error: ${context} has no Ready node; rerun the VMSS recovery in step 03" >&2
        exit 1
    }
done
webhook_endpoints="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" -n kube-system get \
    endpointslices.discovery.k8s.io \
    -l kubernetes.io/service-name=azure-wi-webhook-webhook-service -o json |
    jq '[.items[]?.endpoints[]? | select(.conditions.ready != false)] | length')"
[[ "${webhook_endpoints}" -ge 1 ]] || {
    echo "error: hub Azure Workload Identity webhook has no endpoint" >&2
    exit 1
}

hub_client="$(jq -r --arg name "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" \
    '.identities[] | select(.name == $name) | .clientId' "${AFD_PLS_E2E_STATE_FILE}")"
member_1_client="$(jq -r --arg name "${AFD_PLS_E2E_MEMBER1_CLUSTER}-kubelet" \
    '.identities[] | select(.name == $name) | .clientId' "${AFD_PLS_E2E_STATE_FILE}")"
member_2_client="$(jq -r --arg name "${AFD_PLS_E2E_MEMBER2_CLUSTER}-kubelet" \
    '.identities[] | select(.name == $name) | .clientId' "${AFD_PLS_E2E_STATE_FILE}")"
[[ -n "${hub_client}" && -n "${member_1_client}" && -n "${member_2_client}" ]] || {
    echo "error: controller client ID state is incomplete; rerun step 04" >&2
    exit 1
}

tenant_id="$(az account show --query tenantId -o tsv)"
hub_server="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" config view --raw --minify \
    -o jsonpath='{.clusters[0].cluster.server}')"
hub_ca="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" config view --raw --minify \
    -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
hub_image="$(jq -r '.images.hubGateway' "${AFD_PLS_E2E_STATE_FILE}")"
member_image="$(jq -r '.images.member' "${AFD_PLS_E2E_STATE_FILE}")"
crd_image="$(jq -r '.images.crdInstaller' "${AFD_PLS_E2E_STATE_FILE}")"
refresh_image="$(jq -r '.images.refreshToken' "${AFD_PLS_E2E_STATE_FILE}")"
hub_repo="${hub_image%@*}"; hub_digest="${hub_image##*@}"
member_repo="${member_image%@*}"; member_digest="${member_image##*@}"
crd_repo="${crd_image%@*}"; crd_digest="${crd_image##*@}"
refresh_repo="${refresh_image%@*}"; refresh_digest="${refresh_image##*@}"

HELM_NO_PLUGINS=1 helm template phase7 "${REPO_ROOT}/charts/hub-gateway-controller-manager" \
    --namespace fleet-system --set-string image.repository="${hub_repo}" \
    --set-string image.digest="${hub_digest}" \
    --set-string crdInstaller.image.repository="${crd_repo}" \
    --set-string crdInstaller.image.digest="${crd_digest}" \
    --set-string azure.clientId="${hub_client}" \
    --set-string azure.tenantId="${tenant_id}" \
    --set-string azure.subscriptionId="${EXPECTED_SUBSCRIPTION_ID}" \
    --set-string azure.resourceGroup="${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --set-string resources.requests.cpu=25m \
    --set-string azure.location="${AFD_PLS_E2E_LOCATION}" >"${AFD_PLS_E2E_HUB_MANIFEST}"

render_member() {
    local manifest="$1" member_name="$2" client_id="$3" node_rg="$4"
    HELM_NO_PLUGINS=1 helm template phase7 "${REPO_ROOT}/charts/member-net-controller-manager" \
        --namespace fleet-system --set-string fullnameOverride=member-net-controller-manager \
        --set-string image.repository="${member_repo}" \
        --set-string image.digest="${member_digest}" \
        --set crdInstaller.enabled=true \
        --set-string crdInstaller.image.repository="${crd_repo}" \
        --set-string crdInstaller.image.digest="${crd_digest}" \
        --set crdInstaller.isE2ETest=true \
        --set-string config.hubURL="${hub_server}" \
        --set-string config.hubCA="${hub_ca}" \
        --set-string config.memberClusterName="${member_name}" \
        --set-string config.provider=azure \
        --set-string refreshtoken.repository="${refresh_repo}" \
        --set-string refreshtoken.digest="${refresh_digest}" \
        --set-string resources.requests.cpu=25m \
        --set-string resources.requests.memory=64Mi \
        --set tlsClientInsecure=false \
        --set-string azure.clientid="${client_id}" \
        --set azure.workloadIdentityEnabled=false \
        --set enableTrafficManagerFeature=false \
        --set enableAFDPrivateLinkFeature=true \
        --set-string azureCloudConfig.tenantId="${tenant_id}" \
        --set-string azureCloudConfig.subscriptionId="${EXPECTED_SUBSCRIPTION_ID}" \
        --set-string azureCloudConfig.aadClientId="${client_id}" \
        --set azureCloudConfig.useManagedIdentityExtension=true \
        --set-string azureCloudConfig.userAssignedIdentityID="${client_id}" \
        --set azureCloudConfig.cloudProviderRateLimit=false \
        --set-string azureCloudConfig.resourceGroup="${node_rg}" \
        --set-string azureCloudConfig.location="${AFD_PLS_E2E_LOCATION}" >"${manifest}"
}
render_member "${AFD_PLS_E2E_MEMBER1_MANIFEST}" "${AFD_PLS_E2E_MEMBER1_CLUSTER}" \
    "${member_1_client}" "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}"
render_member "${AFD_PLS_E2E_MEMBER2_MANIFEST}" "${AFD_PLS_E2E_MEMBER2_CLUSTER}" \
    "${member_2_client}" "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"

for manifest in "${AFD_PLS_E2E_HUB_MANIFEST}" "${AFD_PLS_E2E_MEMBER1_MANIFEST}" \
    "${AFD_PLS_E2E_MEMBER2_MANIFEST}"; do
    [[ -s "${manifest}" ]] || {
        echo "error: rendered controller manifest ${manifest} is empty" >&2
        exit 1
    }
    grep -Eq '^[[:space:]]*image:.*@sha256:[[:xdigit:]]{64}"?[[:space:]]*$' "${manifest}" || {
        echo "error: rendered controller manifest ${manifest} has no immutable image" >&2
        exit 1
    }
    ! grep -E '^[[:space:]]*image:' "${manifest}" |
        grep -Ev '@sha256:[[:xdigit:]]{64}"?[[:space:]]*$'
    ! grep -Eq \
        '^[[:space:]]*kind: (Service|Gateway|HTTPRoute|MultiClusterBackend)[[:space:]]*$' \
        "${manifest}"
done

k "${AFD_PLS_E2E_HUB_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${AFD_PLS_E2E_HUB_MANIFEST}"
k "${AFD_PLS_E2E_MEMBER1_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${AFD_PLS_E2E_MEMBER1_MANIFEST}"
k "${AFD_PLS_E2E_MEMBER2_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${AFD_PLS_E2E_MEMBER2_MANIFEST}"

k "${AFD_PLS_E2E_HUB_CONTEXT}" -n fleet-system wait --for=condition=Available \
    deployment/hub-gateway-controller-manager --timeout=20m
for context in "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" -n fleet-system wait --for=condition=Available \
        deployment/member-net-controller-manager --timeout=20m
done
k "${AFD_PLS_E2E_HUB_CONTEXT}" wait --for=condition=Established \
    crd/multiclusterbackends.networking.fleet.azure.com \
    crd/serviceoriginassignments.networking.fleet.azure.com --timeout=5m

trap - EXIT INT TERM
complete_stage step-06-platform-controllers "make phase7-e2e-step-07-platform-join"
