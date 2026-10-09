#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq
install_stage_failure_trap step-03-aks
require_primary_resource_group
require_state_fields '.images | length == 5' "immutable image state is missing"
acr_id="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query id -o tsv 2>/dev/null)" || {
    echo "error: exact run ACR is missing; run step 01" >&2
    exit 1
}
vnet_id="$(az network vnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --name "${AFD_PLS_E2E_VNET}" --query id -o tsv 2>/dev/null)" || {
    echo "error: exact run VNet is missing; run step 02" >&2
    exit 1
}
tags=("source=${SOURCE_TAG}" "phase=7" "run-id=${AFD_PLS_E2E_RUN_ID}")
cluster_specs=(
    "${AFD_PLS_E2E_HUB_CLUSTER}:hub:${AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP}"
    "${AFD_PLS_E2E_MEMBER1_CLUSTER}:member-1:${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}"
    "${AFD_PLS_E2E_MEMBER2_CLUSTER}:member-2:${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"
)

validate_cluster() {
    local cluster="$1" subnet="$2" node_rg="$3" json pool_subnet
    json="$(az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" -o json)"
    validate_resource_tags "$(jq -r '.tags.source // ""' <<<"${json}")" \
        "$(jq -r '.tags["run-id"] // ""' <<<"${json}")" "AKS cluster ${cluster}"
    [[ "$(jq -r '.resourceGroup' <<<"${json}")" == "${AFD_PLS_E2E_RESOURCE_GROUP}" &&
        "$(jq -r '.location' <<<"${json}")" == "${AFD_PLS_E2E_LOCATION}" &&
        "$(jq -r '.nodeResourceGroup' <<<"${json}")" == "${node_rg}" &&
        "$(jq -r '.networkProfile.loadBalancerSku' <<<"${json}")" == "standard" &&
        "$(jq -r '.aadProfile.managed' <<<"${json}")" == "true" &&
        "$(jq -r '.aadProfile.enableAzureRbac' <<<"${json}")" == "true" &&
        "$(jq -r '.oidcIssuerProfile.enabled' <<<"${json}")" == "true" &&
        "$(jq -r '.workloadIdentityProfile.enabled' <<<"${json}")" == "true" ]] || {
        echo "error: existing AKS cluster ${cluster} does not match RG/node RG/LB/workload identity plan" >&2
        return 1
    }
    [[ "$(az aks nodepool show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --cluster-name "${cluster}" --name nodepool1 --query vmSize -o tsv)" == "Standard_D2as_v4" ]] || {
        echo "error: existing AKS cluster ${cluster} node SKU does not match Standard_D2as_v4" >&2
        return 1
    }
    pool_subnet="$(az aks nodepool show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --cluster-name "${cluster}" --name nodepool1 --query vnetSubnetId -o tsv)"
    [[ "${pool_subnet,,}" == "${vnet_id,,}/subnets/${subnet}" ]] || {
        echo "error: existing AKS cluster ${cluster} uses unexpected subnet ${pool_subnet}" >&2
        return 1
    }
}

# Validate every prerequisite and every existing cluster before creating any missing cluster.
for spec in "${cluster_specs[@]}"; do
    cluster="${spec%%:*}"
    rest="${spec#*:}"
    subnet="${rest%%:*}"
    node_rg="${rest#*:}"
    az network vnet subnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --vnet-name "${AFD_PLS_E2E_VNET}" --name "${subnet}" --output none
    if az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
        --output none 2>/dev/null; then
        existing_json="$(az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
            --name "${cluster}" -o json)"
        validate_resource_tags "$(jq -r '.tags.source // ""' <<<"${existing_json}")" \
            "$(jq -r '.tags["run-id"] // ""' <<<"${existing_json}")" "AKS cluster ${cluster}"
        if [[ "$(jq -r '.aadProfile.managed // false' <<<"${existing_json}")" != "true" ||
            "$(jq -r '.aadProfile.enableAzureRbac // false' <<<"${existing_json}")" != "true" ]]; then
            echo "enabling Microsoft Entra integration and Azure RBAC on ${cluster}"
            az aks update --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
                --enable-aad --enable-azure-rbac --output none
        fi
        validate_cluster "${cluster}" "${subnet}" "${node_rg}"
        az group show --name "${node_rg}" --output none 2>/dev/null || {
            echo "error: existing AKS cluster ${cluster} node resource group ${node_rg} is missing" >&2
            exit 1
        }
        node_source="$(az group show --name "${node_rg}" --query tags.source -o tsv)"
        node_run="$(az group show --name "${node_rg}" --query 'tags."run-id"' -o tsv)"
        if [[ -n "${node_source}" || -n "${node_run}" ]]; then
            validate_resource_tags "${node_source}" "${node_run}" "node resource group ${node_rg}"
        fi
    fi
