# Phase 7 AFD Private Link human-validation runbook

> **Status: In progress.** The runbook and guarded automation are implemented, but the complete
> human validation has not passed. Retained run `p7-10082105` stopped during a monolithic rerun
> when Azure rejected an attempted in-use subnet deletion. Do not interpret this document or its
> commit as Phase 7 completion.

This is the standalone runbook for the real-Azure Phase 7 human-run POC validation. Commands marked
**READ ONLY** do not change Azure or Kubernetes. Commands marked **MUTATING** create, update, or
delete billable resources and must not be run without explicit approval.

The fixed target is:

- subscription: `AKS Fleet Development/Test`
- subscription ID: `d712bfad-d238-486f-8f1b-bf61a831b712`
- branch: `poc/gateway-api-afd-private-link`
- required ancestor: `5093b017e6df521a08af424b497701d3658381a6`

No credentials belong in this repository, command history, manifests, or state inventory.

## 1. Prerequisites

Run from the repository root:

```bash
cd /home/weiweng/fleet-networking
```

Required tools are Azure CLI, Docker with a running daemon and buildx, Go, kubectl, Helm, jq, curl,
git, and make. The preparation was validated with the following versions; use these or compatible
newer patch releases:

| Tool | Validated version / requirement |
|---|---|
| Azure CLI | 2.87.0 |
| Docker Engine | 29.8.2 |
| Docker buildx | 0.37.1 |
| Go | module requires 1.25.12; validated with 1.26.5 |
| kubectl | 1.36.1 |
| Helm | 3.16.4 |
| jq/curl/git/make | available on `PATH` |

**READ ONLY — check tools and Docker:**

```bash
for tool in az docker go kubectl helm jq curl git make; do
  command -v "$tool" || exit 1
done
docker info >/dev/null
docker buildx version
go version
kubectl version --client
HELM_NO_PLUGINS=1 helm version
```

The signed-in principal must be able to create resource groups, AKS, ACR, managed identities,
role assignments, networking resources, AFD Premium, and WAF resources in the fixed
subscription. The setup uses `az aks get-credentials --admin`; it never writes credentials to the
repository.

## 2. Sign in and select the exact subscription

`az login` changes only local Azure CLI authentication, but may open a browser. Do not paste tokens
or passwords into this runbook.

```bash
az login
az account set --subscription d712bfad-d238-486f-8f1b-bf61a831b712
```

**READ ONLY — verify both ID and name:**

```bash
az account show --query '{name:name,id:id,tenantId:tenantId}' -o table
```

The name must be `AKS Fleet Development/Test` and the ID must be
`d712bfad-d238-486f-8f1b-bf61a831b712`.

## 3. Choose one unique run ID and location

Use 3–15 lowercase alphanumeric/hyphen characters. Keep the same values for preflight, setup,
test, debugging, and cleanup.

```bash
export AZURE_SUBSCRIPTION_ID=d712bfad-d238-486f-8f1b-bf61a831b712
export AFD_PLS_E2E_RUN_ID="p7-$(date -u +%m%d%H%M)"
export AFD_PLS_E2E_LOCATION=eastus2
printf 'run=%s location=%s\n' "$AFD_PLS_E2E_RUN_ID" "$AFD_PLS_E2E_LOCATION"
```

Do not set `AFD_PLS_E2E_APPROVED` yet.

## 4. Run and review preflight

**READ ONLY:**

```bash
make phase7-e2e-preflight
```

Preflight verifies:

- exact subscription name/ID, provider registrations, regional and DSv3 quota;
- deterministic ACR-name availability or ownership by the same tagged run;
- absence of conflicting tagged resource groups, or exact ownership recorded for a safe rerun;
- Docker daemon/buildx and all four Docker build inputs;
- both Helm charts and pinned Fleet `v0.14.0` / Gateway API `v1.2.1` CRD inputs;
- exact branch and required commit ancestry; and
- no merge conflicts or staged files.

It performs Azure queries and local validation only. It does not create a Docker builder, publish
images, render files, or call mutating Azure/Kubernetes operations.

The getting-started hub chart requires populated member values. Its default `helm lint` currently
reports the pre-existing `templates/ns.yaml: invalid Yaml document separator: apiVersion: v1`
issue; Phase 7 validates the chart with both member IDs/principal IDs populated, and that render
must pass.

### Billable/resource plan to approve

One run creates:

- primary RG `fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}`;
- Basic ACR `fleetp7${AFD_PLS_E2E_RUN_ID//-/}d712`;
- four image repositories/builds: hub Gateway controller, member controller, CRD installer, echo;
- three controller user-assigned identities/federated credentials, three AKS control-plane
  identities, three AKS kubelet identities, three built-in RG-scoped assignments, and three
  AKS-created AcrPull assignments;
