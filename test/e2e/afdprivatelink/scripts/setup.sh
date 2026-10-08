#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

initialize_names
require_mutation_approval
"${SCRIPT_DIR}/preflight.sh"

report_failure() {
    local status=$?
    if (( status != 0 )); then
        if [[ -f "${AFD_PLS_E2E_KUBECONFIG}" ]]; then
            diagnostics="${AFD_PLS_E2E_RESULTS_FILE%.jsonl}.setup-failure.log"
            {
                echo "Phase 7 setup diagnostics at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
                for context in \
                    "${AFD_PLS_E2E_HUB_CONTEXT}" \
                    "${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
                    "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
                    echo "=== ${context}: pods ==="
                    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" get pods -A -o wide || true
                    echo "=== ${context}: events ==="
                    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" get events -A \
                        --sort-by=.metadata.creationTimestamp || true
                done
                echo "=== hub init logs ==="
                kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${AFD_PLS_E2E_HUB_CONTEXT}" \
                    -n fleet-system logs deployment/hub-gateway-controller-manager -c net-crd-installer --tail=200 || true
                echo "=== hub manager logs ==="
                kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${AFD_PLS_E2E_HUB_CONTEXT}" \
                    -n fleet-system logs deployment/hub-gateway-controller-manager -c manager --tail=200 || true
                for context in "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
                    echo "=== ${context}: member manager logs ==="
                    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" \
                        -n fleet-system logs deployment/member-net-controller-manager --tail=200 || true
                done
            } >"${diagnostics}" 2>&1
            echo "setup diagnostics written to ${diagnostics}" >&2
        fi
        cat >&2 <<EOF
setup failed; the partially provisioned environment was preserved for diagnosis.
After fixing the issue, retry with:
  make phase7-e2e-setup
To delete the exact tagged run resources manually, run:
  make phase7-e2e-cleanup
Billable resources remain until setup completes or cleanup is run explicitly.
EOF
    fi
    exit "${status}"
}
trap report_failure EXIT INT TERM

umask 077
mkdir -p "${AFD_PLS_E2E_ARTIFACT_DIR}"
if [[ -f "${AFD_PLS_E2E_STATE_FILE}" ]]; then
    jq -e --arg subscriptionId "${EXPECTED_SUBSCRIPTION_ID}" --arg runId "${AFD_PLS_E2E_RUN_ID}" \
        --arg resourceGroup "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        '.subscriptionId == $subscriptionId and .runId == $runId and .resourceGroup == $resourceGroup' \
        "${AFD_PLS_E2E_STATE_FILE}" >/dev/null
    jq '.version = 2 | .resources //= [] | .identities //= [] | .roleAssignments //= [] | .images //= {}' \
        "${AFD_PLS_E2E_STATE_FILE}" >"${AFD_PLS_E2E_STATE_FILE}.next"
    mv "${AFD_PLS_E2E_STATE_FILE}.next" "${AFD_PLS_E2E_STATE_FILE}"
else
    jq -n \
        --arg subscriptionId "${EXPECTED_SUBSCRIPTION_ID}" \
        --arg runId "${AFD_PLS_E2E_RUN_ID}" \
        --arg resourceGroup "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --arg location "${AFD_PLS_E2E_LOCATION}" \
        '{version: 2, subscriptionId: $subscriptionId, runId: $runId, resourceGroup: $resourceGroup,
          location: $location, resources: [], identities: [], roleAssignments: [], images: {}}' \
        >"${AFD_PLS_E2E_STATE_FILE}"
fi

tags=("source=${SOURCE_TAG}" "phase=7" "run-id=${AFD_PLS_E2E_RUN_ID}")
az group create --name "${AFD_PLS_E2E_RESOURCE_GROUP}" --location "${AFD_PLS_E2E_LOCATION}" \
    --tags "${tags[@]}" --output none
resource_group_id="/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_RESOURCE_GROUP}"
record_resource resourceGroup "${AFD_PLS_E2E_RESOURCE_GROUP}" "${resource_group_id}"

if ! az acr show --name "${AFD_PLS_E2E_ACR}" --output none 2>/dev/null; then
    az acr create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${AFD_PLS_E2E_ACR}" \
        --location "${AFD_PLS_E2E_LOCATION}" --sku Basic --admin-enabled false --tags "${tags[@]}" --output none
