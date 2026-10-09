#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq kubectl
install_stage_failure_trap step-07-deploy-join
require_primary_resource_group
[[ -f "${AFD_PLS_E2E_KUBECONFIG}" ]] || {
    echo "error: kubeconfig is missing; run step 04" >&2
    exit 1
}
for manifest in "${AFD_PLS_E2E_HUB_MANIFEST}" "${AFD_PLS_E2E_MEMBER1_MANIFEST}" \
    "${AFD_PLS_E2E_MEMBER2_MANIFEST}" "${AFD_PLS_E2E_GATEWAY_MANIFEST}"; do
    [[ -s "${manifest}" ]] || {
        echo "error: rendered manifest ${manifest} is missing; run step 06" >&2
        exit 1
    }
done
k() {
    local context="$1"
    shift
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" "$@"
}
for member in "${AFD_PLS_E2E_MEMBER1_CLUSTER}" "${AFD_PLS_E2E_MEMBER2_CLUSTER}"; do
    k "${AFD_PLS_E2E_HUB_CONTEXT}" get membercluster "${member}" >/dev/null || {
        echo "error: MemberCluster ${member} is missing; rerun step 05" >&2
        exit 1
    }
done
for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    ready_nodes="$(k "${context}" get nodes -o json |
        jq '[.items[].status.conditions[] | select(.type == "Ready" and .status == "True")] | length')"
    [[ "${ready_nodes}" -ge 1 ]] || {
        echo "error: ${context} has no Ready node; rerun the VMSS recovery in step 03" >&2
        exit 1
    }
done
webhook_endpoints="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" -n kube-system get endpoints \
    azure-wi-webhook-webhook-service -o json |
    jq '[.subsets[]?.addresses[]?] | length')"
[[ "${webhook_endpoints}" -ge 1 ]] || {
    echo "error: hub Azure Workload Identity webhook has no endpoint" >&2
    exit 1
}

k "${AFD_PLS_E2E_HUB_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${AFD_PLS_E2E_HUB_MANIFEST}"
k "${AFD_PLS_E2E_MEMBER1_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${AFD_PLS_E2E_MEMBER1_MANIFEST}"
k "${AFD_PLS_E2E_MEMBER2_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${AFD_PLS_E2E_MEMBER2_MANIFEST}"

k "${AFD_PLS_E2E_HUB_CONTEXT}" -n fleet-system wait --for=condition=Available \
    deployment/hub-gateway-controller-manager --timeout=20m
for context in "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" -n fleet-system wait --for=condition=Available \
        deployment/member-net-controller-manager --timeout=20m
    k "${context}" -n afd-pls-e2e wait --for=condition=Available deployment/echo --timeout=20m
done
k "${AFD_PLS_E2E_HUB_CONTEXT}" wait --for=condition=Established \
    crd/multiclusterbackends.networking.fleet.azure.com \
    crd/serviceoriginassignments.networking.fleet.azure.com --timeout=5m
k "${AFD_PLS_E2E_HUB_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${AFD_PLS_E2E_GATEWAY_MANIFEST}"

join_member() {
    local member_name="$1"
    local namespace="fleet-member-${member_name}"
    cat <<EOF | k "${AFD_PLS_E2E_HUB_CONTEXT}" apply -f -
apiVersion: cluster.kubernetes-fleet.io/v1beta1
kind: InternalMemberCluster
metadata:
  name: ${member_name}
  namespace: ${namespace}
spec:
  state: Join
  heartbeatPeriodSeconds: 10
EOF
    for _ in $(seq 1 60); do
        if k "${AFD_PLS_E2E_HUB_CONTEXT}" -n "${namespace}" get \
            internalmembercluster "${member_name}" -o json | jq -e '
                any(.status.agentStatus[]?;
                    .type == "ServiceExportImportAgent" and
                    (.lastReceivedHeartbeat != null) and
                    any(.conditions[]?; .type == "Joined" and .status == "True"))
            ' >/dev/null; then
            now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
            k "${AFD_PLS_E2E_HUB_CONTEXT}" patch membercluster "${member_name}" \
                --subresource=status --type=merge \
                -p "{\"status\":{\"conditions\":[{\"type\":\"Joined\",\"status\":\"True\",\"reason\":\"NetworkingAgentJoined\",\"message\":\"Phase 7 networking agent joined through the existing E2E registration path\",\"lastTransitionTime\":\"${now}\"}]}}"
            return
        fi
        sleep 10
    done
    echo "error: networking agent for ${member_name} did not join within 10 minutes" >&2
    return 1
}
join_member "${AFD_PLS_E2E_MEMBER1_CLUSTER}"
join_member "${AFD_PLS_E2E_MEMBER2_CLUSTER}"

assignments="$(az role assignment list --all --fill-principal-name false \
    --query "[?contains(scope, '${AFD_PLS_E2E_RESOURCE_GROUP}')].{id:id,name:name,role:roleDefinitionName,principalId:principalId,scope:scope}" \
    -o json)"
next="${AFD_PLS_E2E_STATE_FILE}.next"
jq --argjson assignments "${assignments}" '.discoveredRoleAssignments = $assignments' \
    "${AFD_PLS_E2E_STATE_FILE}" >"${next}"
mv "${next}" "${AFD_PLS_E2E_STATE_FILE}"

trap - EXIT INT TERM
complete_stage step-07-deploy-join \
    "follow section 7 in test/e2e/afdprivatelink/README.md; do not claim Phase 7 complete"
