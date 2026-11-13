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
  exec go test -count=1 -tags=e2e -timeout=130m ./test/e2e \
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
