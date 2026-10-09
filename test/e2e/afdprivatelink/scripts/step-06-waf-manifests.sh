#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

stage_init az jq kubectl helm
install_stage_failure_trap step-06-waf-manifests
require_primary_resource_group
require_state_fields '.images | length == 5' "immutable image state is missing"
[[ -f "${AFD_PLS_E2E_KUBECONFIG}" ]] || {
    echo "error: kubeconfig is missing; run step 04" >&2
    exit 1
}
k() {
    local context="$1"
    shift
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" "$@"
}
for member in "${AFD_PLS_E2E_MEMBER1_CLUSTER}" "${AFD_PLS_E2E_MEMBER2_CLUSTER}"; do
    k "${AFD_PLS_E2E_HUB_CONTEXT}" get membercluster "${member}" >/dev/null || {
        echo "error: MemberCluster ${member} is missing; run step 05" >&2
        exit 1
    }
done

hub_client="$(jq -r --arg name "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" \
    '.identities[] | select(.name == $name) | .clientId' "${AFD_PLS_E2E_STATE_FILE}")"
member_1_client="$(jq -r --arg name "${AFD_PLS_E2E_MEMBER1_CLUSTER}-kubelet" \
    '.identities[] | select(.name == $name) | .clientId' "${AFD_PLS_E2E_STATE_FILE}")"
member_2_client="$(jq -r --arg name "${AFD_PLS_E2E_MEMBER2_CLUSTER}-kubelet" \
    '.identities[] | select(.name == $name) | .clientId' "${AFD_PLS_E2E_STATE_FILE}")"
[[ -n "${hub_client}" && -n "${member_1_client}" && -n "${member_2_client}" ]] || {
    echo "error: controller client ID state is incomplete; rerun step 04" >&2
    exit 1
}
tags=("source=${SOURCE_TAG}" "phase=7" "run-id=${AFD_PLS_E2E_RUN_ID}")
if waf_json="$(az network front-door waf-policy show \
    --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${AFD_PLS_E2E_WAF_POLICY}" \
    -o json 2>/dev/null)"; then
    validate_resource_tags "$(jq -r '.tags.source // ""' <<<"${waf_json}")" \
        "$(jq -r '.tags["run-id"] // ""' <<<"${waf_json}")" "WAF policy ${AFD_PLS_E2E_WAF_POLICY}"
    [[ "$(jq -r '.sku.name' <<<"${waf_json}")" == "Premium_AzureFrontDoor" &&
        "$(jq -r '.policySettings.mode' <<<"${waf_json}")" == "Prevention" ]] || {
        echo "error: existing WAF policy SKU or mode does not match the run plan" >&2
        exit 1
    }
else
    az network front-door waf-policy create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --name "${AFD_PLS_E2E_WAF_POLICY}" --sku Premium_AzureFrontDoor --mode Prevention \
        --location Global --tags "${tags[@]}" --output none
fi
if rule_json="$(az network front-door waf-policy rule show \
    --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --policy-name "${AFD_PLS_E2E_WAF_POLICY}" \
    --name BlockPOCHeader -o json 2>/dev/null)"; then
    [[ "$(jq -r '.priority' <<<"${rule_json}")" == "1" &&
        "$(jq -r '.action' <<<"${rule_json}")" == "Block" ]] || {
        echo "error: existing BlockPOCHeader rule does not match priority/action" >&2
        exit 1
    }
else
    az network front-door waf-policy rule create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --policy-name "${AFD_PLS_E2E_WAF_POLICY}" --name BlockPOCHeader --priority 1 \
        --rule-type MatchRule --action Block --match-variable RequestHeader.X-POC-Block \
        --operator Equal --values true --output none
fi
waf_id="$(az network front-door waf-policy show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --name "${AFD_PLS_E2E_WAF_POLICY}" --query id -o tsv)"
record_resource wafPolicy "${AFD_PLS_E2E_WAF_POLICY}" "${waf_id}"