- three one-node `Standard_D2as_v4` workload-identity-enabled AKS clusters;
- three tagged AKS node RGs, one VNet, three AKS subnets, and two PLS NAT subnets;
- two internal Standard load balancers and two Private Link Services from the echo Services;
- one AFD Premium profile graph and one Front Door WAF policy.

Because this subscription has exhausted its custom-role-definition quota, the approved live run
uses the built-in `Contributor` role for the hub identity scoped only to the disposable primary RG
and `Network Contributor` for each member identity scoped only to its exact disposable node RG.
No role is assigned at subscription scope.

## 5. Explicitly acknowledge mutation

Stop here until the preflight output and billable plan have been explicitly approved.

**MUTATING acknowledgement:**

```bash
export AFD_PLS_E2E_APPROVED=true
```

Every mutating entry point rejects any value other than the exact lowercase string `true`.

## 6. Run the seven resumable setup stages

Run one numbered target at a time. This is the recommended workflow for both a new run and a
resume. Every target is **MUTATING**, requires `AFD_PLS_E2E_APPROVED=true`, recomputes all names
from the current run ID, validates the exact subscription/tags/state and all prerequisites before
its first mutation, upserts state, preserves the environment on failure, and prints the next
command. A completed stage can be rerun safely with the same run ID.

| Stage | Command | Azure mutation | Kubernetes mutation |
|---|---|---|---|
| 01 | `make phase7-e2e-step-01-registry-images` | create/reuse tagged RG and Basic ACR; push four images | none |
| 02 | `make phase7-e2e-step-02-network` | create VNet only if absent; create only absent subnets; optionally restore tags on the exact state-recorded legacy VNet | none |
| 03 | `make phase7-e2e-step-03-aks` | create only absent AKS clusters; tag exact AKS node RGs | none |
| 04 | `make phase7-e2e-step-04-identities-rbac-kubeconfig` | create/reuse identities, federations, and exact scoped assignments; read AKS credentials | local kubeconfig only |
| 05 | `make phase7-e2e-step-05-crds-registration` | none | apply CRDs, namespaces, Azure-principal registration, and `MemberCluster`s |
| 06 | `make phase7-e2e-step-06-waf-manifests` | create/reuse exact WAF policy/rule | read hub connection data only; render local manifests |
| 07 | `make phase7-e2e-step-07-deploy-join` | read assignments only; controllers later own AFD | apply workloads, wait for controllers, create `InternalMemberCluster`s, wait heartbeat, patch joined status |

### 6.1 Registry and immutable images

```bash
make phase7-e2e-step-01-registry-images
jq '.images' ".phase7-${AFD_PLS_E2E_RUN_ID}.state.json"
```

Expected: five non-empty digest-pinned image references. Existing ACRs are reused only when the
resource group, `Basic` SKU, `source`, and `run-id` match; images are rebuilt/pushed on rerun.

### 6.2 Network

```bash
make phase7-e2e-step-02-network
az network vnet subnet list -g "fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}" \
  --vnet-name "afd-vnet-${AFD_PLS_E2E_RUN_ID}" \
  --query '[].{name:name,prefix:addressPrefix,plsPolicy:privateLinkServiceNetworkPolicies}' -o table
```

Expected: `hub`, `member-1`, `member-2`, `pls-1`, and `pls-2` with their fixed prefixes and
`Disabled` PLS network policy. If the VNet exists, the stage **never** invokes
`az network vnet create`; it validates address space first, validates each existing subnet without
updating it, and creates only missing subnets.

### 6.3 AKS

```bash
make phase7-e2e-step-03-aks
az aks list -g "fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}" \
  --query '[].{name:name,nodeResourceGroup:nodeResourceGroup,power:powerState.code}' -o table
```

Expected: the exact hub and two member clusters are `Running`. Existing clusters are reused only
after tag, primary RG, node RG, subnet, `Standard_D2as_v4`, Standard LB, OIDC, and workload
identity validation; no cluster is recreated.

### 6.4 Identities, RBAC, and kubeconfig

```bash
make phase7-e2e-step-04-identities-rbac-kubeconfig
KUBECONFIG=".phase7-${AFD_PLS_E2E_RUN_ID}.kubeconfig" kubectl config get-contexts
```

Expected: exact `phase7-...-hub`, `...-member-1`, and `...-member-2` contexts. Existing tagged
identities, exact issuer/subject/audience federations, and exact principal/role/scope assignments
are reused and state entries are replaced by logical key rather than appended.

