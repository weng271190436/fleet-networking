#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq docker make kubectl helm
install_stage_failure_trap redeploy-controller
require_primary_resource_group
require_state_fields '.images | length == 5' "immutable image state is missing"
require_env CONTROLLER
[[ -f "${AFD_PLS_E2E_KUBECONFIG}" ]] || {
    echo "error: kubeconfig is missing; Part 1 is not ready" >&2
    exit 1
}

case "${CONTROLLER}" in
hub)
    repository=hub-gateway-controller-manager
    state_key=hubGateway
    build_target=docker-build-hub-gateway-controller-manager
    ;;
member)
    repository=member-net-controller-manager
    state_key=member
    build_target=docker-build-member-net-controller-manager
    ;;
*)
    echo "error: CONTROLLER must be hub or member" >&2
    exit 1
    ;;
esac

docker info >/dev/null
docker buildx version >/dev/null
acr_login_server="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query loginServer -o tsv)"
iteration_tag="${AFD_PLS_E2E_RUN_ID}-dev-$(date -u +%m%d%H%M%S)"
az acr login --name "${AFD_PLS_E2E_ACR}" --output none

cd "${REPO_ROOT}"
make REGISTRY="${acr_login_server}" TAG="${iteration_tag}" OUTPUT_TYPE=type=registry \
    "${build_target}"

digest="$(az acr repository show --name "${AFD_PLS_E2E_ACR}" \
    --image "${repository}:${iteration_tag}" --query digest -o tsv)"
[[ "${digest}" =~ ^sha256:[[:xdigit:]]{64}$ ]] || {
    echo "error: registry returned invalid digest for ${repository}" >&2
    exit 1
}
image="${acr_login_server}/${repository}@${digest}"
next="${AFD_PLS_E2E_STATE_FILE}.next"
jq --arg key "${state_key}" --arg image "${image}" \
    '.images[$key] = $image' "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"

bash "${SCRIPT_DIR}/step-06-platform-controllers.sh"

if [[ "${CONTROLLER}" == hub ]]; then
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" \
        --context "${AFD_PLS_E2E_HUB_CONTEXT}" -n fleet-system \
        rollout status deployment/hub-gateway-controller-manager --timeout=10m
else
    for context in "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
        kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" \
            --context "${context}" -n fleet-system \
            rollout status deployment/member-net-controller-manager --timeout=10m
    done
fi

trap - EXIT INT TERM
echo "Redeployed ${CONTROLLER} controller with ${image}"
