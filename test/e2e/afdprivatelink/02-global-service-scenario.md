# Part 2: global Service application scenario

[Previous: infrastructure setup](01-infrastructure-setup.md) ·
[Next: lifecycle validation](03-lifecycle-validation.md)

> **Status: Validated.** Retained run `p7-10082105` completed two private-origin approval, Gateway
> programming, two-member HTTP 200 sampling, and the WAF HTTP 403 assertion. Lifecycle evidence and
> final cleanup remain, so Phase 7 is not complete.

Part 1 must be complete. In a new shell, export the same approved run values before using any run
variable, then initialize names and the context-aware Kubernetes helper. Do not copy credentials or
generated tokens between shells.

```bash
cd /home/weiweng/fleet-networking
export AZURE_SUBSCRIPTION_ID=d712bfad-d238-486f-8f1b-bf61a831b712
export AFD_PLS_E2E_RUN_ID="replace-with-the-approved-run-id"
export AFD_PLS_E2E_LOCATION=eastus2
export AFD_PLS_E2E_APPROVED=true
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names
validate_subscription
tags=("source=${SOURCE_TAG}" "phase=7" "run-id=${AFD_PLS_E2E_RUN_ID}")
k() {
  local context="$1"
  shift
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" "$@"
}
test "$AFD_PLS_E2E_APPROVED" = true
```

## 1. Pre-create the application WAF policy

**READ ONLY — inspect retained WAF objects and rendering inputs:**

```bash
waf_json="$(az network front-door waf-policy show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "$AFD_PLS_E2E_WAF_POLICY" -o json 2>/dev/null || true)"
if [[ -n "$waf_json" ]]; then
  validate_resource_tags "$(jq -r '.tags.source // ""' <<<"$waf_json")" \
    "$(jq -r '.tags["run-id"] // ""' <<<"$waf_json")" "WAF $AFD_PLS_E2E_WAF_POLICY"
  test "$(jq -r '.sku.name' <<<"$waf_json")" = Premium_AzureFrontDoor
  test "$(jq -r '.policySettings.mode' <<<"$waf_json")" = Prevention
fi
rule_json="$(az network front-door waf-policy rule show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --policy-name "$AFD_PLS_E2E_WAF_POLICY" -n BlockPOCHeader -o json 2>/dev/null || true)"
if [[ -n "$rule_json" ]]; then
  test "$(jq -r '.priority' <<<"$rule_json")" = 1
  test "$(jq -r '.action' <<<"$rule_json")" = Block
fi
jq -e '.images | length == 5 and all(.[]; contains("@sha256:"))' "$AFD_PLS_E2E_STATE_FILE"
```

Expected: absent WAF objects print nothing; retained objects validate exactly; image check is true.

**MUTATING — conditionally create the WAF policy and rule:**

```bash
test "$AFD_PLS_E2E_APPROVED" = true
if [[ -z "$waf_json" ]]; then
  az network front-door waf-policy create -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    -n "$AFD_PLS_E2E_WAF_POLICY" --sku Premium_AzureFrontDoor --mode Prevention \
    --location Global --tags "${tags[@]}" --output none
fi
if [[ -z "$rule_json" ]]; then
  az network front-door waf-policy rule create -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    --policy-name "$AFD_PLS_E2E_WAF_POLICY" -n BlockPOCHeader --priority 1 \
    --rule-type MatchRule --action Block --match-variable RequestHeader.X-POC-Block \
    --operator Equal --values true --output none
fi
waf_id="$(az network front-door waf-policy show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  -n "$AFD_PLS_E2E_WAF_POLICY" --query id -o tsv)"
record_resource wafPolicy "$AFD_PLS_E2E_WAF_POLICY" "$waf_id"
```

Load the immutable echo image and choose separate application manifests. Do not append workloads
to the chart-owned controller manifests from Part 1:

