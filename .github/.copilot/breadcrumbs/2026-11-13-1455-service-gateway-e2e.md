# Service Gateway live E2E

## Approved scope

Implement an explicitly opted-in live E2E using the existing hub/two-member
harness. Create actual ClusterIP workloads, ServiceExports, GatewayClass,
Gateway, and TCPRoute. Observe controller-owned Azure state, verify traffic
from both members, readiness withdrawal, and ordered final withdrawal.
Retain fixtures on failure; never let ordinary suite teardown delete an
enrolled network after a failed assertion. No live execution is approved.

## Plan

- [x] Add gated live spec with prerequisite/ownership checks and bounded waits.
- [x] Verify endpoint membership, ingress, readiness withdrawal, subnet restore,
  and deletion of owned Azure resources.
- [x] Integrate failure retention and diagnostics without changing ordinary E2E.
- [x] Add focused offline assertion tests and usage instructions.
- [x] Compile live test and run focused offline validation.

## Risks

The shared-VNet environment and original AKS egress must be approved and
functional before running. NSG policy remains operator-owned. These tests
must not adopt the existing manual experiment's resources.

## Implementation

- `test/e2e/servicegateway_test.go`: serial `service-gateway-proof` live spec.
  Requires explicit environment opt-in, approved configuration, fresh resource
  IDs, enabled/current controller deployments, and initially unassociated
  subnets. The test only reads Azure; installed controllers provision/write.
- Real ClusterIP workloads and ServiceExports feed Gateway/TCPRoute intent.
  Observations include Azure ownership, VIP and LB rule, default NAT binding,
  exact pod/node membership, and 100-request member-identity checks.
- Readiness changes use a Recreate Deployment rollout with a failing HTTP
  readiness path, then restore it. This covers replacement/readiness convergence,
  not in-place readiness transition without pod replacement.
- Teardown is an explicit test phase: remove route, observe restored subnet
  links before missing NAT, verify resource deletion/journal removal, fresh
  node heartbeat leases, and new available workloads after detach.
- `test/e2e/e2e_test.go`: registers TCPRoute and requires isolated label
  execution when enabled; suite cleanup retains fixtures after failed proof.
- `test/e2e/servicegateway_assertions_test.go`: offline positive/negative
  assertions reject stale Gateway readiness, missing/wrong/duplicate mappings,
  incomplete traffic batches and foreign/malformed reply identities.
- Usage, network-policy requirements, failure retention/recovery, and limits
  documented in `test/README.md` and the design document.

## Validation

Passed using repository-required Go toolchain:

- `go test -race ./test/e2e -run '^TestServiceGateway' -count=1`
- `go test -race -tags=e2e ./test/e2e -run '^TestServiceGateway' -count=1`
- `go vet -tags=e2e ./test/e2e`
- Documentation file-link checks and `git diff --check`.

The test selectors exclude `TestE2E`: no clusters, credential acquisition, or
Azure operations were performed. The live proof remains unrun. Polling samples
do not establish transactional migration guarantees. Egress reflected-IP
identity, cross-VNet/region, managed-controller restart, and member deletion
are outside this test's verified scope.

## Follow-up: standard Make workflow

The user explicitly requested `make e2e-setup` followed by `make e2e-tests`
with a few environment variables, creating a dedicated hub and two new members.
The opted-in bootstrap now dispatches to `bootstrap-service-gateway.sh`.
It creates Entra-enabled Standard-LB/Azure-CNI clusters, private-pull ACR and
current controller images, Fleet transports/CRDs, controller identities and
flags, narrow test-client NSG rules, and the proof's JSON configuration.
Setup and test derive isolated durable state from subscription/resource group;
the test target requires a successful setup marker. The old manual lab and
normal kubeconfig are untouched. Setup failure retains resources. Automatic
resource-group cleanup refuses the opted-in scenario.

All role assignments use explicit IDs and ARM; no Graph lookup, policy skip
tags, anonymous ACR pulls, or local admin kubeconfig are used. Existing
non-Service-Gateway and AFD paths remain unchanged when disabled.

Offline checks passed: all `test/scripts` tests (including existing AFD
regressions), shell syntax, exact generated AKS template tests, isolated
runner/state/approval/cleanup guards, E2E proof assertion race tests and
live-file compilation, `go vet ./test/scripts`, and whitespace checks.
The new setup has not been executed against Azure: quota, policy, identity
propagation, preview capacity, node migration and packet delivery are still
live validation risks.

## Installation recovery: namespace ownership and global Helm plugin

The first live setup reached chart installation but failed because bootstrap
had already created `fleet-system` and the example fixture chart also rendered
it as a Helm-owned Namespace. The member fixture chart had the same problem.
Both charts now offer `createSystemNamespace` (default true), disabled by this
installer. Their optional user Namespace conditional was also corrected so
an empty `userNS` does not produce an invalid unnamed Namespace.

