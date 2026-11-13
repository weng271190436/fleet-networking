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