### 6.5 CRDs and registration

```bash
make phase7-e2e-step-05-crds-registration
kubectl --kubeconfig ".phase7-${AFD_PLS_E2E_RUN_ID}.kubeconfig" \
  --context "phase7-${AFD_PLS_E2E_RUN_ID}-hub" get memberclusters
```

Expected: both exact member names exist. This stage intentionally does **not** create
`InternalMemberCluster`s; networking controllers are not deployed yet.

### 6.6 WAF and manifests

```bash
make phase7-e2e-step-06-waf-manifests
grep -R 'image:' ".phase7-${AFD_PLS_E2E_RUN_ID}" | grep -v '@sha256:' && exit 1 || true
```

Expected: the command prints the step-07 next command and no non-digest workload image line.
This stage renders files only; it does not apply Kubernetes manifests.

### 6.7 Deploy, wait, then join

```bash
make phase7-e2e-step-07-deploy-join
kubectl --kubeconfig ".phase7-${AFD_PLS_E2E_RUN_ID}.kubeconfig" \
  --context "phase7-${AFD_PLS_E2E_RUN_ID}-hub" get memberclusters
```

Expected: controller/echo Deployments become Available before `InternalMemberCluster` join
requests are created; both networking-agent heartbeats are observed before joined status is
patched.

### Optional sequential wrapper

For a brand-new unattended run only, the old target remains as a thin sequential wrapper:

```bash
make phase7-e2e-setup
```

It is **not recommended** for debugging or resuming because it starts again at image publication.
It never performs automatic cleanup.

Generated files are mode-protected and run-scoped:

```text
.phase7-${AFD_PLS_E2E_RUN_ID}.state.json
.phase7-${AFD_PLS_E2E_RUN_ID}.kubeconfig
.phase7-${AFD_PLS_E2E_RUN_ID}/hub.yaml
.phase7-${AFD_PLS_E2E_RUN_ID}/member-1.yaml
.phase7-${AFD_PLS_E2E_RUN_ID}/member-2.yaml
```

Generated paths are always recomputed from the current run ID. This prevents a new run in the same
shell from accidentally reusing an earlier run's state or kubeconfig path.

The state inventory records ACR/image digests, identities, federated credentials, role assignments,
AKS-created assignments, resource IDs, and RGs. It contains no bearer tokens.

The member chart uses reduced 25m CPU requests for each controller pod container in this
single-node validation topology; default production chart requests are unchanged.

If a stage fails, it writes
`.phase7-${AFD_PLS_E2E_RUN_ID}.results.step-NN-...-failure.log`, preserves all resources, and
performs no automatic cleanup. Review that file and rerun only the failed numbered target.

### Recovery for retained run `p7-10082105`

The previous monolithic rerun called `az network vnet create` on an existing VNet. Azure treated
the supplied single hub subnet as desired VNet state and attempted to remove `member-1`, which was
attached to a Kubernetes internal load balancer. Azure correctly returned
`InUseSubnetCannotBeDeleted`. **Do not delete or detach that subnet.**

After exporting the fixed subscription, run ID, location, and approval variables, the next command
is:

```bash
make phase7-e2e-step-02-network
```

The new network stage reuses the VNet, validates every existing subnet, creates only a genuinely
absent subnet, and never updates/deletes an in-use subnet. If the legacy VNet has no tags, it adds
the run tags only when the exact VNet ID is already recorded in this run's state. Any prefix,
policy, ownership, or tag conflict stops before subnet creation; inspect the diagnostic log rather
than changing/deleting network resources.

### Fleet registration limitation

This repository ships networking controllers, not Fleet's production hub/member registration
agents or their deployment chart (`cmd/` and `charts/` contain only networking managers).
Therefore setup cannot test the production core Fleet `MemberCluster` controller. It does reuse the
repository's existing E2E registration path: reserved namespaces and Azure-principal RoleBindings
from `examples/getting-started/charts/hub`, Azure refresh-token authentication from the member
chart, and `InternalMemberCluster` join requests observed by the real networking agents. Setup
waits for `ServiceExportImportAgent/Joined=True` and a heartbeat before setting the aggregate
selector-facing `MemberCluster/Joined=True` condition. Only that aggregate condition remains a
validation-scoped substitute.

## 7. Perform the human-run POC validation

There is intentionally no Go/Ginkgo runner and no `phase7-e2e-run` target. The operator performs
and witnesses every assertion below. Commands marked **MUTATING** stay inside the approved run
boundary. Run them in order and stop on any unexpected output.

Initialize names once in the validation shell:

