#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq kubectl
install_stage_failure_trap step-07-platform-join
require_primary_resource_group
require_state_fields \
    '.completedStages | index("step-06-platform-controllers") != null' \
    "platform controllers have not completed"
[[ -f "${AFD_PLS_E2E_KUBECONFIG}" ]] || {
    echo "error: kubeconfig is missing; run step 04" >&2
    exit 1
}

k() {
    local context="$1"
    shift
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" "$@"
}

k "${AFD_PLS_E2E_HUB_CONTEXT}" -n fleet-system wait --for=condition=Available \
    deployment/hub-gateway-controller-manager --timeout=5m
for context in "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" -n fleet-system wait --for=condition=Available \
        deployment/member-net-controller-manager --timeout=5m
done

join_member() {
    local member_name="$1"
    local namespace="fleet-member-${member_name}"
    local now
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
                    .lastReceivedHeartbeat != null and
                    any(.conditions[]?; .type == "Joined" and .status == "True"))
            ' >/dev/null; then
            if ! k "${AFD_PLS_E2E_HUB_CONTEXT}" get membercluster "${member_name}" -o json |
                jq -e 'any(.status.conditions[]?;
                    .type == "Joined" and .status == "True")' >/dev/null; then
                now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
                k "${AFD_PLS_E2E_HUB_CONTEXT}" patch membercluster "${member_name}" \
                    --subresource=status --type=merge \
                    -p "{\"status\":{\"conditions\":[{\"type\":\"Joined\",\"status\":\"True\",\"reason\":\"NetworkingAgentJoined\",\"message\":\"Phase 7 networking agent joined through the existing E2E registration path\",\"lastTransitionTime\":\"${now}\"}]}}"
            fi
            return
        fi
        sleep 10
    done
    echo "error: networking agent for ${member_name} did not join within 10 minutes" >&2
    return 1
}
join_member "${AFD_PLS_E2E_MEMBER1_CLUSTER}"
join_member "${AFD_PLS_E2E_MEMBER2_CLUSTER}"

cat <<'EOF' | k "${AFD_PLS_E2E_HUB_CONTEXT}" apply --server-side \
    --field-manager=phase7-e2e -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: azure-fleet-afd
spec:
  controllerName: networking.fleet.azure.com/afd
EOF
k "${AFD_PLS_E2E_HUB_CONTEXT}" wait --for=condition=Accepted \
    gatewayclass/azure-fleet-afd --timeout=5m

trap - EXIT INT TERM
complete_stage step-07-platform-join "make phase7-e2e-verify-platform"
