#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq kubectl
install_stage_failure_trap step-04-identities-rbac-kubeconfig
require_primary_resource_group
for cluster in "${AFD_PLS_E2E_HUB_CLUSTER}" "${AFD_PLS_E2E_MEMBER1_CLUSTER}" \
    "${AFD_PLS_E2E_MEMBER2_CLUSTER}"; do
    az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
        --output none 2>/dev/null || {
        echo "error: AKS cluster ${cluster} is missing; run step 03" >&2
        exit 1
    }
done
for node_rg in "${AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"; do
    az group show --name "${node_rg}" --output none 2>/dev/null || {
        echo "error: node resource group ${node_rg} is missing; rerun step 03" >&2
        exit 1
    }
    validate_tagged_resource_group "${node_rg}"
done
hub_role_id="$(az role definition list --name Contributor --query '[0].name' -o tsv)"
member_role_id="$(az role definition list --name 'Network Contributor' --query '[0].name' -o tsv)"
[[ -n "${hub_role_id}" && -n "${member_role_id}" ]] || {
    echo "error: required built-in Azure roles are unavailable" >&2
    exit 1
}

tags=("source=${SOURCE_TAG}" "phase=7" "run-id=${AFD_PLS_E2E_RUN_ID}")
tenant_id="$(az account show --query tenantId -o tsv)"
identity_specs=(
    "hub_gateway:afd-hub-id-${AFD_PLS_E2E_RUN_ID}"
    "member_1:afd-m1-id-${AFD_PLS_E2E_RUN_ID}"
    "member_2:afd-m2-id-${AFD_PLS_E2E_RUN_ID}"
)
# Validate all existing identities before creating any missing identity.
for spec in "${identity_specs[@]}"; do
    name="${spec#*:}"
    if json="$(az identity show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --name "${name}" -o json 2>/dev/null)"; then
        validate_resource_tags "$(jq -r '.tags.source // ""' <<<"${json}")" \
            "$(jq -r '.tags["run-id"] // ""' <<<"${json}")" "identity ${name}"
        [[ "$(jq -r '.location' <<<"${json}")" == "${AFD_PLS_E2E_LOCATION}" ]] || {
            echo "error: identity ${name} is in an unexpected location" >&2
            exit 1
        }
    fi
done
for spec in "${identity_specs[@]}"; do
    key="${spec%%:*}"
    name="${spec#*:}"
    if ! az identity show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --name "${name}" --output none 2>/dev/null; then
        az identity create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${name}" \
            --location "${AFD_PLS_E2E_LOCATION}" --tags "${tags[@]}" --output none
    fi
    json="$(az identity show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${name}" -o json)"
    client_id="$(jq -r '.clientId' <<<"${json}")"
    principal_id="$(jq -r '.principalId' <<<"${json}")"
    record_identity "${name}" "${client_id}" "${principal_id}" "$(jq -r '.id' <<<"${json}")"
    printf -v "${key}_client_id" '%s' "${client_id}"
    printf -v "${key}_principal_id" '%s' "${principal_id}"
done

create_or_validate_federation() {
    local cluster="$1" identity="$2" service_account="$3"
    local issuer name json id
    issuer="$(az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
        --query oidcIssuerProfile.issuerUrl -o tsv)"
    name="fleet-system-${AFD_PLS_E2E_RUN_ID}"
    if json="$(az identity federated-credential show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --identity-name "${identity}" --name "${name}" -o json 2>/dev/null)"; then
        [[ "$(jq -r '.issuer' <<<"${json}")" == "${issuer}" &&
            "$(jq -r '.subject' <<<"${json}")" == \
                "system:serviceaccount:fleet-system:${service_account}" &&
            "$(jq -r '.audiences | sort | join(",")' <<<"${json}")" == "api://AzureADTokenExchange" ]] || {
            echo "error: existing federation ${identity}/${name} does not match issuer/subject/audience" >&2
            return 1
        }
    else
        az identity federated-credential create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
            --identity-name "${identity}" --name "${name}" --issuer "${issuer}" \
            --subject "system:serviceaccount:fleet-system:${service_account}" \
            --audiences api://AzureADTokenExchange --output none
    fi
    id="$(az identity federated-credential show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --identity-name "${identity}" --name "${name}" --query id -o tsv)"
    record_resource federatedCredential "${identity}/${name}" "${id}"
}
create_or_validate_federation "${AFD_PLS_E2E_HUB_CLUSTER}" \
    "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" hub-gateway-controller-manager
create_or_validate_federation "${AFD_PLS_E2E_MEMBER1_CLUSTER}" \
    "afd-m1-id-${AFD_PLS_E2E_RUN_ID}" member-net-controller-manager-sa
create_or_validate_federation "${AFD_PLS_E2E_MEMBER2_CLUSTER}" \
    "afd-m2-id-${AFD_PLS_E2E_RUN_ID}" member-net-controller-manager-sa

resource_group_id="/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_RESOURCE_GROUP}"
ensure_assignment() {
    local principal="$1" role_id="$2" scope="$3" logical_name="$4"
    local matches assignment_id
    matches="$(az role assignment list --assignee-object-id "${principal}" --scope "${scope}" -o json |
        jq --arg role_id "${role_id}" --arg scope "${scope}" \
            '[.[] | select((.roleDefinitionId | ascii_downcase | endswith("/" + ($role_id | ascii_downcase))) and
              (.scope | ascii_downcase) == ($scope | ascii_downcase))]')"
    if [[ "$(jq 'length' <<<"${matches}")" -gt 1 ]]; then
        echo "error: multiple matching role assignments exist for ${logical_name}" >&2
        return 1
    fi
    if [[ "$(jq 'length' <<<"${matches}")" -eq 0 ]]; then
        matches="$(az role assignment create --assignee-object-id "${principal}" \
            --assignee-principal-type ServicePrincipal --role "${role_id}" --scope "${scope}" -o json |
            jq -s 'if length == 1 then . else error("unexpected assignment response") end')"
    fi
    assignment_id="$(jq -r 'if type == "array" then .[0].id else .id end' <<<"${matches}")"
    record_role_assignment "${logical_name}" "${principal}" "${role_id}" "${scope}" "${assignment_id}"
}
ensure_assignment "${hub_gateway_principal_id}" "${hub_role_id}" "${resource_group_id}" hub-afd
ensure_assignment "${member_1_principal_id}" "${member_role_id}" \
    "/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" member-1-pls
ensure_assignment "${member_2_principal_id}" "${member_role_id}" \
    "/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}" member-2-pls

for context_spec in \
    "${AFD_PLS_E2E_HUB_CLUSTER}:${AFD_PLS_E2E_HUB_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER1_CLUSTER}:${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CLUSTER}:${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    cluster="${context_spec%%:*}"
    context="${context_spec#*:}"
    az aks get-credentials --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
        --admin --file "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" --overwrite-existing
    if kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" config get-contexts \
        "${context}-admin" --no-headers >/dev/null 2>&1; then
        kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" config rename-context \
            "${context}-admin" "${context}" >/dev/null
    fi
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" config get-contexts \
        "${context}" --no-headers >/dev/null
done

trap - EXIT INT TERM
complete_stage step-04-identities-rbac-kubeconfig "make phase7-e2e-step-05-crds-registration"