```bash
echo_image="$(jq -r '.images.echo' "$AFD_PLS_E2E_STATE_FILE")"
test "$echo_image" != null
test "${echo_image#*@sha256:}" != "$echo_image"
member_1_app_manifest="${AFD_PLS_E2E_ARTIFACT_DIR}/member-1-app.yaml"
member_2_app_manifest="${AFD_PLS_E2E_ARTIFACT_DIR}/member-2-app.yaml"
```

## 2. Create the two private-origin applications

Render each echo workload and internal LoadBalancer/PLS Service:

```bash
render_echo() {
  local manifest="$1" member="$2" subnet="$3"
  cat >"$manifest" <<EOF
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
    service.beta.kubernetes.io/azure-pls-ip-configuration-subnet: "${subnet}"
    service.beta.kubernetes.io/azure-pls-visibility: "*"
spec:
  type: LoadBalancer
  selector: {app: echo}
  ports: [{name: http, port: 80, targetPort: 8080}]
EOF
}
render_echo "$member_1_app_manifest" member-1 pls-1
render_echo "$member_2_app_manifest" member-2 pls-2
```

**MUTATING — apply the application manifests and wait for both private origins:**

```bash
k "$AFD_PLS_E2E_MEMBER1_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "$member_1_app_manifest"
k "$AFD_PLS_E2E_MEMBER2_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "$member_2_app_manifest"
for context in "$AFD_PLS_E2E_MEMBER1_CONTEXT" "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  k "$context" -n afd-pls-e2e wait --for=condition=Available deployment/echo --timeout=20m
  k "$context" -n afd-pls-e2e get service/echo \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}{"\n"}'
done
```

Both printed addresses must be non-empty private IPs.

## 3. Create the Fleet and Gateway API resources

Render the `MultiClusterBackend`, `Gateway`, and `HTTPRoute`. Part 1 already installed the
cluster-scoped `GatewayClass`.

```bash
cat >"$AFD_PLS_E2E_GATEWAY_MANIFEST" <<EOF
apiVersion: v1
kind: Namespace
metadata: {name: afd-pls-e2e}
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
```

**READ ONLY — verify the application manifests:**

```bash
for manifest in "$member_1_app_manifest" "$member_2_app_manifest" \
  "$AFD_PLS_E2E_GATEWAY_MANIFEST"; do
  test -s "$manifest"
  ! grep -E '^[[:space:]]*image:' "$manifest" |
    grep -Ev '@sha256:[[:xdigit:]]{64}"?[[:space:]]*$'
done
```

Expected: all manifests are non-empty and no tag-only workload image is printed.

Grant the hub identity `Reader` on each exact discovered PLS before AFD links the origins:

```bash
hub_principal_id="$(jq -r --arg n "afd-hub-id-${AFD_PLS_E2E_RUN_ID}" \
  '.identities[] | select(.name==$n).principalId' "$AFD_PLS_E2E_STATE_FILE")"
reader_role_id="$(az role definition list --name Reader --query '[0].name' -o tsv)"
ensure_pls_reader() {
  local node_rg="$1" logical_name="$2" pls_json pls_id assignments assignment_id
  pls_json="$(az network private-link-service list -g "$node_rg" -o json)"
  if [[ "$(jq 'length' <<<"$pls_json")" -ne 1 ]]; then
    echo "ERROR: expected exactly one PLS in $node_rg" >&2
    return 1
  fi
  pls_id="$(jq -r '.[0].id' <<<"$pls_json")"
  assignments="$(az role assignment list --assignee-object-id "$hub_principal_id" \
    --scope "$pls_id" --fill-principal-name false -o json |
    jq --arg role "$reader_role_id" --arg scope "$pls_id" \
      '[.[] | select((.roleDefinitionId|ascii_downcase|endswith("/"+($role|ascii_downcase)))
        and (.scope|ascii_downcase)==($scope|ascii_downcase))]')"
  if [[ "$(jq 'length' <<<"$assignments")" -eq 0 ]]; then
    assignment_id="$(az role assignment create --assignee-object-id "$hub_principal_id" \
      --assignee-principal-type ServicePrincipal --role "$reader_role_id" \
      --scope "$pls_id" --query id -o tsv)"
  else
    assignment_id="$(jq -r '.[0].id' <<<"$assignments")"
  fi
  record_role_assignment "$logical_name" "$hub_principal_id" "$reader_role_id" \
    "$pls_id" "$assignment_id"
  echo "Recorded $logical_name on $pls_id"
}
ensure_pls_reader "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" hub-read-member-1-pls
ensure_pls_reader "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP" hub-read-member-2-pls
```