```bash
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names
```

### 7.1 Verify contexts, controllers, and immutable images

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

### 7.2 Verify echo Services, internal LBs, and PLS resources

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

### 7.3 Verify selection, discovery, approval, and programmed status

**READ ONLY:**

```bash
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  get memberclusters -l networking.fleet.azure.com/afd-poc=true
```

Expected: exactly `afd-m1-${AFD_PLS_E2E_RUN_ID}` and `afd-m2-${AFD_PLS_E2E_RUN_ID}` are listed.

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

### 7.4 Verify normal traffic reaches both members

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

### 7.5 Verify the deterministic WAF rule

**READ ONLY data-plane requests:**

```bash
curl --silent --show-error --max-time 30 --output /dev/null \
  --write-out '%{http_code}\n' "https://${HOSTNAME}/"
curl --silent --show-error --max-time 30 --output /dev/null \
  --header 'X-POC-Block: true' --write-out '%{http_code}\n' "https://${HOSTNAME}/"
```

Expected exact outputs, in order: `200` and `403`. Merely observing a WAF association is not
sufficient.

### 7.6 Withdraw member 2 and verify uninterrupted traffic

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

### 7.7 Verify fail-static traffic during a hub-controller outage

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

### 7.8 Verify Gateway deletion ownership

Capture pre-delete evidence:

```bash
make phase7-e2e-evidence EVIDENCE_LABEL=before-gateway-delete
```

**MUTATING — deletes the test Gateway and its controller-owned AFD graph:**

```bash
test "${AFD_PLS_E2E_APPROVED:-}" = true
kubectl --kubeconfig "$AFD_PLS_E2E_KUBECONFIG" --context "$AFD_PLS_E2E_HUB_CONTEXT" \
  -n afd-pls-e2e delete gateway/global --wait=true --timeout=20m
```

Expected: `gateway.gateway.networking.k8s.io "global" deleted`.

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

## 8. Monitor and debug

The following are **READ ONLY**. Initialize deterministic names in each new shell:

```bash
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names
```

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

## 9. Safe reruns and failure recovery

Resume at the failed numbered stage, not at the wrapper. A stage accepts only exact resources that
match its deterministic name, tags, state, topology, and scope. State resources, identities, role
assignments, and completed-stage markers are upserted/deduplicated.

```bash
# MUTATING
make phase7-e2e-step-NN-...
```

Each stage checks all of its prerequisites before mutation. A missing prerequisite tells you which
preceding stage to run. A conflicting existing resource is never replaced; inspect it and resolve
the ownership/configuration mismatch manually. Never delete an in-use subnet as recovery.

Common fixes:

- missing image state or ACR: rerun step 01;
- missing/wrong subnet: rerun step 02 only when absent; a wrong existing prefix/policy requires
  investigation, not an update;
- missing AKS cluster: rerun step 03; an existing topology mismatch is a hard stop;
- missing identity/context: rerun step 04;
- missing CRD/MemberCluster: rerun step 05;
- stale/missing manifests: rerun step 06, then step 07;
- controller timeout: inspect the stage diagnostic log and pod events/logs, then rerun step 07.

Billable resources remain active until validation succeeds or you explicitly run:

```bash
# MUTATING / DESTRUCTIVE, but bounded to exact validated run resources
make phase7-e2e-cleanup
```

Never manually broaden the cleanup query, remove the approval guard, rename state entries, or use
subscription-wide deletion.

## 10. Bounded cleanup

Run cleanup after success or failure.

**MUTATING / DESTRUCTIVE:**

```bash
make phase7-e2e-cleanup
```

Cleanup validates subscription, state run ID, deterministic names, RG tags, and assignment IDs,
and then deletes:

- the exact run-recorded built-in role assignments;
- primary RG `fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}`; and
- the three exact tagged node RGs.

It waits for RG deletion, then removes the generated kubeconfig, manifest directory, and state
inventory. It intentionally preserves the JSONL results file.

## 11. Verify cleanup

**READ ONLY:**

```bash
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names

for rg in \
  "$AFD_PLS_E2E_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  printf '%s: ' "$rg"
  az group exists --name "$rg"
done

```

Every RG query must print `false`. ACR, images, identities, and scoped role assignments are inside
the deleted resource groups or are explicitly removed from the state inventory during cleanup.

## 12. Unset the run environment

Keep the results file if evidence is required, then clear all run variables:

```bash
unset AZURE_SUBSCRIPTION_ID
while IFS= read -r name; do unset "$name"; done < <(compgen -A variable AFD_PLS_E2E_)
```

Starting a new shell is an equivalent way to clear exported values.
