# Service Gateway repeat-run recovery

## Requirements

Fix repeated live proof failure: endpoint withdrawal retained an old member
mapping, and both members lost outbound connectivity despite successful ARM
gateway/NAT provisioning. Preserve unrelated pending CI work and the old manual
lab. Use only the dedicated E2E scopes for authorized live verification.

## Plan

- [x] Reproduce stale duplicate-source export selection in offline tests.
- [x] Select current source snapshots without trusting cross-member metadata.
- [x] Reconcile observed Azure deltas, avoiding unchanged service/map writes.
- [x] Isolate resource-ID recreation versus fresh-identity enrollment using
  health and reflected-egress evidence; do not claim an unproven platform cause.
- [x] Run affected tests and repeated live lifecycle proofs with recovery.
- [x] Record findings and unresolved platform risks honestly.

## Initial evidence

The failed proof reached initial two-member ingress and timed out after 600
seconds with `gateway membership count=2, expected=1`. All four nodes became
NotReady; controller route withdrawal recovered them.

Hub exports included two objects for the same source EndpointSlice UID, with
different source generations. The resolver currently unions both snapshots.
Activity Log showed 21 address-map writes and 22 service writes in ten minutes,
but only one NAT write. Repeated NAT updates are not the observed cause.
The successful run used fresh network identities; its failed repeat reused
those identities after teardown. Resource-name reuse is a hypothesis, not a
confirmed platform restriction.

## Changes and evidence

- Hub selects the newest source EndpointSlice generation, within the accepted
  member/service scope. Older snapshots cannot revive withdrawn membership.
  Equal-generation contradictions fail explicitly. Regression uses a ready old
  snapshot followed by a newer empty snapshot, then a contradictory duplicate.
- Provider now diffs inbound registration and pod/location state. Repeated
  converged polls make zero Azure writes; pod replacement/readiness changes
  mutate only address mappings. Regression checks exact write shape and count.
- Stronger live proof creates diagnostic curl pods in both members and requires
  six reflected addresses per member to equal the shared NAT IP, along with
  fresh node leases, before moving into readiness withdrawal. An ingress-only
  success can no longer mask lost egress.
- With the exact fixed controller image, recreating the previously deleted
  gateway/NAT/PIP IDs still failed sustained egress. The failed network was
  withdrawn and members recovered. Its report/config were archived.
- Identical code using never-used IDs then passed the full stronger proof.
  Two further invocations passed with unique IDs, without restarting member
  agents between those successful runs.
- Make runner generates per-invocation root names within the approved resource
  group scopes. It preserves base network.json and stores both current and
  immutable run-specific configurations, archives the previous report, and
  locks the local environment to reject concurrent cutovers.
- Pending CI cleanup/artifact code was kept consistent: cleanup reads the actual
  per-run network IDs; report artifacts include them.
- Last-route cleanup records retired root IDs on the Gateway. Its subsequent
  reenrollment refuses these IDs, requiring new explicit configuration instead
  of blindly recreating a known-failing layout. This guard has offline lifecycle
  coverage; it cannot track deleted/recreated Gateway objects, which must use
  never-used IDs by platform policy.

## Live verification

Dedicated scope only:
`d712bfad-d238-486f-8f1b-bf61a831b712/fleet-sg-e2e-20261113151410`.
The older manual two-AKS experiment was untouched.

Three consecutive fresh-ID stronger proofs passed:

- `repeatability-fixed-fresh-identities.log`
- `repeatability-runner-unique-identities.log`
- `repeatability-runner-second-invocation.log`

Each required 100 successful cross-member requests, six matched NAT-IP
samples per member, readiness withdrawal/restoration, original subnet restore,
owned-resource cleanup, fresh node leases and post-detach workloads.
Clusters and their controllers remain retained; only E2E-owned network resources
were withdrawn during normal proof/recovery. The first fixed-image run with
reused root IDs failed and is retained as negative evidence, not hidden.

## Remaining limitation

This is a demonstrated resource-recreation workaround, not an explanation or
repair of Network RP/data-plane internals. Production migration, reusing old
root IDs across Gateways, multi-region/VNet and live CI remain unverified.
CI cutover approval was not enabled, and no workflow was dispatched.

## Final validation

Passed race-enabled provider/controller packages, tagged E2E assertion/traffic
tests without running the live suite, and all test/scripts contracts (including
pending CI changes). Vet passed for new controller/provider/scripts; hub manager
built successfully. The build-only binary was removed. Documentation links,
workflow YAML, affected Bash syntax, and whitespace checks passed.

The final report shows SuiteSucceeded=true, live proof passed from
17:47:19Z to 17:51:28Z, shared NAT IP `172.173.32.138`, six reflected samples
per member, and final all-four-node Ready verification. The deployed hub image
contains snapshot selection and no-op-write fixes; the additional retired-ID
guard was covered offline, not rolled out during the passing proof.
No commits/pushes were created. An unrelated untracked `.pipeline/e2e-tests.yaml`
appeared during work and was left untouched.