fi
acr_id="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query id -o tsv)"
acr_login_server="$(az acr show --name "${AFD_PLS_E2E_ACR}" --query loginServer -o tsv)"
record_resource containerRegistry "${AFD_PLS_E2E_ACR}" "${acr_id}"
az acr login --name "${AFD_PLS_E2E_ACR}" --output none

cd "${REPO_ROOT}"
make REGISTRY="${acr_login_server}" TAG="${AFD_PLS_E2E_RUN_ID}" OUTPUT_TYPE=type=registry \
    docker-build-hub-gateway-controller-manager \
    docker-build-member-net-controller-manager \
    docker-build-net-crd-installer \
    docker-build-afd-pls-echo

resolve_image() {
    local repository="$1"
    local digest
    digest="$(az acr repository show --name "${AFD_PLS_E2E_ACR}" \
        --image "${repository}:${AFD_PLS_E2E_RUN_ID}" --query digest -o tsv)"
    [[ "${digest}" =~ ^sha256:[[:xdigit:]]{64}$ ]] || {
        echo "error: registry returned invalid digest for ${repository}" >&2
        return 1
    }
    printf '%s/%s@%s' "${acr_login_server}" "${repository}" "${digest}"
}

export AFD_PLS_E2E_HUB_IMAGE="$(resolve_image hub-gateway-controller-manager)"
export AFD_PLS_E2E_MEMBER_IMAGE="$(resolve_image member-net-controller-manager)"
export AFD_PLS_E2E_CRD_INSTALLER_IMAGE="$(resolve_image net-crd-installer)"
export AFD_PLS_E2E_ECHO_IMAGE="$(resolve_image afd-pls-echo)"
refresh_token_repository="ghcr.io/azure/fleet/refresh-token"
refresh_token_digest="$(docker buildx imagetools inspect "${refresh_token_repository}:v0.1.0" \
    --format '{{json .Manifest.Digest}}' | tr -d '"')"
[[ "${refresh_token_digest}" =~ ^sha256:[[:xdigit:]]{64}$ ]] || {
    echo "error: refresh-token image returned invalid digest" >&2
    exit 1
}
export AFD_PLS_E2E_REFRESH_TOKEN_IMAGE="${refresh_token_repository}@${refresh_token_digest}"
state_next="${AFD_PLS_E2E_STATE_FILE}.next"
jq --arg hub "${AFD_PLS_E2E_HUB_IMAGE}" --arg member "${AFD_PLS_E2E_MEMBER_IMAGE}" \
    --arg crd "${AFD_PLS_E2E_CRD_INSTALLER_IMAGE}" --arg echo "${AFD_PLS_E2E_ECHO_IMAGE}" \
    --arg refreshToken "${AFD_PLS_E2E_REFRESH_TOKEN_IMAGE}" \
    '.images = {hubGateway: $hub, member: $member, crdInstaller: $crd, echo: $echo, refreshToken: $refreshToken}' \
    "${AFD_PLS_E2E_STATE_FILE}" >"${state_next}"
mv "${state_next}" "${AFD_PLS_E2E_STATE_FILE}"

az network vnet create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${AFD_PLS_E2E_VNET}" \
    --location "${AFD_PLS_E2E_LOCATION}" --address-prefixes 10.70.0.0/16 \
    --subnet-name hub --subnet-prefixes 10.70.0.0/22 --output none
for spec in "member-1:10.70.4.0/22" "member-2:10.70.8.0/22" "pls-1:10.70.12.0/24" "pls-2:10.70.13.0/24"; do
    name="${spec%%:*}"
    prefix="${spec#*:}"
    az network vnet subnet create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --vnet-name "${AFD_PLS_E2E_VNET}" --name "${name}" --address-prefixes "${prefix}" \
        --disable-private-link-service-network-policies true --output none
done
vnet_id="$(az network vnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${AFD_PLS_E2E_VNET}" --query id -o tsv)"
record_resource virtualNetwork "${AFD_PLS_E2E_VNET}" "${vnet_id}"
for subnet in hub member-1 member-2 pls-1 pls-2; do
    subnet_id="$(az network vnet subnet show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --vnet-name "${AFD_PLS_E2E_VNET}" --name "${subnet}" --query id -o tsv)"
    record_resource subnet "${subnet}" "${subnet_id}"
done