done

for spec in "${cluster_specs[@]}"; do
    cluster="${spec%%:*}"
    rest="${spec#*:}"
    subnet="${rest%%:*}"
    node_rg="${rest#*:}"
    subnet_id="${vnet_id}/subnets/${subnet}"
    if ! az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
        --output none 2>/dev/null; then
        az aks create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
            --location "${AFD_PLS_E2E_LOCATION}" --node-count 1 --node-vm-size Standard_D2as_v4 \
            --network-plugin azure --load-balancer-sku standard --vnet-subnet-id "${subnet_id}" \
            --enable-managed-identity --enable-aad --enable-azure-rbac \
            --enable-oidc-issuer --enable-workload-identity \
            --attach-acr "${AFD_PLS_E2E_ACR}" --node-resource-group "${node_rg}" \
            --generate-ssh-keys --tags "${tags[@]}" --output none
    fi
    validate_cluster "${cluster}" "${subnet}" "${node_rg}"
    cluster_id="$(az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --name "${cluster}" --query id -o tsv)"
    record_resource managedCluster "${cluster}" "${cluster_id}"
    identity_json="$(az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --name "${cluster}" --query '{controlPlane:identity,kubelet:identityProfile.kubeletidentity}' -o json)"
    record_identity "${cluster}-control-plane" "" \
        "$(jq -r '.controlPlane.principalId' <<<"${identity_json}")" "${cluster_id}"
    record_identity "${cluster}-kubelet" "$(jq -r '.kubelet.clientId' <<<"${identity_json}")" \
        "$(jq -r '.kubelet.objectId' <<<"${identity_json}")" \
        "$(jq -r '.kubelet.resourceId' <<<"${identity_json}")"
    if az group show --name "${node_rg}" --output none 2>/dev/null; then
        node_source="$(az group show --name "${node_rg}" --query tags.source -o tsv)"
        node_run="$(az group show --name "${node_rg}" --query 'tags."run-id"' -o tsv)"
        if [[ -n "${node_source}" || -n "${node_run}" ]]; then
            validate_resource_tags "${node_source}" "${node_run}" "node resource group ${node_rg}"
        else
            az group update --name "${node_rg}" --tags "${tags[@]}" --output none
        fi
        record_resource nodeResourceGroup "${node_rg}" \
            "$(az group show --name "${node_rg}" --query id -o tsv)"
        vmss="$(az vmss list --resource-group "${node_rg}" --query '[0].name' -o tsv)"
        [[ -n "${vmss}" ]] || {
            echo "error: node resource group ${node_rg} contains no VMSS" >&2
            exit 1
        }
        power="$(az vmss list-instances --resource-group "${node_rg}" --name "${vmss}" \
            --expand instanceView \
            --query '[0].instanceView.statuses[?starts_with(code, `PowerState/`)].code | [0]' -o tsv)"
        if [[ "${power}" != "PowerState/running" ]]; then
            echo "starting deallocated VMSS ${node_rg}/${vmss}"
            az vmss start --resource-group "${node_rg}" --name "${vmss}" --no-wait
            for _ in $(seq 1 60); do
                power="$(az vmss list-instances --resource-group "${node_rg}" --name "${vmss}" \
                    --expand instanceView \
                    --query '[0].instanceView.statuses[?starts_with(code, `PowerState/`)].code | [0]' -o tsv)"
                [[ "${power}" == "PowerState/running" ]] && break
                sleep 15
            done
            [[ "${power}" == "PowerState/running" ]] || {
                echo "error: VMSS ${node_rg}/${vmss} did not reach running state" >&2
                exit 1
            }
        fi
    fi
done

trap - EXIT INT TERM
complete_stage step-03-aks "make phase7-e2e-step-04-identities-rbac-kubeconfig"
