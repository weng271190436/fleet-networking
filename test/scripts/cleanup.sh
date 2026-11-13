#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail
set -x

if [[ "${ENABLE_SERVICE_GATEWAY_PROOF:-false}" == true ]]; then
  echo "error: Service Gateway environments require explicit inspected cleanup; automatic resource-group deletion is disabled" >&2
  exit 1
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