tenant_id="$(az account show --query tenantId -o tsv)"
identity_specs=(
    "hub-gateway:afd-hub-id-${AFD_PLS_E2E_RUN_ID}"
    "member-1:afd-m1-id-${AFD_PLS_E2E_RUN_ID}"
    "member-2:afd-m2-id-${AFD_PLS_E2E_RUN_ID}"
)
for spec in "${identity_specs[@]}"; do
    identity_key="${spec%%:*}"
    identity_name="${spec#*:}"
    identity_json="$(az identity create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --name "${identity_name}" --location "${AFD_PLS_E2E_LOCATION}" --tags "${tags[@]}" -o json)"
    client_id="$(jq -r '.clientId' <<<"${identity_json}")"
    principal_id="$(jq -r '.principalId' <<<"${identity_json}")"
    identity_id="$(jq -r '.id' <<<"${identity_json}")"
    record_identity "${identity_name}" "${client_id}" "${principal_id}" "${identity_id}"
    printf -v "${identity_key//-/_}_client_id" '%s' "${client_id}"
    printf -v "${identity_key//-/_}_principal_id" '%s' "${principal_id}"
done

for cluster_spec in \
    "${AFD_PLS_E2E_HUB_CLUSTER}:hub:${AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_MEMBER1_CLUSTER}:member-1:${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" \
    "${AFD_PLS_E2E_MEMBER2_CLUSTER}:member-2:${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"; do
    cluster="${cluster_spec%%:*}"
    remainder="${cluster_spec#*:}"
    subnet="${remainder%%:*}"
    node_resource_group="${remainder#*:}"
    subnet_id="${vnet_id}/subnets/${subnet}"
    if ! az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" --output none 2>/dev/null; then
        az aks create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
            --location "${AFD_PLS_E2E_LOCATION}" --node-count 1 --node-vm-size Standard_D2as_v4 \
            --network-plugin azure --load-balancer-sku standard --vnet-subnet-id "${subnet_id}" --enable-managed-identity \
            --enable-oidc-issuer --enable-workload-identity --attach-acr "${AFD_PLS_E2E_ACR}" \
            --node-resource-group "${node_resource_group}" --generate-ssh-keys \
            --tags "${tags[@]}" --output none
    fi
    cluster_id="$(az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" --query id -o tsv)"
    record_resource managedCluster "${cluster}" "${cluster_id}"
    aks_identity_json="$(az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
        --query '{controlPlane:identity,kubelet:identityProfile.kubeletidentity}' -o json)"
    record_identity "${cluster}-control-plane" "" \
        "$(jq -r '.controlPlane.principalId' <<<"${aks_identity_json}")" "${cluster_id}"
    record_identity "${cluster}-kubelet" \
        "$(jq -r '.kubelet.clientId' <<<"${aks_identity_json}")" \
        "$(jq -r '.kubelet.objectId' <<<"${aks_identity_json}")" \
        "$(jq -r '.kubelet.resourceId' <<<"${aks_identity_json}")"
    az group update --name "${node_resource_group}" --tags "${tags[@]}" --output none
    node_resource_group_id="$(az group show --name "${node_resource_group}" --query id -o tsv)"
    record_resource nodeResourceGroup "${node_resource_group}" "${node_resource_group_id}"
done

create_federation() {
    local cluster="$1" identity="$2" service_account="$3"
    local issuer federation_id
    issuer="$(az aks show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
        --query oidcIssuerProfile.issuerUrl -o tsv)"
    if ! az identity federated-credential show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --identity-name "${identity}" --name "fleet-system-${AFD_PLS_E2E_RUN_ID}" --output none 2>/dev/null; then
        az identity federated-credential create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
            --identity-name "${identity}" --name "fleet-system-${AFD_PLS_E2E_RUN_ID}" \
            --issuer "${issuer}" --subject "system:serviceaccount:fleet-system:${service_account}" \
            --audiences api://AzureADTokenExchange --output none
    fi
    federation_id="$(az identity federated-credential show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --identity-name "${identity}" --name "fleet-system-${AFD_PLS_E2E_RUN_ID}" --query id -o tsv)"
    record_resource federatedCredential "${identity}/fleet-system-${AFD_PLS_E2E_RUN_ID}" "${federation_id}"
}
create_federation "${AFD_PLS_E2E_HUB_CLUSTER}" "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" "hub-gateway-controller-manager"
create_federation "${AFD_PLS_E2E_MEMBER1_CLUSTER}" "afd-m1-id-${AFD_PLS_E2E_RUN_ID}" "member-net-controller-manager-sa"
create_federation "${AFD_PLS_E2E_MEMBER2_CLUSTER}" "afd-m2-id-${AFD_PLS_E2E_RUN_ID}" "member-net-controller-manager-sa"

