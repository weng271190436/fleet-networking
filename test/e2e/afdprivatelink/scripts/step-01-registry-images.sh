#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq docker make
install_stage_failure_trap step-01-registry-images
docker info >/dev/null
docker buildx version >/dev/null
bash "${SCRIPT_DIR}/preflight.sh"

tags=("source=${SOURCE_TAG}" "phase=7" "run-id=${AFD_PLS_E2E_RUN_ID}")
if az group show --name "${AFD_PLS_E2E_RESOURCE_GROUP}" --output none 2>/dev/null; then
    validate_resource_group_boundary
else
    az group create --name "${AFD_PLS_E2E_RESOURCE_GROUP}" --location "${AFD_PLS_E2E_LOCATION}" \
        --tags "${tags[@]}" --output none
fi
resource_group_id="/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_RESOURCE_GROUP}"
record_resource resourceGroup "${AFD_PLS_E2E_RESOURCE_GROUP}" "${resource_group_id}"

if az acr show --name "${AFD_PLS_E2E_ACR}" --output none 2>/dev/null; then
    acr_json="$(az acr show --name "${AFD_PLS_E2E_ACR}" -o json)"
    validate_resource_tags "$(jq -r '.tags.source // ""' <<<"${acr_json}")" \
        "$(jq -r '.tags["run-id"] // ""' <<<"${acr_json}")" "ACR ${AFD_PLS_E2E_ACR}"
    [[ "$(jq -r '.resourceGroup' <<<"${acr_json}")" == "${AFD_PLS_E2E_RESOURCE_GROUP}" &&
        "$(jq -r '.sku.name' <<<"${acr_json}")" == "Basic" ]] || {
        echo "error: existing ACR resource group or SKU does not match the run plan" >&2
        exit 1
    }
else
    az acr create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${AFD_PLS_E2E_ACR}" \
        --location "${AFD_PLS_E2E_LOCATION}" --sku Basic --admin-enabled false \
        --tags "${tags[@]}" --output none
fi
acr_id="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query id -o tsv)"
acr_login_server="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query loginServer -o tsv)"
record_resource containerRegistry "${AFD_PLS_E2E_ACR}" "${acr_id}"
az acr login --name "${AFD_PLS_E2E_ACR}" --output none

cd "${REPO_ROOT}"
make REGISTRY="${acr_login_server}" TAG="${AFD_PLS_E2E_RUN_ID}" OUTPUT_TYPE=type=registry \
    docker-build-hub-gateway-controller-manager docker-build-member-net-controller-manager \
    docker-build-net-crd-installer docker-build-afd-pls-echo

resolve_image() {
    local repository="$1" digest
    digest="$(az acr repository show --name "${AFD_PLS_E2E_ACR}" \
        --image "${repository}:${AFD_PLS_E2E_RUN_ID}" --query digest -o tsv)"
    [[ "${digest}" =~ ^sha256:[[:xdigit:]]{64}$ ]] || {
        echo "error: registry returned invalid digest for ${repository}" >&2
        return 1
    }
    printf '%s/%s@%s' "${acr_login_server}" "${repository}" "${digest}"
}

hub_image="$(resolve_image hub-gateway-controller-manager)"
member_image="$(resolve_image member-net-controller-manager)"
crd_image="$(resolve_image net-crd-installer)"
echo_image="$(resolve_image afd-pls-echo)"
refresh_repo="ghcr.io/azure/fleet/refresh-token"
refresh_digest="$(docker buildx imagetools inspect "${refresh_repo}:v0.1.0" \
    --format '{{json .Manifest.Digest}}' | tr -d '"')"
[[ "${refresh_digest}" =~ ^sha256:[[:xdigit:]]{64}$ ]] || {
    echo "error: refresh-token image returned invalid digest" >&2
    exit 1
}
next="${AFD_PLS_E2E_STATE_FILE}.next"
jq --arg hub "${hub_image}" --arg member "${member_image}" --arg crd "${crd_image}" \
    --arg echo "${echo_image}" --arg refresh "${refresh_repo}@${refresh_digest}" \
    '.images = {hubGateway:$hub, member:$member, crdInstaller:$crd, echo:$echo,
      refreshToken:$refresh}' "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"

trap - EXIT INT TERM
complete_stage step-01-registry-images "make phase7-e2e-step-02-network"
