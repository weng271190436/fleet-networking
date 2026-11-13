#!/bin/bash

export ENABLE_TRAFFIC_MANAGER="${ENABLE_TRAFFIC_MANAGER:-false}"
export ENABLE_AZURE_FRONT_DOOR="${ENABLE_AZURE_FRONT_DOOR:-false}"
export RETAIN_AFD_E2E_SETUP="${RETAIN_AFD_E2E_SETUP:-false}"
export AZURE_NETWORK_SETTING="${AZURE_NETWORK_SETTING:-shared-vnet}"
export ENABLE_SERVICE_GATEWAY_PROOF="${ENABLE_SERVICE_GATEWAY_PROOF:-false}"

service_gateway_principal_id() {
  local kind="${SERVICE_GATEWAY_PRINCIPAL_TYPE:-User}"
  local object_id="${SERVICE_GATEWAY_PRINCIPAL_OBJECT_ID:-${SERVICE_GATEWAY_USER_OBJECT_ID:-}}"
  if [[ "$kind" != User && "$kind" != ServicePrincipal ]]; then
    echo "error: SERVICE_GATEWAY_PRINCIPAL_TYPE must be User or ServicePrincipal" >&2
    return 1
  fi
  if [[ -z "$object_id" && "$kind" == ServicePrincipal ]]; then
    # Read only the identity claim from the trusted CLI's ARM token locally.
    # Never print/store the token or query Microsoft Graph for name resolution.
    object_id=$(az account get-access-token --subscription "$AZURE_SUBSCRIPTION_ID" \
      --resource https://management.azure.com/ --query accessToken -o tsv |
      python3 -c 'import sys,json,base64
try:
 p=sys.stdin.read().strip().split(".")
 claims=json.loads(base64.urlsafe_b64decode(p[1]+"="*(-len(p[1])%4)))
 if claims.get("idtyp")=="user": raise ValueError()
 print(claims["oid"])
except (IndexError,KeyError,ValueError,TypeError):
 sys.exit("Cannot identify the CI service principal from the ARM token")') || return
  fi
  if [[ ! "$object_id" =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]]; then
    echo "error: provide SERVICE_GATEWAY_PRINCIPAL_OBJECT_ID (or legacy SERVICE_GATEWAY_USER_OBJECT_ID) as an Entra Object ID" >&2
    return 1
  fi
  printf '%s\n' "$object_id"
}

if [[ "$ENABLE_SERVICE_GATEWAY_PROOF" != true && "$ENABLE_SERVICE_GATEWAY_PROOF" != false ]]; then
  echo "error: ENABLE_SERVICE_GATEWAY_PROOF must be true or false" >&2
  return 1
fi
if [[ "$ENABLE_SERVICE_GATEWAY_PROOF" == true ]]; then
  if [[ "$ENABLE_AZURE_FRONT_DOOR" != false || "$ENABLE_TRAFFIC_MANAGER" != false ||
        "$RETAIN_AFD_E2E_SETUP" != false || "$AZURE_NETWORK_SETTING" != shared-vnet ]]; then
    echo "error: Service Gateway proof requires an isolated shared-vnet scenario without AFD or Traffic Manager" >&2
    return 1
  fi
  if [[ ! "${AZURE_SUBSCRIPTION_ID:-}" =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ||
        ! "${AZURE_RESOURCE_GROUP:-}" =~ ^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$ ]]; then
    echo "error: Service Gateway proof requires AZURE_SUBSCRIPTION_ID and a fresh alphanumeric/hyphen AZURE_RESOURCE_GROUP (1-63 characters)" >&2
    return 1
  fi
  if [[ "${HUB_CLUSTER:-hub}" != hub || "${MEMBER_CLUSTER_1:-member-1}" != member-1 ||
        "${MEMBER_CLUSTER_2:-member-2}" != member-2 ]]; then
    echo "error: Service Gateway proof requires default hub/member names" >&2
    return 1
  fi
  export SERVICE_GATEWAY_STATE_DIR="$HOME/.local/state/fleet-networking/service-gateway/$AZURE_SUBSCRIPTION_ID/$AZURE_RESOURCE_GROUP"
  export KUBECONFIG="$SERVICE_GATEWAY_STATE_DIR/kubeconfig"
  export SERVICE_GATEWAY_PROOF_CONFIG="$SERVICE_GATEWAY_STATE_DIR/network.json"
fi

if [[ "$ENABLE_AZURE_FRONT_DOOR" != true && "$ENABLE_AZURE_FRONT_DOOR" != false ]]; then
  echo "error: ENABLE_AZURE_FRONT_DOOR must be true or false" >&2
  return 1
fi
export ENABLE_AFD_PRIVATE_LINK_AUTO_APPROVAL="${ENABLE_AFD_PRIVATE_LINK_AUTO_APPROVAL:-$ENABLE_AZURE_FRONT_DOOR}"
if [[ "$ENABLE_AFD_PRIVATE_LINK_AUTO_APPROVAL" != true && "$ENABLE_AFD_PRIVATE_LINK_AUTO_APPROVAL" != false ]]; then
  echo "error: ENABLE_AFD_PRIVATE_LINK_AUTO_APPROVAL must be true or false" >&2
  return 1
fi

if [[ "$RETAIN_AFD_E2E_SETUP" != true && "$RETAIN_AFD_E2E_SETUP" != false ]]; then
  echo "error: RETAIN_AFD_E2E_SETUP must be true or false" >&2
  return 1
fi
if [[ "$RETAIN_AFD_E2E_SETUP" == true && "$ENABLE_AZURE_FRONT_DOOR" != true ]]; then
  echo "error: RETAIN_AFD_E2E_SETUP requires ENABLE_AZURE_FRONT_DOOR=true" >&2
  return 1
fi

if [[ "$ENABLE_AZURE_FRONT_DOOR" == true ]]; then
  if [[ "$AZURE_NETWORK_SETTING" != shared-vnet ]]; then
    echo "error: ENABLE_AZURE_FRONT_DOOR requires shared-vnet" >&2
    return 1
  fi
  if [[ -z "${AZURE_SUBSCRIPTION_ID:-}" || -z "${AZURE_RESOURCE_GROUP:-}" ]]; then
    echo "error: AFD requires AZURE_SUBSCRIPTION_ID and AZURE_RESOURCE_GROUP" >&2
    return 1
  fi
  if [[ "${HUB_CLUSTER:-hub}" != hub || "${MEMBER_CLUSTER_1:-member-1}" != member-1 ||
        "${MEMBER_CLUSTER_2:-member-2}" != member-2 ]]; then
    echo "error: AFD proof requires default hub/member cluster names" >&2
    return 1
  fi
  export ENABLE_GATEWAY_API=true
  export ENABLE_AFD_PRIVATE_HTTP_PROOF=true
  # Derive the same candidate in separate setup/test shells; do not rely on child exports.
  export REGISTRY="${AZURE_RESOURCE_GROUP//-}.azurecr.io"
  if [[ -z "${TAG:-}" ]]; then
    TAG="$(git rev-parse --short=7 HEAD)" || return 1
    export TAG
  fi
  export AFD_PROOF_CONTROLLER_NAMESPACE="${AFD_PROOF_CONTROLLER_NAMESPACE:-fleet-system}"
  export AFD_PROOF_CONTROLLER_DEPLOYMENT="${AFD_PROOF_CONTROLLER_DEPLOYMENT:-hub-gateway-controller-manager}"
  export AFD_PROOF_CONTROLLER_IMAGE="${REGISTRY}/${HUB_GATEWAY_CONTROLLER_MANAGER_IMAGE_NAME:-hub-gateway-controller-manager}:${HUB_GATEWAY_CONTROLLER_MANAGER_IMAGE_VERSION:-$TAG}"
fi
