#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq
install_stage_failure_trap step-02-network
require_primary_resource_group
tags=("source=${SOURCE_TAG}" "phase=7" "run-id=${AFD_PLS_E2E_RUN_ID}")
vnet_exists=false
if az network vnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --name "${AFD_PLS_E2E_VNET}" --output none 2>/dev/null; then
    vnet_exists=true
    vnet_json="$(az network vnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --name "${AFD_PLS_E2E_VNET}" -o json)"
    [[ "$(jq -r '.addressSpace.addressPrefixes | sort | join(",")' <<<"${vnet_json}")" == "10.70.0.0/16" ]] || {
        echo "error: existing VNet address space is not exactly 10.70.0.0/16" >&2
        exit 1
    }
    source_tag="$(jq -r '.tags.source // ""' <<<"${vnet_json}")"
    run_tag="$(jq -r '.tags["run-id"] // ""' <<<"${vnet_json}")"
    if [[ -z "${source_tag}" && -z "${run_tag}" ]]; then
        expected_id="/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_RESOURCE_GROUP}/providers/Microsoft.Network/virtualNetworks/${AFD_PLS_E2E_VNET}"
        jq -e --arg id "${expected_id}" \
            'any(.resources[]?; .type == "virtualNetwork" and (.id | ascii_downcase) == ($id | ascii_downcase))' \
            "${AFD_PLS_E2E_STATE_FILE}" >/dev/null || {
            echo "error: untagged existing VNet is not recorded in this exact run state" >&2
            exit 1
        }
        echo "repairing tags on the state-recorded VNet; no address space or subnet is changed"
        az network vnet update --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
            --name "${AFD_PLS_E2E_VNET}" --tags "${tags[@]}" --output none
    else
        validate_resource_tags "${source_tag}" "${run_tag}" "VNet ${AFD_PLS_E2E_VNET}"
    fi
fi

subnet_specs=(
    "hub:10.70.0.0/22"
    "member-1:10.70.4.0/22"
    "member-2:10.70.8.0/22"
    "pls-1:10.70.12.0/24"
    "pls-2:10.70.13.0/24"
)
if [[ "${vnet_exists}" == true ]]; then
    for spec in "${subnet_specs[@]}"; do
        name="${spec%%:*}"
        prefix="${spec#*:}"
        if subnet_json="$(az network vnet subnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
            --vnet-name "${AFD_PLS_E2E_VNET}" --name "${name}" -o json 2>/dev/null)"; then
            [[ "$(jq -r '[.addressPrefix // empty, .addressPrefixes[]?] | map(select(length > 0)) |
                sort | join(",")' <<<"${subnet_json}")" == "${prefix}" ]] || {
                echo "error: existing subnet ${name} prefix does not match ${prefix}; it will not be updated" >&2
                exit 1
            }
            [[ "$(jq -r '.privateLinkServiceNetworkPolicies' <<<"${subnet_json}")" == "Disabled" ]] || {
                echo "error: existing subnet ${name} PLS policy is not Disabled; it will not be updated" >&2
                exit 1
            }
            echo "reuse subnet ${name} (${prefix})"
        fi
    done
fi

if [[ "${vnet_exists}" == false ]]; then
    az network vnet create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --name "${AFD_PLS_E2E_VNET}" --location "${AFD_PLS_E2E_LOCATION}" \
        --address-prefixes 10.70.0.0/16 --tags "${tags[@]}" --output none
fi
for spec in "${subnet_specs[@]}"; do
    name="${spec%%:*}"
    prefix="${spec#*:}"
    if ! az network vnet subnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --vnet-name "${AFD_PLS_E2E_VNET}" --name "${name}" --output none 2>/dev/null; then
        az network vnet subnet create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
            --vnet-name "${AFD_PLS_E2E_VNET}" --name "${name}" --address-prefixes "${prefix}" \
            --disable-private-link-service-network-policies true --output none
    fi
done
vnet_id="$(az network vnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --name "${AFD_PLS_E2E_VNET}" --query id -o tsv)"
record_resource virtualNetwork "${AFD_PLS_E2E_VNET}" "${vnet_id}"
for spec in "${subnet_specs[@]}"; do
    name="${spec%%:*}"
    subnet_id="$(az network vnet subnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --vnet-name "${AFD_PLS_E2E_VNET}" --name "${name}" --query id -o tsv)"
    record_resource subnet "${name}" "${subnet_id}"
done

trap - EXIT INT TERM
complete_stage step-02-network "make phase7-e2e-step-03-aks"
