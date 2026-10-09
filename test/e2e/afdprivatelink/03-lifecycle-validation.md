# Part 3: lifecycle validation

[Previous: global Service scenario](02-global-service-scenario.md) ·
[Next: cleanup](04-cleanup.md)

> **Status: In progress.** Retained run `p7-10082105` recorded member withdrawal and verified that
> Gateway deletion removed the owned AFD profile while preserving WAF, Services, ILBs, and PLS
> resources. Fail-static evidence and final cleanup remain.

In a new shell, export the same approved run values and initialize helpers before the first
scenario command:

```bash
cd /home/weiweng/fleet-networking
export AZURE_SUBSCRIPTION_ID=d712bfad-d238-486f-8f1b-bf61a831b712
export AFD_PLS_E2E_RUN_ID="replace-with-the-approved-run-id"
export AFD_PLS_E2E_LOCATION=eastus2
export AFD_PLS_E2E_APPROVED=true
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names
validate_subscription
k() {
  local context="$1"
  shift
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" "$@"
}
HOSTNAME="$(
  k "$AFD_PLS_E2E_HUB_CONTEXT" -n afd-pls-e2e get gateway/global \
    -o jsonpath='{.status.addresses[0].value}'
)"
test -n "$HOSTNAME"
```

## 1. Withdraw member 2 and verify uninterrupted traffic

**MUTATING — removes one test label:**

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  label membercluster "$AFD_PLS_E2E_MEMBER2_CLUSTER" \
  networking.fleet.azure.com/afd-poc-
```

Expected: `membercluster.cluster.kubernetes-fleet.io/... unlabeled`.

**READ ONLY wait and traffic check:**

```bash
for _ in $(seq 1 60); do
  VALUE="$(
    kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
      -n afd-pls-e2e get multiclusterbackend/echo \
      -o jsonpath='{.status.selectedClusters}/{.status.readyOrigins}'
  )"
  echo "$VALUE"
  [[ "$VALUE" == "1/1" ]] && break
  sleep 15
done
test "$VALUE" = "1/1"
curl --silent --show-error --max-time 30 --output /dev/null \
  --write-out '%{http_code}\n' "https://${HOSTNAME}/"
```

Expected: status converges to exact `1/1`; curl prints `200`.

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=member-withdrawn
```

## 2. Verify fail-static traffic during a hub-controller outage

**MUTATING — temporarily scales the test controller to zero:**

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system scale deployment/hub-gateway-controller-manager --replicas=0
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system wait --for=delete pod -l app=hub-gateway-controller-manager --timeout=5m
```

Expected: scale reports `scaled`; wait reports the pod condition met.

**READ ONLY traffic check:**

```bash
curl --silent --show-error --max-time 30 --output /dev/null \
  --write-out '%{http_code}\n' "https://${HOSTNAME}/"
```

Expected exact output: `200`.

Always restore the controller (**MUTATING**):

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system scale deployment/hub-gateway-controller-manager --replicas=1
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system rollout status deployment/hub-gateway-controller-manager --timeout=5m
```

Expected: `scaled`, then `successfully rolled out`.

## 3. Verify Gateway deletion ownership

Capture pre-delete evidence:

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=before-gateway-delete
```

Resolve the one controller-owned AFD profile before requesting deletion:

```bash
profile_count="$(az afd profile list -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --query 'length(@)' -o tsv)"
test "$profile_count" = 1
profile_name="$(az afd profile list -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --query '[0].name' -o tsv)"
test -n "$profile_name"
```

**MUTATING — deletes the test Gateway and its controller-owned AFD graph:**

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e delete gateway/global --wait=false
```

`gateway ... deleted from ...` means the delete request was accepted; the object remains behind
its cleanup finalizer while Azure deletes the profile. Monitor both sides with visible progress:

