#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq kubectl helm go
install_stage_failure_trap step-05-crds-registration
require_primary_resource_group
[[ -f "${AFD_PLS_E2E_KUBECONFIG}" ]] || {
    echo "error: kubeconfig is missing; run step 04" >&2
    exit 1
}
for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" config get-contexts \
        "${context}" --no-headers >/dev/null || {
        echo "error: context ${context} is missing; rerun step 04" >&2
        exit 1
    }
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" \
        get --raw=/readyz >/dev/null || {
        echo "error: Kubernetes API for ${context} is not ready" >&2
        exit 1
    }
done
hub_principal="$(jq -r --arg name "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" \
    '.identities[] | select(.name == $name) | .principalId' "${AFD_PLS_E2E_STATE_FILE}")"
member_1_principal="$(jq -r --arg name "afd-m1-id-${AFD_PLS_E2E_RUN_ID}" \
    '.identities[] | select(.name == $name) | .principalId' "${AFD_PLS_E2E_STATE_FILE}")"
member_2_principal="$(jq -r --arg name "afd-m2-id-${AFD_PLS_E2E_RUN_ID}" \
    '.identities[] | select(.name == $name) | .principalId' "${AFD_PLS_E2E_STATE_FILE}")"
[[ -n "${hub_principal}" && -n "${member_1_principal}" && -n "${member_2_principal}" ]] || {
    echo "error: controller identity state is incomplete; rerun step 04" >&2
    exit 1
}

k() {
    local context="$1"
    shift
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" "$@"
}
module_cache="$(go env GOMODCACHE)"
fleet_crd_dir="${module_cache}/go.goms.io/fleet@v0.14.0/config/crd/bases"
gateway_crd_dir="${module_cache}/sigs.k8s.io/gateway-api@v1.2.1/config/crd/standard"
for input in \
    "${fleet_crd_dir}/cluster.kubernetes-fleet.io_memberclusters.yaml" \
    "${fleet_crd_dir}/cluster.kubernetes-fleet.io_internalmemberclusters.yaml" \
    "${gateway_crd_dir}/gateway.networking.k8s.io_gateways.yaml"; do
    [[ -f "${input}" ]] || {
        echo "error: pinned CRD input ${input} is missing; run go mod download" >&2
        exit 1
    }
done

for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" create namespace fleet-system --dry-run=client -o yaml |
        k "${context}" apply -f -
done
k "${AFD_PLS_E2E_HUB_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${fleet_crd_dir}/cluster.kubernetes-fleet.io_memberclusters.yaml" \
    -f "${fleet_crd_dir}/cluster.kubernetes-fleet.io_internalmemberclusters.yaml" \
    -f "${gateway_crd_dir}"
for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" apply --server-side --field-manager=phase7-e2e \
        -f "${REPO_ROOT}/config/crd/bases"
done

registration_manifest="${AFD_PLS_E2E_ARTIFACT_DIR}/hub-member-registration.yaml"
HELM_NO_PLUGINS=1 helm template phase7-registration \
    "${REPO_ROOT}/examples/getting-started/charts/hub" --namespace fleet-system \
    --set-string userNS=afd-pls-e2e \
    --set-string memberClusterConfigs[0].memberID="${AFD_PLS_E2E_MEMBER1_CLUSTER}" \
    --set-string memberClusterConfigs[0].principalID="${member_1_principal}" \
    --set-string memberClusterConfigs[1].memberID="${AFD_PLS_E2E_MEMBER2_CLUSTER}" \
    --set-string memberClusterConfigs[1].principalID="${member_2_principal}" \
    >"${registration_manifest}"
k "${AFD_PLS_E2E_HUB_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${registration_manifest}"

create_member_cluster() {
    local member_name="$1" principal_id="$2"
    cat <<EOF | k "${AFD_PLS_E2E_HUB_CONTEXT}" apply -f -
apiVersion: cluster.kubernetes-fleet.io/v1beta1
kind: MemberCluster
metadata:
  name: ${member_name}
  labels: {networking.fleet.azure.com/afd-poc: "true"}
spec:
  identity:
    apiGroup: rbac.authorization.k8s.io
    kind: User
    name: ${principal_id}
EOF
}
create_member_cluster "${AFD_PLS_E2E_MEMBER1_CLUSTER}" "${member_1_principal}"
create_member_cluster "${AFD_PLS_E2E_MEMBER2_CLUSTER}" "${member_2_principal}"

trap - EXIT INT TERM
complete_stage step-05-crds-registration "make phase7-e2e-step-06-waf-manifests"
