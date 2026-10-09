#!/usr/bin/env bash

readonly EXPECTED_SUBSCRIPTION_ID="d712bfad-d238-486f-8f1b-bf61a831b712"
readonly EXPECTED_SUBSCRIPTION_NAME="AKS Fleet Development/Test"
readonly RUN_ID_PATTERN='^[a-z0-9][a-z0-9-]{2,14}$'
readonly SOURCE_TAG="fleet-networking-afd-pls-e2e"
readonly PHASE7_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"

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
    export AFD_PLS_E2E_ARTIFACT_DIR="${PHASE7_REPO_ROOT}/.phase7-${AFD_PLS_E2E_RUN_ID}"
    export AFD_PLS_E2E_KUBECONFIG="${PHASE7_REPO_ROOT}/.phase7-${AFD_PLS_E2E_RUN_ID}.kubeconfig"
    export AFD_PLS_E2E_STATE_FILE="${PHASE7_REPO_ROOT}/.phase7-${AFD_PLS_E2E_RUN_ID}.state.json"
    export AFD_PLS_E2E_RESULTS_FILE="${PHASE7_REPO_ROOT}/.phase7-${AFD_PLS_E2E_RUN_ID}.results.jsonl"
    export AFD_PLS_E2E_HUB_CONTEXT="phase7-${AFD_PLS_E2E_RUN_ID}-hub"
    export AFD_PLS_E2E_MEMBER1_CONTEXT="phase7-${AFD_PLS_E2E_RUN_ID}-member-1"
    export AFD_PLS_E2E_MEMBER2_CONTEXT="phase7-${AFD_PLS_E2E_RUN_ID}-member-2"
    export AFD_PLS_E2E_HUB_MANIFEST="${AFD_PLS_E2E_HUB_MANIFEST:-${AFD_PLS_E2E_ARTIFACT_DIR}/hub.yaml}"
    export AFD_PLS_E2E_MEMBER1_MANIFEST="${AFD_PLS_E2E_MEMBER1_MANIFEST:-${AFD_PLS_E2E_ARTIFACT_DIR}/member-1.yaml}"
    export AFD_PLS_E2E_MEMBER2_MANIFEST="${AFD_PLS_E2E_MEMBER2_MANIFEST:-${AFD_PLS_E2E_ARTIFACT_DIR}/member-2.yaml}"
    export AFD_PLS_E2E_GATEWAY_MANIFEST="${AFD_PLS_E2E_ARTIFACT_DIR}/gateway-resources.yaml"
    export AFD_PLS_E2E_HUB_CONTROLLER_DEPLOYMENT="deployment/hub-gateway-controller-manager"

    if [[ -f "${AFD_PLS_E2E_STATE_FILE}" ]]; then
        export AFD_PLS_E2E_HUB_IMAGE="$(jq -r '.images.hubGateway // empty' "${AFD_PLS_E2E_STATE_FILE}")"
        export AFD_PLS_E2E_MEMBER_IMAGE="$(jq -r '.images.member // empty' "${AFD_PLS_E2E_STATE_FILE}")"
        export AFD_PLS_E2E_CRD_INSTALLER_IMAGE="$(jq -r '.images.crdInstaller // empty' "${AFD_PLS_E2E_STATE_FILE}")"
        export AFD_PLS_E2E_ECHO_IMAGE="$(jq -r '.images.echo // empty' "${AFD_PLS_E2E_STATE_FILE}")"
        export AFD_PLS_E2E_REFRESH_TOKEN_IMAGE="$(jq -r '.images.refreshToken // empty' "${AFD_PLS_E2E_STATE_FILE}")"
    fi
}

