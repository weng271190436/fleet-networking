#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

# shellcheck source=test/scripts/e2e-config.sh
source "$(dirname "${BASH_SOURCE[0]}")/e2e-config.sh"

if [[ "$ENABLE_SERVICE_GATEWAY_PROOF" == true ]]; then
  if [[ ! -f "$SERVICE_GATEWAY_STATE_DIR/setup-complete" ||
        ! -f "$SERVICE_GATEWAY_PROOF_CONFIG" || ! -f "$KUBECONFIG" ]]; then
    echo "error: run make e2e-setup successfully for this Service Gateway resource group first" >&2
    exit 1
  fi
  # A resource-ID recreation after teardown failed live egress, despite ARM
  # success. Each independent proof gets new root identities, like its K8s UIDs.
  command -v flock >/dev/null || { echo "error: flock is required to serialize subnet cutover proofs" >&2; exit 1; }
  exec 9>"$SERVICE_GATEWAY_STATE_DIR/proof.lock"
  flock -n 9 || { echo "error: another Service Gateway proof is using this environment" >&2; exit 1; }
  run_id=$(python3 -c 'import uuid; print(uuid.uuid4().hex)')
  jq --arg suffix "$run_id" '
    def fresh: split("/") | .[-1]=("proof-"+$suffix) | join("/");
    .gatewayID |= fresh | .natGatewayID |= fresh | .egressPublicIPID |= fresh
  ' "$SERVICE_GATEWAY_PROOF_CONFIG" > "$SERVICE_GATEWAY_STATE_DIR/proof-network.json"
  cp "$SERVICE_GATEWAY_STATE_DIR/proof-network.json" "$SERVICE_GATEWAY_STATE_DIR/proof-network-$run_id.json"
  export SERVICE_GATEWAY_PROOF_CONFIG="$SERVICE_GATEWAY_STATE_DIR/proof-network.json"
  if [[ -f "$SERVICE_GATEWAY_STATE_DIR/report.json" ]]; then
    mv "$SERVICE_GATEWAY_STATE_DIR/report.json" "$SERVICE_GATEWAY_STATE_DIR/report.previous.json"
  fi
  echo "Service Gateway proof network configuration: $SERVICE_GATEWAY_PROOF_CONFIG"
  exec go test -v -count=1 -tags=e2e -timeout=130m ./test/e2e \
    -args -ginkgo.v -ginkgo.label-filter=service-gateway-proof \
    -ginkgo.timeout=125m "-ginkgo.json-report=$SERVICE_GATEWAY_STATE_DIR/report.json"
fi

if [[ "$ENABLE_AZURE_FRONT_DOOR" == true ]]; then
  if [[ "$RETAIN_AFD_E2E_SETUP" == true ]]; then
    echo "AFD retained diagnostic: withdrawal and cleanup are not run; resources require explicit later cleanup."
    exec go test -count=1 -timeout 10h -tags=e2e -v ./test/e2e -args -ginkgo.v -ginkgo.timeout=9h30m -ginkgo.label-filter=afd-private-http-proof
  fi
  exec go test -count=1 -timeout 10h -tags=e2e -v ./test/e2e -args -ginkgo.v -ginkgo.timeout=9h30m
fi
exec go test -timeout 50m -tags=e2e -v ./test/e2e -args -ginkgo.v
