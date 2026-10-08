#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

initialize_names
require_mutation_approval
for tool in az jq; do
    require_command "${tool}"
done
validate_subscription

if [[ -f "${AFD_PLS_E2E_STATE_FILE}" ]]; then
    [[ "$(jq -r '.subscriptionId' "${AFD_PLS_E2E_STATE_FILE}")" == "${EXPECTED_SUBSCRIPTION_ID}" ]] || {
        echo "error: state subscription does not match" >&2
        exit 1
    }
    [[ "$(jq -r '.runId' "${AFD_PLS_E2E_STATE_FILE}")" == "${AFD_PLS_E2E_RUN_ID}" ]] || {
        echo "error: state run ID does not match" >&2
        exit 1
    }
    [[ "$(jq -r '.resourceGroup' "${AFD_PLS_E2E_STATE_FILE}")" == "${AFD_PLS_E2E_RESOURCE_GROUP}" ]] || {
        echo "error: state resource group does not match" >&2
        exit 1
    }

    while IFS= read -r assignment_id; do
        normalized_assignment_id="${assignment_id,,}"
        subscription_prefix="/subscriptions/${EXPECTED_SUBSCRIPTION_ID,,}"
        [[ "${normalized_assignment_id}" == "${subscription_prefix}/providers/microsoft.authorization/roleassignments/"* ||
            "${normalized_assignment_id}" == "${subscription_prefix}/resourcegroups/${AFD_PLS_E2E_RESOURCE_GROUP,,}/providers/microsoft.authorization/roleassignments/"* ||
            "${normalized_assignment_id}" == "${subscription_prefix}/resourcegroups/${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP,,}/providers/microsoft.authorization/roleassignments/"* ||
            "${normalized_assignment_id}" == "${subscription_prefix}/resourcegroups/${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP,,}/providers/microsoft.authorization/roleassignments/"* ]] || {
            echo "error: refusing unexpected role assignment ID ${assignment_id}" >&2
            exit 1
        }
        az role assignment delete --ids "${assignment_id}" 2>/dev/null || true
    done < <(jq -r '.roleAssignments[]?.id' "${AFD_PLS_E2E_STATE_FILE}")
fi

if ! az group show --name "${AFD_PLS_E2E_RESOURCE_GROUP}" --output none 2>/dev/null; then
    echo "primary deterministic resource group is already absent"
else
    validate_resource_group_boundary
    az group delete --name "${AFD_PLS_E2E_RESOURCE_GROUP}" --yes
fi

for node_resource_group in \
    "${AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"; do
    if az group show --name "${node_resource_group}" --output none 2>/dev/null; then
        validate_tagged_resource_group "${node_resource_group}"
        az group delete --name "${node_resource_group}" --yes
    fi
done
rm -f "${AFD_PLS_E2E_KUBECONFIG}"
rm -rf "${AFD_PLS_E2E_ARTIFACT_DIR}"
echo "cleanup complete for the exact tagged primary and AKS node resource groups"
rm -f "${AFD_PLS_E2E_STATE_FILE}"
