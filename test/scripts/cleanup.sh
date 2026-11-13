#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail
set -x

if [[ "${ENABLE_SERVICE_GATEWAY_PROOF:-false}" == true ]]; then
  if [[ "${SERVICE_GATEWAY_CLEANUP_APPROVED:-false}" != true ]]; then
    echo "error: Service Gateway environments require explicit inspected cleanup; automatic resource-group deletion is disabled" >&2
    exit 1
  fi
  source "$(dirname "${BASH_SOURCE[0]}")/e2e-config.sh"
  export SERVICE_GATEWAY_PROOF_CONFIG="$SERVICE_GATEWAY_STATE_DIR/proof-network.json"
  for file in setup-complete report.json proof-network.json; do
    if [[ ! -f "$SERVICE_GATEWAY_STATE_DIR/$file" ]]; then
      echo "error: missing successful Service Gateway proof evidence: $file; retaining environment" >&2
      exit 1
    fi
  done
  grep -Fx "subscription=$AZURE_SUBSCRIPTION_ID" "$SERVICE_GATEWAY_STATE_DIR/setup-complete"
  grep -Fx "resourceGroup=$AZURE_RESOURCE_GROUP" "$SERVICE_GATEWAY_STATE_DIR/setup-complete"
  jq -e 'length==1 and all(.[];
    .SuiteSucceeded==true and
    ([.SpecReports[] | select(
      (([.ContainerHierarchyLabels[]?[]?,.LeafNodeLabels[]?]|index("service-gateway-proof"))!=null) and
      .State=="passed")]|length)==1)
  ' "$SERVICE_GATEWAY_STATE_DIR/report.json" >/dev/null
  base="/subscriptions/$AZURE_SUBSCRIPTION_ID/resourceGroups/$AZURE_RESOURCE_GROUP"
  jq -e --arg base "${base,,}/providers/microsoft.network/" '
    (.subnetIDs|length)>0 and
    all([.gatewayID,.natGatewayID,.egressPublicIPID,.vnetID,.subnetIDs[]][];
      ascii_downcase|startswith($base))
  ' "$SERVICE_GATEWAY_PROOF_CONFIG" >/dev/null
  for subnet in $(jq -er '.subnetIDs[]' "$SERVICE_GATEWAY_PROOF_CONFIG"); do
    az rest --subscription "$AZURE_SUBSCRIPTION_ID" --resource https://management.azure.com/ \
      --method get --url "https://management.azure.com$subnet?api-version=2025-01-01" -o json |
      jq -e '.properties.provisioningState=="Succeeded" and
        (.properties.serviceGateway.id // "")==""' >/dev/null
  done
  az resource list --subscription "$AZURE_SUBSCRIPTION_ID" -g "$AZURE_RESOURCE_GROUP" -o json |
    jq -e --slurpfile config "$SERVICE_GATEWAY_PROOF_CONFIG" '
      ([$config[0].gatewayID,$config[0].natGatewayID,$config[0].egressPublicIPID] |
        map(ascii_downcase)) as $ids |
      all(.[]; (.id|ascii_downcase) as $id | ($ids|index($id))==null)
    ' >/dev/null
  group=$(az group show --subscription "$AZURE_SUBSCRIPTION_ID" -n "$AZURE_RESOURCE_GROUP" -o json)
  jq -e --arg id "${base,,}" '.tags.purpose=="fleet-service-gateway-e2e" and
    (.id|ascii_downcase)==$id' <<<"$group" >/dev/null
  az group delete --subscription "$AZURE_SUBSCRIPTION_ID" -n "$AZURE_RESOURCE_GROUP" --yes --no-wait
  exit 0
fi

case "${RETAIN_AFD_E2E_SETUP:-false}" in
  true)
    echo "error: AFD setup is retained; clear RETAIN_AFD_E2E_SETUP only when explicit cleanup is authorized" >&2
    exit 1
    ;;
  false) ;;
  *)
    echo "error: RETAIN_AFD_E2E_SETUP must be true or false" >&2
    exit 1
    ;;
esac

[[ -z "${AZURE_RESOURCE_GROUP}" ]] && echo "AZURE_RESOURCE_GROUP is not set" && exit 1

az group show --name "$AZURE_RESOURCE_GROUP" && az group delete --name "$AZURE_RESOURCE_GROUP" --no-wait --yes