validate_role_assignment_boundary() {
    local scope="$1"
    local assignment_id="$2"
    local normalized_scope="${scope,,}"
    local normalized_assignment_id="${assignment_id,,}"
    local resource_group resource_group_scope
    local allowed_scope=false

    [[ -n "${scope}" && -n "${assignment_id}" ]] || {
        echo "error: role assignment scope and ID must be non-empty" >&2
        return 1
    }

    for resource_group in \
        "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        "${AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP}" \
        "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" \
        "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"; do
        resource_group_scope="/subscriptions/${EXPECTED_SUBSCRIPTION_ID,,}/resourcegroups/${resource_group,,}"
        if [[ "${normalized_scope}" == "${resource_group_scope}" ||
            "${normalized_scope}" == "${resource_group_scope}/"* ]]; then
            allowed_scope=true
            break
        fi
    done

    [[ "${allowed_scope}" == true ]] || {
        echo "error: refusing role assignment outside the exact run resource groups: ${scope}" >&2
        return 1
    }

    local assignment_prefix="${normalized_scope}/providers/microsoft.authorization/roleassignments/"
    local assignment_suffix="${normalized_assignment_id#"${assignment_prefix}"}"
    [[ "${normalized_assignment_id}" == "${assignment_prefix}"* &&
        "${assignment_suffix}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || {
        echo "error: role assignment ID does not match its recorded scope: ${assignment_id}" >&2
        return 1
    }
}

record_identity() {
    local name="$1" client_id="$2" principal_id="$3" resource_id="$4"
    local next="${AFD_PLS_E2E_STATE_FILE}.next"
    jq --arg name "${name}" --arg clientId "${client_id}" --arg principalId "${principal_id}" --arg id "${resource_id}" \
        '.identities = ((.identities // []) | map(select(.name != $name)) +
          [{name: $name, clientId: $clientId, principalId: $principalId, id: $id}])' \
        "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
    mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"
}

record_role_assignment() {
    local name="$1" principal_id="$2" role="$3" scope="$4" id="$5"
    local next="${AFD_PLS_E2E_STATE_FILE}.next"
    jq --arg name "${name}" --arg principalId "${principal_id}" --arg role "${role}" --arg scope "${scope}" --arg id "${id}" \
        '.roleAssignments = ((.roleAssignments // []) | map(select(.name != $name)) +
          [{name: $name, principalId: $principalId, role: $role, scope: $scope, id: $id}])' \
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
        echo "error: resource group ${resource_group} tags do not match this run" >&2
        return 1
    fi
}

record_resource() {
    local type="$1" name="$2" id="$3"
    local next="${AFD_PLS_E2E_STATE_FILE}.next"
    jq --arg type "${type}" --arg name "${name}" --arg id "${id}" \
        '.resources = ((.resources // []) | map(select(.type != $type or .name != $name)) +
          [{type: $type, name: $name, id: $id}])' \
        "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
    mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"
}

initialize_state() {
    umask 077
    mkdir -p "${AFD_PLS_E2E_ARTIFACT_DIR}"
    if [[ -f "${AFD_PLS_E2E_STATE_FILE}" ]]; then
        jq -e --arg subscriptionId "${EXPECTED_SUBSCRIPTION_ID}" --arg runId "${AFD_PLS_E2E_RUN_ID}" \
            --arg resourceGroup "${AFD_PLS_E2E_RESOURCE_GROUP}" --arg location "${AFD_PLS_E2E_LOCATION}" \
            '.subscriptionId == $subscriptionId and .runId == $runId and
             .resourceGroup == $resourceGroup and .location == $location' \
            "${AFD_PLS_E2E_STATE_FILE}" >/dev/null
        update_state '.version = 3 | .resources //= [] | .identities //= [] |
            .roleAssignments //= [] | .images //= {} | .completedStages //= []'
        return
    fi
    jq -n --arg subscriptionId "${EXPECTED_SUBSCRIPTION_ID}" --arg runId "${AFD_PLS_E2E_RUN_ID}" \
        --arg resourceGroup "${AFD_PLS_E2E_RESOURCE_GROUP}" --arg location "${AFD_PLS_E2E_LOCATION}" \
        '{version: 3, subscriptionId: $subscriptionId, runId: $runId,
          resourceGroup: $resourceGroup, location: $location, resources: [], identities: [],
          roleAssignments: [], images: {}, completedStages: []}' >"${AFD_PLS_E2E_STATE_FILE}"
}