The installed global helm-unittest plugin was incompatible with the local Helm
version. Setup now isolates plugins using an empty temporary directory and
removes it on exit, without changing global plugins.

Explicit `SERVICE_GATEWAY_RESUME_INSTALL=true` verifies the retained dedicated
group, cluster provisioning/profile, kubeconfig endpoints and common controller
image tag, then skips infrastructure creation and image builds. Published image
metadata is checkpointed for new setups. Legacy failures can recover only with
one unambiguous common image tag. Install calls use upgrade/install and an
idempotent Secret apply. No adoption flags or namespace deletion are used.

Regression checks cover real fixture chart rendering (default creation and
externally managed namespace modes), install-only resume through the first
Helm call, refusal of ambiguous images, absence of infrastructure writes/builds
during resume, and temporary-plugin-directory cleanup on failure.
No live Azure or Kubernetes resources were changed while implementing the fix.

## Live proof repair: 2026-11-13

The user authorized repairing the active live test in dedicated resource group
`fleet-sg-e2e-20261113151410`; the older manually validated lab was not touched.

Confirmed and fixed:

- InternalServiceExport omitted Service type when both Traffic Manager and
  private-origin discovery were disabled. Service type is now transported
  independently of those features. A new member image was pushed and rolled
  out to both members; accepted exports then reported ClusterIP.
- Network RP rejected public-IP creation with `If-None-Match: *` with HTTP 404.
  A dedicated diagnostic PIP reproduced this, succeeded without the header,
  and was deleted afterward. ARM client creation now rechecks absence before
  ordinary PUT; this is explicitly documented as non-atomic and restricted to
  dedicated controller-reserved names. Existing-resource ETag checks remain.
  Fixed hub image was pushed and rolled out.
- Both member subnets referenced the same policy-managed NSG. A shared
  ingress-rule name overwrote the first member's destination prefix. Setup now
  names rules per member. Live narrow source/port rules were corrected.
- HTTP keep-alive tested a single L4 flow. Proof requests now use independent
  connections; a local HTTP server regression requires exactly 100 TCP flows.
- Go test output now streams with `-v`.

The original running binary was gracefully interrupted because it could not
load the traffic fix. Its route was removed and controller withdrawal verified
before starting a corrected proof on the same clusters. The corrected run
observed owned Azure resources and passed 100 independent requests across both
members at VIP `48.223.40.254`.

The readiness phase exposed a remaining live failure: both member clusters
lost node outbound HTTPS/API connectivity after enrollment; all four nodes
became NotReady even though gateway/NAT/default service ARM state was
Succeeded. The exact platform/configuration cause is not established.
The corrected test was interrupted, its route removed, and the controller
restored subnet associations/deleted its owned network. All four nodes were
verified Ready afterward. Gateways/namespaces and diagnostics are retained;
the failed test is not a full lifecycle pass. The proof now checks member
node health while observing the initial programmed graph.

Dedicated state contains `fixed-proof-run.log`, JSON report, per-component
repair image names, and a 100-response diagnostic batch. Do not blindly run
install-only resume: its original image checkpoint predates these repair
images. No cluster/resource-group deletion was performed.

## Gateway/NAT isolation and passing live proof

The user requested further live investigation of the gateway/NAT setup.
Read-only comparison found no decisive configuration difference between the
successful original manual lab and the failed controller snapshots.

Bounded probes on dedicated E2E member-1 (and the last probe on both members)
verified:

- Two-step NAT creation/association produced the expected reflected public IP.
- Inline NAT creation/association also worked; it is not inherently broken.
- Partial/full inbound address-location updates, including repeated identical
  service/location requests, preserved reflected NAT egress in isolation.
- The exact `fleet-default-egress` registration shape worked with both subnets
  linked, without needing a new outbound field.
- Each probe restored prior subnet associations. Diagnostic Azure resources
  were retained but left unlinked; no old manual-lab resources were changed.

Member agents needed a rollout restart after the repeated connectivity
transitions; their fresh deployments and all nodes recovered.
A first fresh-proof attempt correctly stopped at preflight because a member
agent was unavailable; it performed no network enrollment.

The subsequent installed-controller proof passed (JSON report SuiteSucceeded
true; live spec duration about 178 seconds):

1. Fresh owned resources and both subnets observed in the programmed graph.
2. 100 independent successful requests reached both members.
3. Member-1 readiness withdrawal and restoration converged, with traffic checks.
4. Last-route removal restored subnets and deleted owned resources.
5. Fresh node leases and new post-detach workloads passed.
6. All four nodes were verified Ready after suite completion.

The configuration now uses fresh `e2e-164941` network resource names. The
passing report and verbose `fresh-egress-proof-after-agent-recovery.log` are
retained in the dedicated local state directory. No further NAT provider
behavior was changed based on an unproven hypothesis. The exact cause of the
earlier egress failure remains unresolved; a passing run does not justify
claiming a particular ordering, resource-name reuse issue or platform bug.
