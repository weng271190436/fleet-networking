#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

initialize_names
require_env AZURE_SUBSCRIPTION_ID
[[ "${AZURE_SUBSCRIPTION_ID}" == "${EXPECTED_SUBSCRIPTION_ID}" ]] || {
    echo "error: AZURE_SUBSCRIPTION_ID must be ${EXPECTED_SUBSCRIPTION_ID}" >&2
    exit 1
}
for tool in az jq kubectl; do
    require_command "${tool}"
done
validate_subscription
require_primary_resource_group
require_state_fields '.images | length == 5' "immutable image state is missing"
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

for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    ready_nodes="$(k "${context}" get nodes -o json |
        jq '[.items[] | select(any(.status.conditions[]?;
            .type == "Ready" and .status == "True"))] | length')"
    [[ "${ready_nodes}" -ge 1 ]] || {
        echo "error: ${context} has no Ready node" >&2
        exit 1
    }
done

k "${AFD_PLS_E2E_HUB_CONTEXT}" -n fleet-system wait --for=condition=Available \
    deployment/hub-gateway-controller-manager --timeout=1m
for context in "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" -n fleet-system wait --for=condition=Available \
        deployment/member-net-controller-manager --timeout=1m
done

for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    images="$(k "${context}" -n fleet-system get pods -o json |
        jq -r '.items[].spec |
            ([.initContainers[]?.image] + [.containers[].image])[]' |
        grep -E '(hub-gateway-controller-manager|member-net-controller-manager|net-crd-installer|refresh-token)')"
    [[ -n "${images}" ]] || {
        echo "error: ${context} has no expected controller images" >&2
        exit 1
    }
    while IFS= read -r image; do
        [[ "${image}" =~ @sha256:[[:xdigit:]]{64}$ ]] || {
            echo "error: ${context} uses a non-immutable controller image: ${image}" >&2
            exit 1
        }
    done <<<"${images}"
done

for member in "${AFD_PLS_E2E_MEMBER1_CLUSTER}" "${AFD_PLS_E2E_MEMBER2_CLUSTER}"; do
    k "${AFD_PLS_E2E_HUB_CONTEXT}" -n "fleet-member-${member}" get \
        internalmembercluster "${member}" -o json | jq -e '
            any(.status.agentStatus[]?;
                .type == "ServiceExportImportAgent" and
                .lastReceivedHeartbeat != null and
                any(.conditions[]?; .type == "Joined" and .status == "True"))
        ' >/dev/null
    k "${AFD_PLS_E2E_HUB_CONTEXT}" get membercluster "${member}" -o json | jq -e '
        any(.status.conditions[]?; .type == "Joined" and .status == "True")
    ' >/dev/null
done

[[ "$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get gatewayclass azure-fleet-afd \
    -o jsonpath='{.status.conditions[?(@.type=="Accepted")].status}')" == "True" ]]
k "${AFD_PLS_E2E_HUB_CONTEXT}" get crd \
    multiclusterbackends.networking.fleet.azure.com \
    serviceoriginassignments.networking.fleet.azure.com >/dev/null

[[ "$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get gateways -A --no-headers 2>/dev/null |
    wc -l)" -eq 0 ]]
[[ "$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get httproutes -A --no-headers 2>/dev/null |
    wc -l)" -eq 0 ]]
[[ "$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get multiclusterbackends -A \
    --no-headers 2>/dev/null | wc -l)" -eq 0 ]]
[[ "$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get serviceoriginassignments -A \
    --no-headers 2>/dev/null | wc -l)" -eq 0 ]]
for context in "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    [[ "$(k "${context}" get services -A --no-headers |
        awk '$1 == "afd-pls-e2e" {count++} END {print count+0}')" -eq 0 ]]
    [[ "$(k "${context}" get deployments -A --no-headers |
        awk '$1 == "afd-pls-e2e" {count++} END {print count+0}')" -eq 0 ]]
done

[[ "$(az afd profile list --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --query 'length(@)' -o tsv)" == "0" ]]
[[ "$(az network front-door waf-policy list --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --query 'length(@)' -o tsv)" == "0" ]]
for resource_group in "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"; do
    [[ "$(az network private-link-service list --resource-group "${resource_group}" \
        --query 'length(@)' -o tsv)" == "0" ]]
done

echo "Part 1 platform verification passed for ${AFD_PLS_E2E_RUN_ID}."