update_state() {
    local filter="$1"
    local next="${AFD_PLS_E2E_STATE_FILE}.next"
    jq "${filter}" "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
    mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"
}

complete_stage() {
    local stage="$1" next_command="$2"
    local next="${AFD_PLS_E2E_STATE_FILE}.next"
    jq --arg stage "${stage}" \
        '.completedStages = (((.completedStages // []) + [$stage]) | unique)' \
        "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
    mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"
    printf 'Phase 7 %s complete.\nNext command:\n  %s\n' "${stage}" "${next_command}"
}

require_state_fields() {
    local expression="$1" description="$2"
    [[ -f "${AFD_PLS_E2E_STATE_FILE}" ]] || {
        echo "error: ${description}; run the preceding stage first" >&2
        return 1
    }
    jq -e "${expression}" "${AFD_PLS_E2E_STATE_FILE}" >/dev/null || {
        echo "error: ${description}; state ${AFD_PLS_E2E_STATE_FILE} is incomplete" >&2
        return 1
    }
}

stage_init() {
    initialize_names
    require_mutation_approval
    require_env AZURE_SUBSCRIPTION_ID
    [[ "${AZURE_SUBSCRIPTION_ID}" == "${EXPECTED_SUBSCRIPTION_ID}" ]] || {
        echo "error: AZURE_SUBSCRIPTION_ID must be ${EXPECTED_SUBSCRIPTION_ID}" >&2
        return 1
    }
    for tool in "$@"; do
        require_command "${tool}"
    done
    validate_subscription
    initialize_state
}

require_primary_resource_group() {
    az group show --name "${AFD_PLS_E2E_RESOURCE_GROUP}" --output none 2>/dev/null || {
        echo "error: primary resource group is missing; run step 01 first" >&2
        return 1
    }
    validate_resource_group_boundary
}

validate_resource_tags() {
    local source="$1" run_id="$2" description="$3"
    if [[ "${source}" != "${SOURCE_TAG}" || "${run_id}" != "${AFD_PLS_E2E_RUN_ID}" ]]; then
        echo "error: ${description} is not tagged for this exact Phase 7 run" >&2
        return 1
    fi
}

install_stage_failure_trap() {
    local stage="$1"
    trap 'stage_failure "'"${stage}"'" "$?"' EXIT
}

stage_failure() {
    local stage="$1" status="$2"
    (( status != 0 )) || return 0
    local diagnostics="${AFD_PLS_E2E_RESULTS_FILE%.jsonl}.${stage}-failure.log"
    {
        printf 'Phase 7 %s diagnostics at %s\n' "${stage}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        printf 'run=%s resourceGroup=%s\n' "${AFD_PLS_E2E_RUN_ID}" "${AFD_PLS_E2E_RESOURCE_GROUP}"
        if command -v az >/dev/null 2>&1; then
            az resource list --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
                --query '[].{name:name,type:type,id:id}' -o table 2>&1 || true
        fi
        if [[ -f "${AFD_PLS_E2E_KUBECONFIG}" ]] && command -v kubectl >/dev/null 2>&1; then
            local context
            for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
                "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
                echo "=== ${context}: workloads ==="
                kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" \
                    get pods -A -o wide 2>&1 || true
                echo "=== ${context}: recent events ==="
                kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" \
                    get events -A --sort-by=.metadata.creationTimestamp 2>&1 || true
            done
        fi
    } >"${diagnostics}"
    cat >&2 <<EOF
${stage} failed; the environment was preserved and no cleanup was attempted.
Diagnostics (no Secrets): ${diagnostics}
Fix the reported prerequisite/resource mismatch, then rerun only this stage.
EOF
}
