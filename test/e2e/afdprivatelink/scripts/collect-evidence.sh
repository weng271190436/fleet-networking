#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

initialize_names
require_env AZURE_SUBSCRIPTION_ID
for tool in az jq kubectl curl; do
    require_command "${tool}"
done
validate_subscription

label="${1:-snapshot}"
[[ "${label}" =~ ^[a-z0-9][a-z0-9-]{0,30}$ ]] || {
    echo "error: evidence label must contain only lowercase letters, digits, and hyphens" >&2
    exit 1
}
[[ -f "${AFD_PLS_E2E_STATE_FILE}" && -f "${AFD_PLS_E2E_KUBECONFIG}" ]] || {
    echo "error: setup state and kubeconfig are required" >&2
    exit 1
}
jq -e --arg subscriptionId "${EXPECTED_SUBSCRIPTION_ID}" --arg runId "${AFD_PLS_E2E_RUN_ID}" \
    '.subscriptionId == $subscriptionId and .runId == $runId' \
    "${AFD_PLS_E2E_STATE_FILE}" >/dev/null

k() {
    local context="$1"
    shift
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" "$@"
}

members="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get memberclusters -o json)"
gateways="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get gateways.gateway.networking.k8s.io -A -o json)"
backends="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get multiclusterbackends.networking.fleet.azure.com -A -o json)"
assignments="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" get serviceoriginassignments.networking.fleet.azure.com -A -o json)"
member1_service="$(k "${AFD_PLS_E2E_MEMBER1_CONTEXT}" -n afd-pls-e2e get service echo -o json)"
member2_service="$(k "${AFD_PLS_E2E_MEMBER2_CONTEXT}" -n afd-pls-e2e get service echo -o json)"
primary_resources="$(az resource list --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" -o json)"
member1_resources="$(az resource list --resource-group "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" -o json)"
member2_resources="$(az resource list --resource-group "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}" -o json)"
hostname="$(jq -r '.items[0].status.addresses[0].value // empty' <<<"${gateways}")"

http_evidence='null'
if [[ -n "${hostname}" ]]; then
    normal="$(curl --silent --show-error --max-time 30 --write-out $'\n%{http_code}' "https://${hostname}/")"
    blocked="$(curl --silent --show-error --max-time 30 --header 'X-POC-Block: true' \
        --write-out $'\n%{http_code}' "https://${hostname}/")"
    http_evidence="$(jq -n --arg normal "${normal}" --arg blocked "${blocked}" \
        '{normal: $normal, blocked: $blocked}')"
fi

umask 077
jq -cn \
    --arg time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg label "${label}" \
    --argjson members "${members}" \
    --argjson gateways "${gateways}" \
    --argjson backends "${backends}" \
    --argjson assignments "${assignments}" \
    --argjson member1Service "${member1_service}" \
    --argjson member2Service "${member2_service}" \
    --argjson primaryResources "${primary_resources}" \
    --argjson member1Resources "${member1_resources}" \
    --argjson member2Resources "${member2_resources}" \
    --argjson http "${http_evidence}" \
    '{time: $time, label: $label, members: $members, gateways: $gateways, backends: $backends,
      assignments: $assignments, services: {member1: $member1Service, member2: $member2Service},
      azure: {primary: $primaryResources, member1NodeRG: $member1Resources, member2NodeRG: $member2Resources},
      http: $http}' >>"${AFD_PLS_E2E_RESULTS_FILE}"

echo "evidence appended to ${AFD_PLS_E2E_RESULTS_FILE} with label ${label}"