hub_role_id="$(az role definition list --name Contributor --query '[0].name' -o tsv)"
member_role_id="$(az role definition list --name 'Network Contributor' --query '[0].name' -o tsv)"
[[ -n "${hub_role_id}" && -n "${member_role_id}" ]] || {
    echo "error: required built-in Azure roles are unavailable" >&2
    exit 1
}

assign_role() {
    local principal_id="$1" role_id="$2" scope="$3" logical_name="$4"
    local assignment
    assignment="$(az role assignment create --assignee-object-id "${principal_id}" \
        --assignee-principal-type ServicePrincipal --role "${role_id}" --scope "${scope}" -o json)"
    record_role_assignment "${logical_name}" "${principal_id}" "${role_id}" "${scope}" "$(jq -r '.id' <<<"${assignment}")"
}
assign_role "${hub_gateway_principal_id}" "${hub_role_id}" "${resource_group_id}" hub-afd
assign_role "${member_1_principal_id}" "${member_role_id}" \
    "/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}" member-1-pls
assign_role "${member_2_principal_id}" "${member_role_id}" \
    "/subscriptions/${EXPECTED_SUBSCRIPTION_ID}/resourceGroups/${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}" member-2-pls

rm -f "${AFD_PLS_E2E_KUBECONFIG}"
for context_spec in \
    "${AFD_PLS_E2E_HUB_CLUSTER}:${AFD_PLS_E2E_HUB_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER1_CLUSTER}:${AFD_PLS_E2E_MEMBER1_CONTEXT}" \
    "${AFD_PLS_E2E_MEMBER2_CLUSTER}:${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    cluster="${context_spec%%:*}"
    context="${context_spec#*:}"
    az aks get-credentials --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" --name "${cluster}" \
        --admin --file "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" --overwrite-existing
    generated_context="${context}-admin"
    if kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" config get-contexts "${generated_context}" --no-headers >/dev/null 2>&1; then
        kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" config rename-context "${generated_context}" "${context}" >/dev/null
    fi
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" config get-contexts "${context}" --no-headers >/dev/null || {
        echo "error: expected kubeconfig context ${context} was not created" >&2
        exit 1
    }
done

k() {
    local context="$1"
    shift
    kubectl --kubeconfig "${AFD_PLS_E2E_KUBECONFIG}" --context "${context}" "$@"
}
for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" create namespace fleet-system --dry-run=client -o yaml | k "${context}" apply -f -
done

module_cache="$(go env GOMODCACHE)"
fleet_crd_dir="${module_cache}/go.goms.io/fleet@v0.14.0/config/crd/bases"
gateway_crd_dir="${module_cache}/sigs.k8s.io/gateway-api@v1.2.1/config/crd/standard"
k "${AFD_PLS_E2E_HUB_CONTEXT}" apply --server-side --field-manager=phase7-e2e \
    -f "${fleet_crd_dir}/cluster.kubernetes-fleet.io_memberclusters.yaml" \
    -f "${fleet_crd_dir}/cluster.kubernetes-fleet.io_internalmemberclusters.yaml" \
    -f "${gateway_crd_dir}"
for context in "${AFD_PLS_E2E_HUB_CONTEXT}" "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" apply --server-side --field-manager=phase7-e2e -f "${REPO_ROOT}/config/crd/bases"
done

az network front-door waf-policy create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --name "${AFD_PLS_E2E_WAF_POLICY}" --sku Premium_AzureFrontDoor --mode Prevention \
    --location Global --tags "${tags[@]}" --output none
if ! az network front-door waf-policy rule show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --policy-name "${AFD_PLS_E2E_WAF_POLICY}" --name BlockPOCHeader --output none 2>/dev/null; then
    az network front-door waf-policy rule create --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
        --policy-name "${AFD_PLS_E2E_WAF_POLICY}" --name BlockPOCHeader --priority 1 \
        --rule-type MatchRule --action Block --match-variable RequestHeader.X-POC-Block \
        --operator Equal --values true --output none
fi
waf_id="$(az network front-door waf-policy show --resource-group "${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --name "${AFD_PLS_E2E_WAF_POLICY}" --query id -o tsv)"
record_resource wafPolicy "${AFD_PLS_E2E_WAF_POLICY}" "${waf_id}"

