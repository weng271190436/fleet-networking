#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

readonly EXPECTED_SUBSCRIPTION_ID="d712bfad-d238-486f-8f1b-bf61a831b712"
readonly EXPECTED_SUBSCRIPTION_NAME="AKS Fleet Development/Test"
readonly RUN_ID_PATTERN='^[a-z0-9][a-z0-9-]{2,14}$'
readonly SOURCE_TAG="fleet-networking-afd-pls-e2e"

require_command() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "error: required command '$1' was not found" >&2
        return 1
    }
}

require_env() {
    local name
    for name in "$@"; do
        if [[ -z "${!name:-}" ]]; then
            echo "error: required environment variable ${name} is not set" >&2
            return 1
        fi
    done
}

initialize_names() {
    require_env AFD_PLS_E2E_RUN_ID
    if [[ ! "${AFD_PLS_E2E_RUN_ID}" =~ ${RUN_ID_PATTERN} ]]; then
        echo "error: AFD_PLS_E2E_RUN_ID must match ${RUN_ID_PATTERN}" >&2
        return 1
    fi

    export AFD_PLS_E2E_LOCATION="${AFD_PLS_E2E_LOCATION:-eastus2}"
    export AFD_PLS_E2E_RESOURCE_GROUP="fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}"
    export AFD_PLS_E2E_HUB_CLUSTER="afd-hub-${AFD_PLS_E2E_RUN_ID}"
    export AFD_PLS_E2E_MEMBER1_CLUSTER="afd-m1-${AFD_PLS_E2E_RUN_ID}"
    export AFD_PLS_E2E_MEMBER2_CLUSTER="afd-m2-${AFD_PLS_E2E_RUN_ID}"
    export AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP="${AFD_PLS_E2E_RESOURCE_GROUP}-hub-nodes"
    export AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP="${AFD_PLS_E2E_RESOURCE_GROUP}-m1-nodes"
    export AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP="${AFD_PLS_E2E_RESOURCE_GROUP}-m2-nodes"
    export AFD_PLS_E2E_VNET="afd-vnet-${AFD_PLS_E2E_RUN_ID}"
    export AFD_PLS_E2E_ACR="fleetp7${AFD_PLS_E2E_RUN_ID//-/}d712"
    export AFD_PLS_E2E_WAF_POLICY="afdwaf${AFD_PLS_E2E_RUN_ID//-/}"
    export AFD_PLS_E2E_ARTIFACT_DIR="${PWD}/.phase7-${AFD_PLS_E2E_RUN_ID}"
    export AFD_PLS_E2E_KUBECONFIG="${PWD}/.phase7-${AFD_PLS_E2E_RUN_ID}.kubeconfig"
    export AFD_PLS_E2E_STATE_FILE="${PWD}/.phase7-${AFD_PLS_E2E_RUN_ID}.state.json"
    export AFD_PLS_E2E_RESULTS_FILE="${PWD}/.phase7-${AFD_PLS_E2E_RUN_ID}.results.jsonl"
    export AFD_PLS_E2E_HUB_CONTEXT="phase7-${AFD_PLS_E2E_RUN_ID}-hub"
    export AFD_PLS_E2E_MEMBER1_CONTEXT="phase7-${AFD_PLS_E2E_RUN_ID}-member-1"
    export AFD_PLS_E2E_MEMBER2_CONTEXT="phase7-${AFD_PLS_E2E_RUN_ID}-member-2"
    export AFD_PLS_E2E_HUB_MANIFEST="${AFD_PLS_E2E_HUB_MANIFEST:-${AFD_PLS_E2E_ARTIFACT_DIR}/hub.yaml}"
    export AFD_PLS_E2E_MEMBER1_MANIFEST="${AFD_PLS_E2E_MEMBER1_MANIFEST:-${AFD_PLS_E2E_ARTIFACT_DIR}/member-1.yaml}"
    export AFD_PLS_E2E_MEMBER2_MANIFEST="${AFD_PLS_E2E_MEMBER2_MANIFEST:-${AFD_PLS_E2E_ARTIFACT_DIR}/member-2.yaml}"
    export AFD_PLS_E2E_HUB_CONTROLLER_DEPLOYMENT="deployment/hub-gateway-controller-manager"

    if [[ -f "${AFD_PLS_E2E_STATE_FILE}" ]]; then
        export AFD_PLS_E2E_HUB_IMAGE="$(jq -r '.images.hubGateway // empty' "${AFD_PLS_E2E_STATE_FILE}")"
        export AFD_PLS_E2E_MEMBER_IMAGE="$(jq -r '.images.member // empty' "${AFD_PLS_E2E_STATE_FILE}")"
        export AFD_PLS_E2E_CRD_INSTALLER_IMAGE="$(jq -r '.images.crdInstaller // empty' "${AFD_PLS_E2E_STATE_FILE}")"
        export AFD_PLS_E2E_ECHO_IMAGE="$(jq -r '.images.echo // empty' "${AFD_PLS_E2E_STATE_FILE}")"
        export AFD_PLS_E2E_REFRESH_TOKEN_IMAGE="$(jq -r '.images.refreshToken // empty' "${AFD_PLS_E2E_STATE_FILE}")"
    fi
}