```bash
for attempt in $(seq 1 90); do
  gateway_json="$(kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" \
    --context "$AFD_PLS_E2E_HUB_CONTEXT" -n afd-pls-e2e \
    get gateway/global -o json 2>/dev/null || true)"
  profile_state="$(az afd profile show -g "$AFD_PLS_E2E_RESOURCE_GROUP" \
    -n "$profile_name" --query resourceState -o tsv 2>/dev/null || true)"
  if [[ -n "$gateway_json" ]]; then
    finalizers="$(jq -c '.metadata.finalizers // []' <<<"$gateway_json")"
    echo "attempt=$attempt Gateway=terminating finalizers=$finalizers AFD=${profile_state:-absent}"
  else
    echo "attempt=$attempt Gateway=absent AFD=${profile_state:-absent}"
  fi
  [[ -z "$gateway_json" && -z "$profile_state" ]] && break
  sleep 30
done
```

Expected progression: AFD reports `Deleting`, then becomes absent; the controller removes
`networking.fleet.azure.com/afd-gateway-cleanup`; finally the Gateway becomes absent. This can take
many minutes.

**READ ONLY — wait for and verify ownership boundaries:**

```bash
for _ in $(seq 1 60); do
  CDN_COUNT="$(
    az resource list --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" \
      --query "[?contains(type, 'Microsoft.Cdn')] | length(@)" -o tsv
  )"
  echo "$CDN_COUNT"
  [[ "$CDN_COUNT" == "0" ]] && break
  sleep 20
done
test "$CDN_COUNT" = "0"

az resource list --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" \
  --resource-type Microsoft.Network/frontdoorWebApplicationFirewallPolicies \
  --query 'length(@)' -o tsv

for context in "$AFD_PLS_E2E_MEMBER1_CONTEXT" "$AFD_PLS_E2E_MEMBER2_CONTEXT"; do
  kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$context" \
    -n afd-pls-e2e get service/echo -o name
done

for rg in "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  az resource list --resource-group "$rg" \
    --query "[?type=='Microsoft.Network/loadBalancers'] | length(@)" -o tsv
  az resource list --resource-group "$rg" \
    --query "[?type=='Microsoft.Network/privateLinkServices'] | length(@)" -o tsv
done
```

Expected: CDN count reaches exact `0`; WAF count is exact `1`; both Service commands print
`service/echo`; every node-RG count is at least `1`.

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=after-gateway-delete
```

Snapshots are appended to:

```text
.phase7-${AFD_PLS_E2E_RUN_ID}.results.jsonl
```

Phase 7 is still incomplete until a human has witnessed every expected result, retained this
evidence, run cleanup, and verified deletion.

## 4. Scenario evidence and debugging

The following are **READ ONLY**. Initialize deterministic names in each new shell:

Inspect inventory without displaying Kubernetes Secrets:

```bash
jq '{runId,resourceGroup,images,identities,roleAssignments,discoveredRoleAssignments,resources}' \
  "$AFD_PLS_E2E_STATE_FILE"
```

Inspect controllers and workloads:

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system get pods
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n fleet-system logs deployment/hub-gateway-controller-manager --tail=200
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_MEMBER1_CONTEXT" \
  -n fleet-system logs deployment/member-net-controller-manager --tail=200
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_MEMBER2_CONTEXT" \
  -n fleet-system logs deployment/member-net-controller-manager --tail=200
```

Inspect API status and Azure resources:

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e get gateway,multiclusterbackend,serviceoriginassignment -o wide
az resource list --resource-group "$AFD_PLS_E2E_RESOURCE_GROUP" -o table
az resource list --resource-group "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" -o table
az resource list --resource-group "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP" -o table
tail -n 20 "$AFD_PLS_E2E_RESULTS_FILE" | jq .
```

Do not print controller token volumes, use `kubectl get secret ... -o yaml`, or enable shell
tracing.

Retain the `member-withdrawn`, `before-gateway-delete`, and `after-gateway-delete` snapshots. Phase
7 remains incomplete until cleanup is run and deletion is verified.

[Previous: global Service scenario](02-global-service-scenario.md) ·
[Next: cleanup](04-cleanup.md)