Wait for chart-owned networking CRDs, then apply the Gateway/backend resources:

```bash
k "$AFD_PLS_E2E_HUB_CONTEXT" wait --for=condition=Established \
  crd/multiclusterbackends.networking.fleet.azure.com \
  crd/serviceoriginassignments.networking.fleet.azure.com \
  --timeout=5m
k "$AFD_PLS_E2E_HUB_CONTEXT" apply --server-side --field-manager=phase7-e2e \
  -f "$AFD_PLS_E2E_GATEWAY_MANIFEST"
```

AFD creates managed private endpoints from a Microsoft-managed subscription that is distinct from
`AKS Fleet Development/Test`. The POC therefore sets PLS visibility to `"*"` but does not enable
PLS auto-approval. The member controller's optional requester-subscription allowlist is left empty;
approval still requires the exact active assignment, UID, generation, PLS ID, pending state, and
complete `fleet:<assignment-uid>:<request-token>` message. Production deployments may configure an
allowlist once their actual AFD-managed requester subscription is known and trusted.

Fleet now handles discovery, AFD graph reconciliation, private-endpoint request correlation and
approval, health, and Gateway status. The application owner does not create AFD resources or
approve arbitrary private endpoints manually.

## 4. Witness discovery, pending connections, and approval

There is intentionally no Go/Ginkgo runner and no `phase7-e2e-run` target. The operator performs
and witnesses every assertion below. Commands marked **MUTATING** stay inside the approved run
boundary. Run them in order and stop on any unexpected output.

### 4.1 Verify contexts, controllers, and immutable images

**READ ONLY:**

```bash
for context in \
  "$AFD_PLS_E2E_HUB_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  echo "=== $context ==="
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" cluster-info
done
```

Expected: each block contains `Kubernetes control plane is running at`.

```bash
for context in \
  "$AFD_PLS_E2E_HUB_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" \
    -n fleet-system get pods -o json |
    jq -r '.items[].spec | ([.initContainers[]?.image] + [.containers[].image])[]'
done
```

Expected: every printed image ends in `@sha256:` followed by 64 hexadecimal characters; no tag-only
reference is present. The hub has `deployment/hub-gateway-controller-manager`; both members have
`deployment/member-net-controller-manager`.

### 4.2 Verify echo Services, internal LBs, and PLS resources

**READ ONLY:**

```bash
for context in "$AFD_PLS_E2E_MEMBER1_CONTEXT" "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" \
    -n afd-pls-e2e wait --for=condition=Available deployment/echo --timeout=10m
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" \
    -n afd-pls-e2e get service/echo \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}{"\n"}'
done
```

Expected: both waits print `deployment.apps/echo condition met`; both following lines are non-empty
private IP addresses.

```bash
for rg in "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  az resource list --resource-group "$rg" \
    --query "[?type=='Microsoft.Network/loadBalancers' || type=='Microsoft.Network/privateLinkServices'].{name:name,type:type}" \
    -o table
done
```

Expected: each node RG lists at least one load balancer and one Private Link Service.

### 4.3 Verify selection, discovery, pending requests, approval, and programmed status