record_identity() {
    local name="$1" client_id="$2" principal_id="$3" resource_id="$4"
    local next="${AFD_PLS_E2E_STATE_FILE}.next"
    jq --arg name "${name}" --arg clientId "${client_id}" --arg principalId "${principal_id}" --arg id "${resource_id}" \
        '.identities += [{name: $name, clientId: $clientId, principalId: $principalId, id: $id}]' \
        "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
    mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"
}

record_role_assignment() {
    local name="$1" principal_id="$2" role="$3" scope="$4" id="$5"
    local next="${AFD_PLS_E2E_STATE_FILE}.next"
    jq --arg name "${name}" --arg principalId "${principal_id}" --arg role "${role}" --arg scope "${scope}" --arg id "${id}" \
        '.roleAssignments += [{name: $name, principalId: $principalId, role: $role, scope: $scope, id: $id}]' \
        "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
    mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"
}

validate_subscription() {
    local id name
    id="$(az account show --query id -o tsv)"
    name="$(az account show --query name -o tsv)"
    if [[ "${id}" != "${EXPECTED_SUBSCRIPTION_ID}" || "${name}" != "${EXPECTED_SUBSCRIPTION_NAME}" ]]; then
        echo "error: active subscription is '${name}' (${id}); expected '${EXPECTED_SUBSCRIPTION_NAME}' (${EXPECTED_SUBSCRIPTION_ID})" >&2
        return 1
    fi
}

require_mutation_approval() {
    if [[ "${AFD_PLS_E2E_APPROVED:-}" != "true" ]]; then
        echo "error: mutation requires AFD_PLS_E2E_APPROVED=true after explicit approval" >&2
        return 1
    fi
}

resource_group_tags() {
    printf 'source=%s phase=7 run-id=%s\n' "${SOURCE_TAG}" "${AFD_PLS_E2E_RUN_ID}"
}

validate_resource_group_boundary() {
    validate_tagged_resource_group "${AFD_PLS_E2E_RESOURCE_GROUP}"
}

validate_tagged_resource_group() {
    local resource_group="$1"
    local expected_source expected_run actual_source actual_run
    [[ "${resource_group}" == "fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}"* ]] || {
        echo "error: resource group ${resource_group} does not match the deterministic run ID" >&2
        return 1
    }
    expected_source="${SOURCE_TAG}"
    expected_run="${AFD_PLS_E2E_RUN_ID}"
    actual_source="$(az group show --name "${resource_group}" --query 'tags.source' -o tsv)"
    actual_run="$(az group show --name "${resource_group}" --query 'tags."run-id"' -o tsv)"
    if [[ "${actual_source}" != "${expected_source}" || "${actual_run}" != "${expected_run}" ]]; then
        echo "error: refusing cleanup: resource group ${resource_group} tags do not match this run" >&2
        return 1
    fi
}

record_resource() {
    local type="$1" name="$2" id="$3"
    local next="${AFD_PLS_E2E_STATE_FILE}.next"
    jq --arg type "${type}" --arg name "${name}" --arg id "${id}" \
        '.resources += [{type: $type, name: $name, id: $id}]' \
        "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
    mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"
}