registration_manifest="${AFD_PLS_E2E_ARTIFACT_DIR}/hub-member-registration.yaml"
HELM_NO_PLUGINS=1 helm template phase7-registration "${REPO_ROOT}/examples/getting-started/charts/hub" \
    --namespace fleet-system --set-string userNS=afd-pls-e2e \
    --set-string memberClusterConfigs[0].memberID="${AFD_PLS_E2E_MEMBER1_CLUSTER}" \
    --set-string memberClusterConfigs[0].principalID="${member_1_principal_id}" \
    --set-string memberClusterConfigs[1].memberID="${AFD_PLS_E2E_MEMBER2_CLUSTER}" \
    --set-string memberClusterConfigs[1].principalID="${member_2_principal_id}" \
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
create_member_cluster "${AFD_PLS_E2E_MEMBER1_CLUSTER}" "${member_1_principal_id}"
create_member_cluster "${AFD_PLS_E2E_MEMBER2_CLUSTER}" "${member_2_principal_id}"

hub_server="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}')"
hub_ca="$(k "${AFD_PLS_E2E_HUB_CONTEXT}" config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')"
hub_repo="${AFD_PLS_E2E_HUB_IMAGE%@*}"
hub_digest="${AFD_PLS_E2E_HUB_IMAGE##*@}"
member_repo="${AFD_PLS_E2E_MEMBER_IMAGE%@*}"
member_digest="${AFD_PLS_E2E_MEMBER_IMAGE##*@}"
crd_repo="${AFD_PLS_E2E_CRD_INSTALLER_IMAGE%@*}"
crd_digest="${AFD_PLS_E2E_CRD_INSTALLER_IMAGE##*@}"
refresh_token_repo="${AFD_PLS_E2E_REFRESH_TOKEN_IMAGE%@*}"
refresh_token_digest="${AFD_PLS_E2E_REFRESH_TOKEN_IMAGE##*@}"

HELM_NO_PLUGINS=1 helm template phase7 "${REPO_ROOT}/charts/hub-gateway-controller-manager" \
    --namespace fleet-system \
    --set-string image.repository="${hub_repo}" --set-string image.digest="${hub_digest}" \
    --set-string crdInstaller.image.repository="${crd_repo}" --set-string crdInstaller.image.digest="${crd_digest}" \
    --set-string azure.clientId="${hub_gateway_client_id}" --set-string azure.tenantId="${tenant_id}" \
    --set-string azure.subscriptionId="${EXPECTED_SUBSCRIPTION_ID}" \
    --set-string azure.resourceGroup="${AFD_PLS_E2E_RESOURCE_GROUP}" \
    --set-string azure.location="${AFD_PLS_E2E_LOCATION}" >"${AFD_PLS_E2E_HUB_MANIFEST}"

render_member() {
    local manifest="$1" member_name="$2" client_id="$3" node_rg="$4"
    HELM_NO_PLUGINS=1 helm template phase7 "${REPO_ROOT}/charts/member-net-controller-manager" \
        --namespace fleet-system \
        --set-string fullnameOverride=member-net-controller-manager \
        --set-string image.repository="${member_repo}" --set-string image.digest="${member_digest}" \
        --set crdInstaller.enabled=true --set-string crdInstaller.image.repository="${crd_repo}" \
        --set-string crdInstaller.image.digest="${crd_digest}" --set crdInstaller.isE2ETest=true \
        --set-string config.hubURL="${hub_server}" --set-string config.hubCA="${hub_ca}" \
        --set-string config.memberClusterName="${member_name}" --set-string config.provider=azure \
        --set-string refreshtoken.repository="${refresh_token_repo}" \
        --set-string refreshtoken.digest="${refresh_token_digest}" \
        --set-string resources.requests.cpu=25m --set-string resources.requests.memory=64Mi \
        --set tlsClientInsecure=false \
        --set-string azure.clientid="${client_id}" --set azure.workloadIdentityEnabled=true \
        --set enableTrafficManagerFeature=false \
        --set enableAFDPrivateLinkFeature=true --set-string afdRequesterSubscriptionAllowlist="${EXPECTED_SUBSCRIPTION_ID}" \
        --set-string azureCloudConfig.tenantId="${tenant_id}" \
        --set-string azureCloudConfig.subscriptionId="${EXPECTED_SUBSCRIPTION_ID}" \
        --set-string azureCloudConfig.aadClientId="${client_id}" \
        --set azureCloudConfig.useFederatedWorkloadIdentityExtension=true \
        --set-string azureCloudConfig.resourceGroup="${node_rg}" \
        --set-string azureCloudConfig.location="${AFD_PLS_E2E_LOCATION}" >"${manifest}"
}
render_member "${AFD_PLS_E2E_MEMBER1_MANIFEST}" "${AFD_PLS_E2E_MEMBER1_CLUSTER}" \
    "${member_1_client_id}" "${AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP}"