tenant_id="$(az account show --query tenantId -o tsv)"
hub_server="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" config view --raw --minify \
    -o jsonpath='{.clusters[0].cluster.server}')"
hub_ca="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" config view --raw --minify \
    -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
hub_image="$(jq -r '.images.hubGateway' "${AFD_PLS_E2E_STATE_FILE}")"
member_image="$(jq -r '.images.member' "${AFD_PLS_E2E_STATE_FILE}")"
crd_image="$(jq -r '.images.crdInstaller' "${AFD_PLS_E2E_STATE_FILE}")"
echo_image="$(jq -r '.images.echo' "${AFD_PLS_E2E_STATE_FILE}")"
refresh_image="$(jq -r '.images.refreshToken' "${AFD_PLS_E2E_STATE_FILE}")"
hub_repo="${hub_image%@*}"; hub_digest="${hub_image##*@}"
member_repo="${member_image%@*}"; member_digest="${member_image##*@}"
crd_repo="${crd_image%@*}"; crd_digest="${crd_image##*@}"
refresh_repo="${refresh_image%@*}"; refresh_digest="${refresh_image##*@}"

HELM_NO_PLUGINS=1 helm template phase7 "${REPO_ROOT}/charts/hub-gateway-controller-manager" \
    --namespace fleet-system --set-string image.repository="${hub_repo}" \
    --set-string image.digest="${hub_digest}" --set-string crdInstaller.image.repository="${crd_repo}" \
    --set-string crdInstaller.image.digest="${crd_digest}" --set-string azure.clientId="${hub_client}" \
    --set-string azure.tenantId="${tenant_id}" --set-string azure.subscriptionId="${EXPECTED_SUBSCRIPTION_ID}" \
    --set-string azure.resourceGroup="${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --set-string resources.requests.cpu=25m \
    --set-string azure.location="${AFD_PLS_E2E_LOCATION}" >"${AFD_PLS_E2E_HUB_MANIFEST}"

render_member() {
    local manifest="$1" member_name="$2" client_id="$3" node_rg="$4"
    HELM_NO_PLUGINS=1 helm template phase7 "${REPO_ROOT}/charts/member-net-controller-manager" \
        --namespace fleet-system --set-string fullnameOverride=member-net-controller-manager \
        --set-string image.repository="${member_repo}" --set-string image.digest="${member_digest}" \
        --set crdInstaller.enabled=true --set-string crdInstaller.image.repository="${crd_repo}" \
        --set-string crdInstaller.image.digest="${crd_digest}" --set crdInstaller.isE2ETest=true \
        --set-string config.hubURL="${hub_server}" --set-string config.hubCA="${hub_ca}" \
        --set-string config.memberClusterName="${member_name}" --set-string config.provider=azure \
        --set-string refreshtoken.repository="${refresh_repo}" --set-string refreshtoken.digest="${refresh_digest}" \
        --set-string resources.requests.cpu=25m --set-string resources.requests.memory=64Mi \
        --set tlsClientInsecure=false --set-string azure.clientid="${client_id}" \
        --set azure.workloadIdentityEnabled=false --set enableTrafficManagerFeature=false \
        --set enableAFDPrivateLinkFeature=true \
        --set-string afdRequesterSubscriptionAllowlist="${EXPECTED_SUBSCRIPTION_ID}" \
        --set-string azureCloudConfig.tenantId="${tenant_id}" \
        --set-string azureCloudConfig.subscriptionId="${EXPECTED_SUBSCRIPTION_ID}" \
        --set-string azureCloudConfig.aadClientId="${client_id}" \
        --set azureCloudConfig.useManagedIdentityExtension=true \
        --set-string azureCloudConfig.userAssignedIdentityID="${client_id}" \
        --set-string azureCloudConfig.resourceGroup="${node_rg}" \
        --set-string azureCloudConfig.location="${AFD_PLS_E2E_LOCATION}" >"${manifest}"
}
render_member "${AFD_PLS_E2E_MEMBER1_MANIFEST}" "${AFD_PLS_E2E_MEMBER1_CLUSTER}" \
    "${member_1_client}" "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}"
