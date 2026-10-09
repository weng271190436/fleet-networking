# Global Service with Azure Front Door: customer quickstart

[Previous: infrastructure setup](01-infrastructure-setup.md) ·
[Next: lifecycle validation](03-lifecycle-validation.md)

This quickstart deploys the same application to two Fleet member clusters and exposes it through
one global Azure Front Door endpoint. Fleet discovers the private origins, configures Azure Front
Door Premium, approves the managed Private Link connections, and reports the endpoint through
Gateway API status.

When you finish, normal requests will return `200` from both member clusters, while a request
matching the sample WAF rule will return `403`.

## Prerequisites

Your platform administrator must complete
[Part 1: infrastructure setup](01-infrastructure-setup.md) on this checkout and give you the run
ID. Part 1 creates the run-scoped kubeconfig and state file that this quickstart loads. It also
installs the `azure-fleet-afd` `GatewayClass` and prepares a digest-pinned sample image.

Part 1 also grants the hub Gateway controller read access to the member-cluster networking
resource groups. You do not need to create Azure role assignments or approve Private Link
connections.

Install the following command-line tools:

- [Azure CLI](https://learn.microsoft.com/cli/azure/install-azure-cli)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [curl](https://curl.se/download.html)
- [jq](https://jqlang.github.io/jq/download/)

## 1. Set the run ID

From the root of this repository, change only the run ID:

```bash
export AFD_PLS_E2E_RUN_ID="replace-with-your-run-id"

if ! declare -F initialize_names >/dev/null; then
  source test/e2e/afdprivatelink/scripts/common.sh
fi
initialize_names

export KUBECONFIG="$AFD_PLS_E2E_KUBECONFIG"
export HUB_CONTEXT="$AFD_PLS_E2E_HUB_CONTEXT"
export MEMBER_1_CONTEXT="$AFD_PLS_E2E_MEMBER1_CONTEXT"
export MEMBER_2_CONTEXT="$AFD_PLS_E2E_MEMBER2_CONTEXT"
export APP_IMAGE="$AFD_PLS_E2E_ECHO_IMAGE"

export AZURE_SUBSCRIPTION_ID="$EXPECTED_SUBSCRIPTION_ID"
export WAF_RESOURCE_GROUP="$AFD_PLS_E2E_RESOURCE_GROUP"
export WAF_POLICY_NAME="$AFD_PLS_E2E_WAF_POLICY"

test -f "$KUBECONFIG"
test -f "$AFD_PLS_E2E_STATE_FILE"
[[ "$APP_IMAGE" =~ @sha256:[[:xdigit:]]{64}$ ]]
```

The three checks produce no output when the Part 1 handoff is ready.

Select the fixed POC subscription and confirm that all three clusters are reachable:

```bash
az account set --subscription "$AZURE_SUBSCRIPTION_ID"

for context in "$HUB_CONTEXT" "$MEMBER_1_CONTEXT" "$MEMBER_2_CONTEXT"; do
  echo "=== $context ==="
  kubectl --context "$context" cluster-info
done
```

## 2. Create the WAF policy

This sample policy blocks requests containing the header `X-POC-Block: true`.

```bash
if ! az network front-door waf-policy show \
    --resource-group "$WAF_RESOURCE_GROUP" \
    --name "$WAF_POLICY_NAME" \
    --output none 2>/dev/null; then
  az network front-door waf-policy create \
    --resource-group "$WAF_RESOURCE_GROUP" \
    --name "$WAF_POLICY_NAME" \
    --sku Premium_AzureFrontDoor \
    --mode Prevention \
    --location Global \
    --output none
fi

if ! az network front-door waf-policy rule show \
    --resource-group "$WAF_RESOURCE_GROUP" \
    --policy-name "$WAF_POLICY_NAME" \
    --name BlockPOCHeader \
    --output none 2>/dev/null; then
  az network front-door waf-policy rule create \
    --resource-group "$WAF_RESOURCE_GROUP" \
    --policy-name "$WAF_POLICY_NAME" \
    --name BlockPOCHeader \
    --priority 1 \
    --rule-type MatchRule \
    --action Block \
    --match-variable RequestHeader.X-POC-Block \
    --operator Equal \
    --values true \
    --output none
fi

export WAF_POLICY_ID="$(
  az network front-door waf-policy show \
    --resource-group "$WAF_RESOURCE_GROUP" \
    --name "$WAF_POLICY_NAME" \
    --query id \
    --output tsv
)"
echo "$WAF_POLICY_ID"
```

The output must be the Azure resource ID of `global-service-waf`.

The WAF policy is application-owned. Fleet attaches it to the generated Azure Front Door
configuration but does not update or delete the policy.

## 3. Deploy the application to both member clusters

The helper below deploys one echo application and one internal LoadBalancer Service. The Service
annotations tell AKS to create a Private Link Service on the platform-provided PLS subnet.

```bash
deploy_member() {
  local context="$1"
  local member_name="$2"
  local pls_subnet="$3"

  kubectl --context "$context" apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: afd-pls-e2e
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
  namespace: afd-pls-e2e
spec:
  replicas: 1
  selector:
    matchLabels:
      app: echo
  template:
    metadata:
      labels:
        app: echo
    spec:
      containers:
      - name: echo
        image: ${APP_IMAGE}
        env:
        - name: MEMBER_NAME
          value: ${member_name}
        ports:
        - containerPort: 8080
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
    service.beta.kubernetes.io/azure-pls-visibility: "*"
spec:
  type: LoadBalancer
  selector:
    app: echo
  ports:
  - name: http
    port: 80
    targetPort: 8080
EOF
}

deploy_member "$MEMBER_1_CONTEXT" member-1 pls-1
deploy_member "$MEMBER_2_CONTEXT" member-2 pls-2
```

Wait for both Deployments:

```bash
for context in "$MEMBER_1_CONTEXT" "$MEMBER_2_CONTEXT"; do
  kubectl --context "$context" \
    --namespace afd-pls-e2e \
    wait --for=condition=Available deployment/echo --timeout=20m
done
```

Wait for each internal load balancer to receive a private IP:

```bash
wait_for_service_ip() {
  local context="$1"
  local ip=""

  for _ in $(seq 1 120); do
    ip="$(
      kubectl --context "$context" \
        --namespace afd-pls-e2e \
        get service/echo \
        --output jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null
    )"
    if [[ -n "$ip" ]]; then
      echo "$context: $ip"
      return 0
    fi
    sleep 10
  done

  echo "Timed out waiting for service/echo in $context" >&2
  return 1
}

wait_for_service_ip "$MEMBER_1_CONTEXT"
wait_for_service_ip "$MEMBER_2_CONTEXT"
```

Both commands must print a private IP address.

## 4. Create the global Gateway

Create a `MultiClusterBackend` that selects the two member clusters, then route the Gateway to
that backend:

```bash
kubectl --context "$HUB_CONTEXT" apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: afd-pls-e2e
---
apiVersion: networking.fleet.azure.com/v1alpha1
kind: MultiClusterBackend
metadata:
  name: echo
  namespace: afd-pls-e2e
spec:
  service:
    name: echo
    port: 80
  clusterSelector:
    matchLabels:
      networking.fleet.azure.com/afd-poc: "true"
  healthProbe:
    path: /
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: global
  namespace: afd-pls-e2e
  annotations:
    networking.fleet.azure.com/afd-sku: Premium_AzureFrontDoor
    networking.fleet.azure.com/afd-waf-policy-id: "${WAF_POLICY_ID}"
spec:
  gatewayClassName: azure-fleet-afd
  listeners:
  - name: http
    protocol: HTTP
    port: 80
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: echo
  namespace: afd-pls-e2e
spec:
  parentRefs:
  - name: global
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /
    backendRefs:
    - group: networking.fleet.azure.com
      kind: MultiClusterBackend
      name: echo
EOF
```

Fleet now:

1. selects the labeled member clusters;
2. discovers each Service, internal load balancer, and Private Link Service;
3. creates the Azure Front Door Premium resource graph;
4. correlates and approves the AFD-managed Private Link connections; and
5. publishes the Front Door hostname in Gateway status.

No manual Private Link approval is required.

## 5. Wait for the global Service

Programming Azure Front Door and its managed Private Link connections can take several minutes:

```bash
kubectl --context "$HUB_CONTEXT" \
  --namespace afd-pls-e2e \
  wait --for=condition=Programmed \
  multiclusterbackend.networking.fleet.azure.com/echo \
  --timeout=30m

kubectl --context "$HUB_CONTEXT" \
  --namespace afd-pls-e2e \
  wait --for=condition=Programmed \
  gateway/global \
  --timeout=30m
```

Confirm that both member origins are ready:

```bash
kubectl --context "$HUB_CONTEXT" \
  --namespace afd-pls-e2e \
  get multiclusterbackend/echo \
  --output jsonpath='{.status.selectedClusters}/{.status.readyOrigins}{"\n"}'
```

Expected output:

```text
2/2
```

Get the public hostname:

```bash
export HOSTNAME="$(
  kubectl --context "$HUB_CONTEXT" \
    --namespace afd-pls-e2e \
    get gateway/global \
    --output jsonpath='{.status.addresses[0].value}'
)"
echo "https://${HOSTNAME}/"
```

Open the printed URL in a browser. Refresh several times; the responses should eventually identify
both `member-1` and `member-2`.

## 6. Verify traffic and WAF protection

Send requests until both member clusters respond:

```bash
declare -A SEEN=()

for _ in $(seq 1 60); do
  body="$(
    curl --fail --silent --show-error --max-time 30 "https://${HOSTNAME}/" |
      tr -d '\r\n'
  )"
  echo "$body"
  case "$body" in
    member-1|member-2)
      SEEN["$body"]=1
      ;;
    *)
      echo "Unexpected response: $body" >&2
      exit 1
      ;;
  esac

  if [[ -n "${SEEN[member-1]:-}" && -n "${SEEN[member-2]:-}" ]]; then
    break
  fi
  sleep 5
done

printf 'member-1=%s member-2=%s\n' \
  "${SEEN[member-1]:-0}" \
  "${SEEN[member-2]:-0}"
```

Expected final output:

```text
member-1=1 member-2=1
```

Verify the WAF policy:

```bash
curl --silent --show-error --max-time 30 \
  --output /dev/null \
  --write-out '%{http_code}\n' \
  "https://${HOSTNAME}/"

curl --silent --show-error --max-time 30 \
  --output /dev/null \
  --header 'X-POC-Block: true' \
  --write-out '%{http_code}\n' \
  "https://${HOSTNAME}/"
```

Expected outputs, in order:

```text
200
403
```

Your global Service is now available through Azure Front Door with two private member-cluster
origins and application-owned WAF protection.

## Troubleshooting

- If a Deployment does not become available, run
  `kubectl --context "$MEMBER_1_CONTEXT" -n afd-pls-e2e get pods` and repeat with
  `"$MEMBER_2_CONTEXT"`.
- If the backend or Gateway does not become programmed, inspect
  `kubectl --context "$HUB_CONTEXT" -n afd-pls-e2e describe multiclusterbackend/echo` and
  `kubectl --context "$HUB_CONTEXT" -n afd-pls-e2e describe gateway/global`.
- Do not manually approve an AFD Private Link connection. The member networking controller
  approves only requests that match an active Fleet assignment.
- Continue to [Part 3: lifecycle validation](03-lifecycle-validation.md) for member withdrawal,
  fail-static, and Gateway deletion scenarios.

[Previous: infrastructure setup](01-infrastructure-setup.md) ·
[Next: lifecycle validation](03-lifecycle-validation.md)