**READ ONLY:**

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  get memberclusters -l networking.fleet.azure.com/afd-poc=true
```

Expected: exactly `afd-m1-${AFD_PLS_E2E_RUN_ID}` and `afd-m2-${AFD_PLS_E2E_RUN_ID}` are listed.

Wait until both assignments are discovered, then inspect the Azure PLS connections while the
controller correlates the exact Fleet request. A `Pending` state may be brief; do not approve it
manually:

```bash
for _ in $(seq 1 120); do
  ASSIGNMENT_COUNT="$(
    k "$AFD_PLS_E2E_HUB_CONTEXT" get serviceoriginassignments -A \
      --no-headers 2>/dev/null | wc -l
  )"
  echo "assignments=$ASSIGNMENT_COUNT"
  [[ "$ASSIGNMENT_COUNT" -eq 2 ]] && break
  sleep 10
done
test "$ASSIGNMENT_COUNT" -eq 2
for rg in "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  pls_id="$(az network private-link-service list -g "$rg" --query '[0].id' -o tsv)"
  az network private-endpoint-connection list --id "$pls_id" \
    --query '[].{name:name,status:privateLinkServiceConnectionState.status,description:privateLinkServiceConnectionState.description}' \
    -o table
done
```

Expected: each PLS exposes the AFD-managed connection as `Pending` before approval or `Approved`
if the exact guarded approval already completed. Any `Rejected` connection is a failure.

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  wait --for=condition=PrivateLinkApproved \
  serviceoriginassignments.networking.fleet.azure.com --all --all-namespaces --timeout=30m
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e wait --for=condition=Programmed gateway/global --timeout=30m
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e wait --for=condition=Programmed \
  multiclusterbackend.networking.fleet.azure.com/echo --timeout=30m
```

Expected: two assignment lines and both programmed resources print `condition met`.

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e get multiclusterbackend/echo \
  -o jsonpath='{.status.selectedClusters}/{.status.readyOrigins}{"\n"}'
```

Expected exact output: `2/2`.

Capture the first evidence snapshot (**READ ONLY except for the local JSONL append**):

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=programmed
```

Expected: `evidence appended to ... with label programmed`.

## 5. Verify normal traffic reaches both members

**READ ONLY data-plane requests:**

```bash
HOSTNAME="$(
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
    -n afd-pls-e2e get gateway/global -o jsonpath='{.status.addresses[0].value}'
)"
test -n "$HOSTNAME"
declare -A SEEN=()
for _ in $(seq 1 60); do
  BODY="$(curl --silent --show-error --max-time 30 "https://${HOSTNAME}/" | tr -d '\r\n')"
  printf '%s\n' "$BODY"
  SEEN["$BODY"]=1
  [[ -n "${SEEN[member-1]:-}" && -n "${SEEN[member-2]:-}" ]] && break
  sleep 5
done
printf 'member-1=%s member-2=%s\n' "${SEEN[member-1]:-0}" "${SEEN[member-2]:-0}"
```

Expected final output: `member-1=1 member-2=1`. Any other response body is a failure.

## 6. Verify the deterministic WAF rule

**READ ONLY data-plane requests:**

```bash
curl --silent --show-error --max-time 30 --output /dev/null \
  --write-out '%{http_code}\n' "https://${HOSTNAME}/"
curl --silent --show-error --max-time 30 --output /dev/null \
  --header 'X-POC-Block: true' --write-out '%{http_code}\n' "https://${HOSTNAME}/"
```

Expected exact outputs, in order: `200` and `403`. Merely observing a WAF association is not
sufficient.

Capture or retain the `programmed` evidence snapshot, the two-member response samples, and the
`200`/`403` outputs before continuing.

### Optional automation/reference

`step-06-waf-manifests.sh` and `step-07-deploy-join.sh` still combine platform and application
operations. They are references only and do not match this document's application-owner boundary.
The literal commands above are authoritative.

[Previous: infrastructure setup](01-infrastructure-setup.md) ·
[Next: lifecycle validation](03-lifecycle-validation.md)