render_member "${AFD_PLS_E2E_MEMBER2_MANIFEST}" "${AFD_PLS_E2E_MEMBER2_CLUSTER}" \
    "${member_2_client_id}" "${AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP}"

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
        image: ${AFD_PLS_E2E_ECHO_IMAGE}
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

cat >>"${AFD_PLS_E2E_HUB_MANIFEST}" <<EOF
---
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

for manifest in "${AFD_PLS_E2E_HUB_MANIFEST}" "${AFD_PLS_E2E_MEMBER1_MANIFEST}" "${AFD_PLS_E2E_MEMBER2_MANIFEST}"; do
    if grep -E '^[[:space:]]*image:' "${manifest}" |
        grep -Ev '@sha256:[[:xdigit:]]{64}"?[[:space:]]*$' >/dev/null; then
        echo "error: rendered workload image is not digest-pinned in ${manifest}" >&2
        exit 1
    fi
done
k "${AFD_PLS_E2E_HUB_CONTEXT}" apply --server-side --field-manager=phase7-e2e -f "${AFD_PLS_E2E_HUB_MANIFEST}"
k "${AFD_PLS_E2E_MEMBER1_CONTEXT}" apply --server-side --field-manager=phase7-e2e -f "${AFD_PLS_E2E_MEMBER1_MANIFEST}"
k "${AFD_PLS_E2E_MEMBER2_CONTEXT}" apply --server-side --field-manager=phase7-e2e -f "${AFD_PLS_E2E_MEMBER2_MANIFEST}"

k "${AFD_PLS_E2E_HUB_CONTEXT}" -n fleet-system wait --for=condition=Available \
    deployment/hub-gateway-controller-manager --timeout=20m
for context in "${AFD_PLS_E2E_MEMBER1_CONTEXT}" "${AFD_PLS_E2E_MEMBER2_CONTEXT}"; do
    k "${context}" -n fleet-system wait --for=condition=Available \
        deployment/member-net-controller-manager --timeout=20m
    k "${context}" -n afd-pls-e2e wait --for=condition=Available deployment/echo --timeout=20m
done

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
    for attempt in $(seq 1 60); do
        if k "${AFD_PLS_E2E_HUB_CONTEXT}" -n "${namespace}" get internalmembercluster "${member_name}" \
            -o json | jq -e '
                any(.status.agentStatus[]?;
                    .type == "ServiceExportImportAgent" and
                    (.lastReceivedHeartbeat != null) and
                    any(.conditions[]?; .type == "Joined" and .status == "True"))
            ' >/dev/null; then
            now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
            k "${AFD_PLS_E2E_HUB_CONTEXT}" patch membercluster "${member_name}" --subresource=status \
                --type=merge -p "{\"status\":{\"conditions\":[{\"type\":\"Joined\",\"status\":\"True\",\"reason\":\"NetworkingAgentJoined\",\"message\":\"Phase 7 networking agent joined through the existing E2E registration path\",\"lastTransitionTime\":\"${now}\"}]}}"
            return
        fi
        sleep 10
    done
    echo "error: networking agent for ${member_name} did not join within 10 minutes" >&2
    return 1
}
join_member "${AFD_PLS_E2E_MEMBER1_CLUSTER}"
join_member "${AFD_PLS_E2E_MEMBER2_CLUSTER}"

state_next="${AFD_PLS_E2E_STATE_FILE}.next"
assignments="$(az role assignment list --all --query "[?contains(scope, '${AFD_PLS_E2E_RESOURCE_GROUP}')].{id:id,name:name,role:roleDefinitionName,principalId:principalId,scope:scope}" -o json)"
jq --argjson assignments "${assignments}" '.discoveredRoleAssignments = $assignments' \
    "${AFD_PLS_E2E_STATE_FILE}" >"${state_next}"
mv "${state_next}" "${AFD_PLS_E2E_STATE_FILE}"

trap - EXIT INT TERM
echo "setup complete; follow the human validation checklist in test/e2e/afdprivatelink/README.md"