render_member "${AFD_PLS_E2E_MEMBER2_MANIFEST}" "${AFD_PLS_E2E_MEMBER2_CLUSTER}" \
    "${member_2_client}" "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"

append_echo() {
    local manifest="$1" member="$2" pls_subnet="$3"
    cat >>"${manifest}" <<EOF
---
apiVersion: v1
kind: Namespace
metadata: {name: afd-pls-e2e}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: echo, namespace: afd-pls-e2e}
spec:
  replicas: 1
  selector: {matchLabels: {app: echo}}
  template:
    metadata: {labels: {app: echo}}
    spec:
      containers:
      - name: echo
        image: ${echo_image}
        env: [{name: MEMBER_NAME, value: "${member}"}]
        ports: [{containerPort: 8080}]
---
apiVersion: v1
kind: Service
metadata:
  name: echo
  namespace: afd-pls-e2e
  annotations:
    service.beta.kubernetes.io/azure-load-balancer-internal: "true"
    service.beta.kubernetes.io/azure-pls-create: "true"
    service.beta.kubernetes.io/azure-pls-ip-configuration-subnet: "${pls_subnet}"
    service.beta.kubernetes.io/azure-pls-visibility: "${EXPECTED_SUBSCRIPTION_ID}"
spec:
  type: LoadBalancer
  selector: {app: echo}
  ports: [{name: http, port: 80, targetPort: 8080}]
EOF
}
append_echo "${AFD_PLS_E2E_MEMBER1_MANIFEST}" member-1 pls-1
append_echo "${AFD_PLS_E2E_MEMBER2_MANIFEST}" member-2 pls-2
cat >"${AFD_PLS_E2E_GATEWAY_MANIFEST}" <<EOF
apiVersion: v1
kind: Namespace
metadata: {name: afd-pls-e2e}
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: azure-fleet-afd}
spec: {controllerName: networking.fleet.azure.com/afd}
---
apiVersion: networking.fleet.azure.com/v1alpha1
kind: MultiClusterBackend
metadata: {name: echo, namespace: afd-pls-e2e}
spec:
  service: {name: echo, port: 80}
  clusterSelector: {matchLabels: {networking.fleet.azure.com/afd-poc: "true"}}
  healthProbe: {path: /}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: global
  namespace: afd-pls-e2e
  annotations:
    networking.fleet.azure.com/afd-sku: Premium_AzureFrontDoor
    networking.fleet.azure.com/afd-waf-policy-id: "${waf_id}"
spec:
  gatewayClassName: azure-fleet-afd
  listeners: [{name: http, protocol: HTTP, port: 80}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: echo, namespace: afd-pls-e2e}
spec:
  parentRefs: [{name: global}]
  rules:
  - matches: [{path: {type: PathPrefix, value: /}}]
    backendRefs: [{group: networking.fleet.azure.com, kind: MultiClusterBackend, name: echo}]
EOF
for manifest in "${AFD_PLS_E2E_HUB_MANIFEST}" "${AFD_PLS_E2E_MEMBER1_MANIFEST}" \
    "${AFD_PLS_E2E_MEMBER2_MANIFEST}" "${AFD_PLS_E2E_GATEWAY_MANIFEST}"; do
    if grep -E '^[[:space:]]*image:' "${manifest}" |
        grep -Ev '@sha256:[[:xdigit:]]{64}"?[[:space:]]*$' >/dev/null; then
        echo "error: rendered workload image is not digest-pinned in ${manifest}" >&2
        exit 1
    fi
done

trap - EXIT INT TERM
complete_stage step-06-waf-manifests "make phase7-e2e-step-07-deploy-join"
