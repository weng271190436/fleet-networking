# Gateway API AFD Private Link POC Phases 2-7

## Requirements

- Implement POC Phases 2 through 7 in dependency order.
- Start each phase with focused failing tests when possible.
- Preserve the frozen Phase 0 API and security contracts.
- Keep each phase independently reviewable and validated.
- Create and push exactly one implementation commit for each completed phase.
- Do not squash, amend, or combine phase commits.
- Complete the human-run real-Azure checklist, retain evidence, and verify cleanup before claiming
  Phase 7 complete.
- Obtain fresh confirmation before provisioning or deleting billable Azure/AKS resources.

## Additional comments from user

- The user requested: "commit push. continue with phase 2 to 7, where each phase should have its
  own commit".
- Phase 1 was committed and pushed as `395e965`.
- Azure CLI authentication, a Kubernetes context, Helm, and kubectl are currently available.
- On 2026-10-09 the user explicitly approved a non-mutating refactor of the Phase 7 setup harness
  on commit `5093b01`, retaining live run `p7-10082105`. The work must remain uncommitted and must
  not run setup, cleanup, Azure/Kubernetes mutations, image pushes, or modify retained `.phase7`
  artifacts.
- The retained run exposed an unsafe rerun: `az network vnet create` was invoked for an existing
  VNet that contained only the hub subnet, and Azure attempted to reconcile away the in-use
  `member-1` subnet attached to the Kubernetes internal load balancer. Azure rejected this as
  `InUseSubnetCannotBeDeleted`. Setup stages must therefore validate and reuse exact existing
  resources, create only missing owned resources, and never replace or delete networking.

### Phase 7 resumable setup refactor plan

#### Phase 1: Shared safety and state

- [x] **Task R1.1: Add stage initialization, prerequisite, state-upsert, and diagnostic helpers.**
  - Every mutating stage must initialize deterministic run paths, require exact approval, validate
    subscription/run ownership, fail before mutation when prerequisites are absent, preserve the
    environment on failure, and print the next command.
  - Success criteria: reruns do not duplicate state entries and diagnostics never expose Secrets.
- [x] **Task R1.2: Preserve exact cleanup boundaries and retained-artifact safety.**
  - Do not change ignored `.phase7` artifacts or broaden cleanup.
  - Success criteria: cleanup remains exact/tag-bounded and no live state is touched during this
    refactor.

#### Phase 2: Split setup into independently resumable stages

- [x] **Task R2.1: Implement registry/images and network stages.**
  - Registry reuses an exactly tagged Basic ACR, republishes the four images, resolves immutable
    digests, and upserts state.
  - Network validates an existing VNet's tags/address space, validates each existing subnet
    prefix/policy, and creates only absent subnets. It never calls VNet create for an existing VNet
    and never updates/deletes an existing subnet.
  - Success criteria: an interrupted run can safely resume without replacing network resources.
- [x] **Task R2.2: Implement AKS and identity/RBAC/kubeconfig stages.**
  - Reuse exact tagged clusters after validating resource group, node resource group, subnet, SKU,
    and workload identity settings; reuse exact identities, assignments, federations, and contexts.
  - Success criteria: existing exact resources are validated rather than recreated.
- [x] **Task R2.3: Implement CRD/registration, WAF/render, and deploy/join stages.**
  - Apply CRDs, registration resources, and MemberClusters before deployment; render manifests
    without applying them; deploy/wait controllers before creating InternalMemberClusters,
    waiting for heartbeat, and marking MemberClusters joined.
  - Success criteria: dependencies and join ordering are explicit and each stage is independently
    retryable.
- [x] **Task R2.4: Retain setup only as an optional sequential wrapper.**
  - Success criteria: the wrapper invokes the seven stages in order and is clearly discouraged for
    debugging/resume.

#### Phase 3: Operator workflow and static validation

- [x] **Task R3.1: Add Make targets and preflight coverage for all stages.**
  - Success criteria: preflight validates every stage script and Make target without mutation.
- [x] **Task R3.2: Rewrite the README setup workflow.**
  - Document numbered commands, expected/check outputs, exact resume behavior, mutation mapping,
    failure recovery, the retained run's `InUseSubnetCannotBeDeleted` recovery, diagnostics,
    optional wrapper, validation, and cleanup.
  - Success criteria: the next safe command for `p7-10082105` is unambiguous.
- [x] **Task R3.3: Run static-only validation.**
  - Run `bash -n`, ShellCheck when installed, read-only preflight with a fresh nonexistent run ID,
    populated Helm renders, quick Docker definition checks, `make fmt`, and `git diff --check`.
    Document the existing default getting-started chart lint issue while requiring populated render
    success.
  - Success criteria: no Azure/Kubernetes mutation, image push, setup stage, or cleanup executes.

The user supplied explicit approval for this implementation plan in the task request, so the
refactor may proceed without a further confirmation round. Phase 7 remains in progress.

### Phase 7 literal-command runbook refactor plan

On 2026-10-09 the user requested a documentation-only refactor that makes literal, copy/paste
commands the authoritative setup workflow. The seven stage scripts and Make targets remain
optional automation/reference only. No Azure/Kubernetes mutation, retained `.phase7` artifact
change, commit, or push is approved.

Manual commands must never silently enable `errexit`, `nounset`, or `pipefail` in the operator's
interactive shell. Shared helpers may be sourced without changing shell options, and failure-prone
command blocks must print actionable errors and return control for diagnosis.
Each independently copyable mutation block must also load and validate its own required IDs rather
than depending on variables populated by an earlier optional inspection block.
Kubeconfig refresh must be idempotent: when Azure CLI recreates a `*-admin` context and the
normalized context already exists, delete only the duplicate `*-admin` context instead of failing
to rename over the retained normalized context.
Live validation found AKS reporting `Running` while all three underlying VMSS instances were
deallocated and nodes carried shutdown/out-of-service taints. The manual workflow must verify VMSS
power and node readiness before deployment, expose an explicit VMSS start recovery, and grant each
member AKS control-plane identity `Network Contributor` on the exact test VNet so cloud provider
can read PLS subnets.
Live controller logs also showed that Fleet v0.14.0 cloud-config validation requires
`useManagedIdentityExtension=true` even when federated workload identity is enabled, and pinned
`refresh-token:v0.1.0` rejects `--workloadIdentityEnabled`. Phase 7 therefore follows the existing
E2E auth pattern exactly: hub uses federated identity with the compatibility managed-identity flag,
while member controllers and refresh-token use each AKS kubelet managed identity through IMDS.
The retained member token was then rejected as `Unauthorized` because Phase 7 AKS clusters lacked
Microsoft Entra integration. The existing repository bootstrap creates clusters with
`--enable-aad --enable-azure-rbac`; Phase 7 cluster creation/validation and retained-run recovery
must enforce the same authentication mode before networking-agent join.
Controller Deployment pod templates include a checksum of their Azure cloud-config Secret so a
corrected authentication configuration triggers a new ReplicaSet instead of relying on a
CrashLoop restart or delayed projected-volume refresh.
Live member logs showed Helm rendered the requester allowlist with literal quote characters inside
the argument value. The member chart must quote the complete
`--afd-requester-subscription-allowlist=<uuid>` scalar, not only the UUID value.
The retained Stage 07 reached healthy controllers and applied Gateway resources, then failed under
`nounset` because one `local` declaration expanded `member_name` before assignment. Join helpers
must assign dependent locals on separate lines, and final role-assignment inventory must disable
Microsoft Graph principal-name enrichment.
The first setup-complete evidence snapshot exposed jq's reserved `label` keyword; evidence JSON
uses a non-keyword argument name while retaining the output field name `label`.
Live assignment status exposed a Phase 5 bug: approval condition transitions rebuilt status without
copying `status.origin`, so `InfrastructureReady=True` coexisted with a nil origin and blocked hub
model construction. Approval status derivation must deep-copy discovered origin facts.
Hub backend status patches were rejected because manually constructed per-member
`metav1.Condition` values omitted required `lastTransitionTime`. Member summaries use
`meta.SetStatusCondition` and preserve the previous per-cluster condition slice to produce valid,
stable transition timestamps.
The live AFD API requires `AfdOriginGroup.LoadBalancingSettings`; the provider sends explicit
sample-size, successful-sample, and latency defaults. Gateway listener conditions likewise use
`meta.SetStatusCondition` so required transition timestamps are valid and stable.
AFD linked authorization requires the hub identity to read each member PLS. Stage 07 grants
built-in `Reader` on the two exact discovered PLS resource IDs before applying Gateway resources.
HTTPRoute parent conditions also use `meta.SetStatusCondition` to satisfy required transition
timestamps.
Azure activity logs showed AFD managed private endpoints originate from a Microsoft-managed
subscription, not the test subscription. Phase 7 PLS visibility is `"*"` so AFD can discover the
service; automatic approval stays disabled. The optional requester-subscription allowlist is empty
for the POC unless the actual managed subscription is explicitly trusted, while exact assignment
UID/token/message/PLS checks remain mandatory. Provider reconciliation retries origins in terminal
`Failed` provisioning state after the underlying visibility issue is corrected.
The default cloud-provider client-side rate limiter starved the final member approval GET in this
low-volume POC while Azure itself was healthy. Phase 7 test cloud configs disable that local
limiter; Azure SDK retry and service-side throttling remain in effect.
After Azure approval succeeded, transient follow-up reads caused `PrivateLinkApproved` to flap from
True to Unknown because reconciliation eagerly wrote Pending/Error. Current-generation approved
status is fail-static: discovery still refreshes independently, but transient approval reads do not
downgrade confirmed approval.
Provider idempotency compared Azure's canonical `Global` location to desired `global`
case-sensitively, causing profile/endpoint rewrites every reconcile and perpetual
`Provider.Ready=false` with no pending resources. Location equality is case-insensitive and
normalized before desired-state comparison.
The fixed hub image initially remained Pending during rolling update because the one-node hub was
at 97% requested CPU. Phase 7 renders a 25m hub controller request, matching the existing reduced
member validation requests, so old and new replicas can overlap during rollout.

### Phase 7 retained-run hardening

The user approved targeted fixes to retained run `p7-10082105` while away, but no new run or
cleanup. The hardening pass will:

- make chart init containers, rather than direct blanket CRD application, own networking CRD
  installation by hub/member mode;
- apply controller manifests and wait for CRD installers before applying custom Gateway/backend
  resources;
- audit every literal command block for standalone prerequisites, shell preservation, idempotency,
  and expected verification;
- reconcile only missing/corrected identities, scoped role assignments, manifests, and workloads
  in the retained environment; and
- preserve the environment and diagnostics on any failure.

#### Retained-run validation result

- Retained run `p7-10082105` completed all seven setup/join stages.
- Hub and member controllers are Available; both networking agents report joined with heartbeats.
- Both ILB/PLS origins were discovered and exact token-correlated AFD managed connections were
  approved.
- AFD profile, endpoint, origin group, two private origins, route, and WAF security policy are
  provisioned; Gateway reports `Programmed=True`.
- Repeated HTTPS requests returned HTTP 200 and observed both `member-1` and `member-2`.
- `X-POC-Block: true` returned HTTP 403 from the WAF.
- Evidence was appended to the ignored run-local results file with label `user-scenario-ready`.
- Label withdrawal, fail-static outage, Gateway deletion ownership, and final cleanup remain for
  the human operator; Phase 7 is still incomplete.
- Gateway deletion is Azure-asynchronous. The runbook submits a nonblocking Kubernetes delete and
  prints Gateway finalizer and AFD profile state until both are absent, rather than appearing stuck
  inside a silent `kubectl delete --wait=true`.
  Gateway deletion reconciliation logs the start of Azure profile deletion, confirmed Azure
  completion, assignment-finalizer release, and Gateway-finalizer removal. Previously successful
  long-running deletion was silent because the provider blocks while polling Azure.

#### Phase 1: Authoritative operator workflow

- [x] **Task L1.1: Replace Section 6 with seven literal command stages.**
  - Each stage separates read-only inspection/preconditions from visible mutations, documents
    expected output, and ends with read-only verification.
  - Existing resources are inspected before conditional creation. In particular, an existing VNet
    is never passed to `az network vnet create`, and existing subnets, AKS clusters, identities,
    federations, assignments, WAF resources, and Kubernetes objects are reused only after
    validation.
  - Success criteria: no authoritative setup command invokes a stage script or setup Make target.
- [x] **Task L1.2: Keep state bookkeeping explicit without duplicating jq internals.**
  - Source `common.sh`, initialize deterministic names/state, and use its non-cloud bookkeeping
    helpers while keeping every Azure and Kubernetes mutation visible in the README.
  - Success criteria: five immutable image references and created/reused resources are recorded.
- [x] **Task L1.3: Render and deploy exact Phase 7 resources.**
  - Document pinned CRDs, populated registration chart, digest-only hub/member/echo/Gateway
    manifests, explicit applies/waits, post-deployment IMC creation, real networking-agent
    heartbeat observation, and aggregate MemberCluster status patching.
  - Success criteria: ordering and mutation boundaries are copy/paste visible.

#### Phase 2: Secondary automation and retry guidance

- [x] **Task L2.1: Move Make/stage entry points to an optional appendix.**
  - Success criteria: automation is explicitly secondary to literal Section 6 commands.
- [x] **Task L2.2: Update troubleshooting and retained-run recovery.**
  - Resume at the failed literal subsection; never recommend rerunning a Make target as the
    authoritative recovery path.
  - Success criteria: the safe VNet recovery rule remains unambiguous.

#### Phase 3: Non-mutating validation

- [x] **Task L3.1: Statically validate documentation and scripts.**
  - Syntax-check extractable README Bash blocks and all existing scripts, run populated Helm
    renders, execute read-only preflight with a fresh valid run ID, run `make fmt`, and run
    `git diff --check`.
  - Success criteria: validation performs no Azure/Kubernetes mutation and Phase 7 remains
    incomplete.

## Plan

### Phase 1: Implement POC Phase 2 - hub member selection

- [x] **Task 1.1: Write selection and assignment tests first.**
  - Cover joined-member filtering, non-matching labels, deterministic ordering and names, empty and
    over-limit selection, reserved namespace placement, idempotence, label changes, and assignment
    condition aggregation.
  - Success criteria: tests fail before the reconciler exists and encode all Phase 2 exit criteria.
- [x] **Task 1.2: Implement the `MultiClusterBackend` reconciler.**
  - Add a hub controller package using the manager client, Fleet MemberCluster GVK, standard label
    selectors, the existing `hubconfig.HubNamespaceNameFormat`, and deterministic assignment names.
  - Write only assignment spec and backend status.
  - Success criteria: selected joined members receive one correct assignment in their reserved hub
    namespace.
- [x] **Task 1.3: Add watches and manager registration.**
  - Watch `MultiClusterBackend`, `MemberCluster`, and `ServiceOriginAssignment`; map member label
    changes and assignment status changes back to affected backends.
  - Register only when the AFD feature is enabled.
  - Success criteria: manager tests prove registration and event mapping.
- [x] **Task 1.4: Validate, document, commit, and push Phase 2.**
  - Run focused unit/envtest/race tests, vet, lint, generation checks, and diff checks.
  - Mark only Phase 2 complete in the POC plan and this breadcrumb.
  - Commit as `feat: add hub backend selection controller` and push.
  - Success criteria: changing labels creates or removes the expected assignments, reconciliation is
    idempotent, and the remote branch contains the Phase 2 commit.

### Phase 2: Implement POC Phase 3 - member origin discovery

- [x] **Task 2.1: Write discovery and status tests first.**
  - Cover Service not found, port not found, non-LoadBalancer Service, non-internal load balancer,
    pending ingress, PLS missing, ready origin facts, status-only writes, and unchanged-status
    no-ops.
  - Success criteria: tests define actionable conditions and member-only status ownership.
- [x] **Task 2.2: Implement Service, ILB, and PLS discovery.**
  - Add a member assignment reconciler with separate scoped hub and local member clients.
  - Put Azure discovery behind a narrow interface and reuse existing cloud configuration.
  - Success criteria: valid pre-created infrastructure yields location, ILB IP, and full PLS ID.
- [x] **Task 2.3: Register the member controller.**
  - Reuse the existing member manager hub cache restricted to its reserved namespace.
  - Add only required local Service and hub assignment RBAC.
  - Success criteria: the member writes assignment status only and cannot modify assignment spec.
- [x] **Task 2.4: Validate, document, commit, and push Phase 3.**
  - Run focused tests and static/generated checks.
  - Commit as `feat: add member origin discovery controller` and push.
  - Success criteria: Phase 3 exit criteria pass and the commit is isolated from later work.

#### Phase 3 execution plan

1. **Tests first**
   - Add table-driven reconciliation tests for missing Services and ports, non-LoadBalancer and
     non-internal Services, pending ingress, missing PLS, ready infrastructure, status-only writes,
     and unchanged-status no-ops.
   - Add focused Azure adapter tests for paged ARM responses and frontend-to-PLS association.
2. **Member discovery controller**
   - Resolve the assignment through the scoped hub client and the referenced Service through the
     local member client.
   - Match the ready ingress IP to an ARM load balancer frontend, then match that frontend resource
     ID to a PLS in the same AKS node resource group.
   - Publish only member-owned assignment status and poll while infrastructure is pending.
3. **Manager, chart, and RBAC wiring**
   - Register the controller on the scoped hub manager with a local Service watch routed through
     the member manager.
   - Initialize the existing `armnetwork/v4` clients only when a new explicit AFD Private Link
     feature flag is enabled.
   - Add only the chart arguments, cloud-config mounting, local Service reads, and assignment
     status permissions required by this phase.
4. **Validation and delivery**
   - Run focused race tests, formatting, vet, lint, generated-manifest checks, and diff checks.
   - Mark only POC Phase 3 and master tasks 2.1-2.4 complete, then create and push the single
     required Phase 3 commit.

Phase 3 implementation uses the approved master plan. The feature remains discovery-only: it does
not create infrastructure, program AFD, or approve Private Link connections.

#### Phase 3 implementation details

- Added table-driven tests before the reconciler. The initial focused test run failed because the
  `Reconciler` did not exist, establishing the intended red test boundary.
- Added a member controller that reads assignments through the scoped hub client, reads Services
  through the local member client, resolves the exact numeric Service port, and requires an
  internal `LoadBalancer` Service with a ready ingress IP.
- Added read-only ARM adapters over the existing `armnetwork/v4` load balancer and Private Link
  Service clients. Discovery matches the Service ingress IP to an LB frontend resource ID and then
  matches that frontend ID to its associated PLS.
- Added status-only publication of `ServiceResolved` and `InfrastructureReady`, unchanged-status
  no-ops, and polling for pending ingress, frontend, or PLS infrastructure.
- Registered the controller on the existing namespace-scoped hub manager and added a local Service
  watch sourced from the member manager cache.
- Added the disabled-by-default `enable-afd-private-link-feature` flag. ARM discovery clients and
  cloud configuration are initialized only when Traffic Manager or this new feature needs them.
- Updated the member chart arguments, cloud-config secret/mount conditions, documentation, and
  assignment read/status RBAC.

#### Phase 3 course corrections

- Running the pinned generator correctly merged assignment status update permissions into
  `config/rbac/role.yaml`, but the current generator also removed two harmless import aliases from
  unrelated generated deepcopy files despite no API changes. Those unrelated generated formatting
  changes were excluded from the Phase 3 change set.
- The repository-wide lint target ran but reported two existing Phase 2 findings in
  `pkg/controllers/hub/multiclusterbackend`: one test complexity warning and one guarded
  `int`-to-`int32` conversion warning. Focused lint for all Phase 3 packages passed; unrelated Phase
  2 code was not changed.
- The first broader member race run lacked `KUBEBUILDER_ASSETS` and could not start envtest. The
  same suite passed after using the repository-pinned `setup-envtest` assets.

#### Phase 3 validation

- Focused race tests passed for the new controller and member manager package.
- All member-controller race tests passed with Kubernetes 1.33 envtest assets.
- Repository-wide `go vet ./...` passed.
- Focused golangci-lint passed for the new controller and member manager packages.
- Helm lint passed for both default and feature-enabled member chart values; feature-enabled
  rendering contains the flag, cloud configuration, and assignment RBAC.
- `controller-gen` manifests and object generation completed. The intended generated RBAC change
  is checked in; unrelated import-alias churn was excluded.
- `go.mod` and `go.sum` are unchanged, so `go mod tidy` was not required.
- `git diff --check` passed.

### Phase 3: Implement POC Phase 4 - AFD Premium provider

- [x] **Task 3.1: Write provider contract and reconciliation tests first.**
  - Cover stable names/order, create/update/no-op/delete, provisioning-state observation, ownership
    collisions, partial failures, Private Link enforcement, and read-only WAF behavior.
  - Success criteria: all Azure SDK operations are exercised through fakes.
- [x] **Task 3.2: Extend the normalized model.**
  - Resolve ready assignment status into private origins grouped by `MultiClusterBackend`.
  - Preserve deterministic ordering and the assignment approval request message.
  - Success criteria: two ready assignments produce two stable private origins.
- [x] **Task 3.3: Implement narrow Azure SDK adapters.**
  - Add `armcdn/v3` v3.0.0 and `armfrontdoor/v2` v2.0.0, update dependencies, and run `go mod
    tidy`.
  - Expose only required AFD operations and WAF `Get`.
  - Success criteria: credentials are not logged and contexts/errors are bounded and classified.
- [x] **Task 3.4: Reconcile the AFD resource graph.**
  - Implement profile, endpoint, origin group, private origins, routes, and WAF security policy with
    deterministic names and complete ownership tags.
  - Success criteria: unchanged input is a no-op, foreign resources are rejected, and external WAF
    deletion is impossible through the interface.
- [x] **Task 3.5: Validate, document, commit, and push Phase 4.**
  - Run focused tests, vet/lint, dependency tidy, generation, and diff checks.
  - Commit as `feat: add AFD Premium provider` and push.
  - Success criteria: Phase 4 exit criteria pass against fakes and the remote contains the isolated
    provider commit.

#### Phase 4 execution plan

1. **Tests first**
   - Extend the normalized model tests for two ready assignments, deterministic private-origin
     ordering and names, exact approval request messages, required Private Link, health probes, WAF,
     and deep-copy behavior.
   - Add fake-backed provider tests for create, update, no-op, delete, provisioning observation,
     foreign ownership, partial retryable failures, and preservation of the external WAF policy.
   - Add adapter and manager-factory tests that compile against the pinned SDK types without Azure
     credentials or network calls.
2. **Normalized backend model**
   - Add `MultiClusterBackend`-derived origin groups and assignment-derived private origins without
     changing existing ServiceImport foundation behavior.
   - Preserve stable ordering, deterministic resource names, and the exact
     `fleet:<assignment-uid>:<request-token>` request message.
3. **AFD provider and Azure adapters**
   - Add the frozen `armcdn/v3` v3.0.0 and `armfrontdoor/v2` v2.0.0 dependencies.
   - Define narrow CRUD interfaces for the controller-owned AFD graph and a read-only WAF `Get`
     interface, then implement concrete SDK adapters using caller-provided contexts and actionable
     wrapped errors.
   - Reconcile a Premium profile, endpoint, origin groups, private origins, routes, and WAF
     security-policy association with deterministic names and complete ownership tags.
4. **Production construction only**
   - Build the Phase 4 provider from the existing gated hub Gateway manager configuration.
   - Do not add GatewayClass, Gateway, or HTTPRoute reconcilers, status writers, finalizers, or
     Phase 5 approval behavior.
5. **Validation and delivery**
   - Run goimports, `go mod tidy`, focused race tests, focused vet/lint, applicable generation
     idempotence checks, and `git diff --check`.
   - Mark only Phase 4 complete, record prior Phase 2 commit `bc5702b` and Phase 3 commit
     `5b5b997`, then create and push the single required Phase 4 commit.

#### Phase 4 frozen-contract blocker

- The pinned `armcdn/v3` v3.0.0 SDK can represent and create the requested AFD Premium profile,
  endpoint, origin groups, private origins, routes, and security policy.
- `armcdn.Profile` and `armcdn.AFDEndpoint` expose `Tags`, but `armcdn.AFDOriginGroup`,
  `armcdn.AFDOrigin`, `armcdn.Route`, and `armcdn.SecurityPolicy` do not expose tags in API
  `2025-06-01`.
- The frozen POC contract requires every owned Azure resource to carry hub identity, Gateway
  namespace/name/UID, and controller identifier tags, and requires rejecting any existing resource
  without matching tags.
- Consequently, the pinned SDK/API cannot implement the frozen ownership contract for four owned
  child resource kinds. Per the user-provided stop condition, Phase 4 stopped without a commit or
  push. The partial tests and implementation remain uncommitted for evidence and must not be
  treated as Phase 4 completion.

#### Phase 4 ownership resolution

- The user approved limiting ownership tags to tag-capable resources.
- The provider verifies the full ownership-tag set on the AFD profile and endpoint.
- Origin groups, origins, routes, and security policies inherit the verified parent ownership and
  are addressed only by their exact Azure parent hierarchy plus deterministic controller-generated
  names.
- The provider does not list, partially match, adopt, mutate, or delete unrelated child resources.
- This is the frozen Phase 4 ownership rule for the pinned `2025-06-01` AFD API.

#### Phase 4 implementation

- Added assignment-derived, deterministically sorted private origins and origin groups to the
  normalized model, including exact `fleet:<assignment-uid>:<request-token>` messages.
- Added deterministic AFD-safe naming with sanitization, hash suffixes, and a conservative
  50-character limit.
- Added narrow provider interfaces for profiles, endpoints, origin groups, origins, routes,
  security policies, and read-only WAF lookup.
- Added concrete `armcdn/v3` v3.0.0 and `armfrontdoor/v2` v2.0.0 adapters.
- Reconciled Premium profile, endpoint, private origin groups/origins, routes, and WAF security
  policy through create/update/no-op observation.
- Observed Azure provisioning state before reporting the provider result ready.
- Rejected foreign tagged profiles/endpoints and left unrelated deterministic-name-mismatched child
  resources untouched.
- Deleted only the verified owned profile, relying on Azure parent deletion for child cleanup; the
  provider interface exposes no WAF mutation or deletion operation.
- Added a production provider factory to the AFD-gated hub Gateway manager without adding Phase 6
  Gateway reconcilers.

#### Phase 4 validation

- Focused race-enabled tests passed for `gatewaymodel`, the AFD provider, and the hub Gateway
  manager.
- Adjacent Phase 2 and Phase 3 controller tests passed during implementation.
- `go vet ./...` passed.
- `golint` passed for both new Phase 4 packages.
- `go mod tidy` is idempotent with the pinned direct SDK dependencies.
- Repository formatting and `git diff --check` passed.
- The initial every-child-tag contract blocker was resolved explicitly by the user; no workaround
  or unsupported SDK field was introduced.

### Phase 4: Implement POC Phase 5 - automated PLS approval

- [x] **Task 4.1: Write approval matcher tests first.**
  - Cover exact assignment UID/token message, PLS ID, pending state, current generation, Service/PLS
    revalidation, optional member-local subscription allowlist, unrelated connections, deleted
    assignments, and replay attempts.
  - Success criteria: only one exact active connection can match.
- [x] **Task 4.2: Implement the PLS Azure client adapter.**
  - Reuse `armnetwork/v4` v4.3.0 for list/get/update connection operations.
  - Keep list and approval behind narrow interfaces.
  - Success criteria: approval updates only the matched connection to `Approved`.
- [x] **Task 4.3: Integrate approval with the member reconciler.**
  - Build the exact `fleet:<assignment-uid>:<request-token>` message, revalidate immediately before
    update, report actionable pending/failure conditions, and publish `PrivateLinkApproved`.
  - Success criteria: unrelated pending connections remain untouched.
- [x] **Task 4.4: Validate, document, commit, and push Phase 5.**
  - Commit as `feat: automate PLS connection approval` and push after focused validation.
  - Success criteria: Phase 5 exit criteria pass and no broad approval path exists.

#### Phase 5 execution plan

1. **Tests first**
   - Add matcher, controller, adapter, and configuration tests for exact complete request messages,
     exact PLS identity, Pending/Approved state, assignment generation/status, immediate Service and
     PLS revalidation, optional requester-subscription policy, replay, ambiguity, deletion, and
     Azure errors.
2. **Narrow Azure adapter**
   - Use the pinned `armnetwork/v4` PLS connection list/get/update operations only.
   - Preserve the fetched connection fields while changing only its connection status to
     `Approved`.
3. **Approval reconciliation**
   - Continue only after Phase 3 discovery is ready, find exactly one exact-message match, and
     re-fetch the assignment, Service, PLS, and connection immediately before approval.
   - Publish actionable `PrivateLinkApproved` conditions without rejecting or changing unrelated
     connections.
4. **Trusted member configuration**
   - Parse an optional comma-separated subscription UUID allowlist at startup, normalize case,
     deduplicate entries, and fail closed on absent/malformed/unlisted managed endpoint IDs when
     configured.
5. **Validation and delivery**
   - Run focused and adjacent race tests, vet, pinned lint, goimports, Helm lint/render, module,
     RBAC, and diff checks before the single Phase 5 commit.

#### Phase 5 implementation

- Phase 5 starts from Phase 4 commit `54caae8f14cba7d8d6a9f632d2c7e3236aecedd9`.
- Added exact-match approval logic for
  `fleet:<assignment-uid>:<request-token>`; no prefix, substring, list-order, or name-prefix match is
  accepted.
- Added a narrow `PrivateEndpointConnectionClient` and `armnetwork/v4` adapter using only PLS
  private endpoint connection list, get, and update. The update is addressed by exact PLS resource
  ID and connection name and changes only the copied connection state to `Approved`.
- Extended the Phase 3 member reconciler to publish current discovery status before approval, find
  exactly one candidate, and immediately re-fetch/revalidate assignment UID/generation/token/status,
  local Service/ILB/PLS, exact Pending connection message/state, and configured subscription.
- Added `PrivateLinkApproved=True/ConnectionApproved`,
  `Unknown/ConnectionPending`, `False/ApprovalValidationFailed`, and
  `Unknown/PrivateLinkApprovalFailed` reporting. Unrelated connections are never modified.
- Added the member-local `afd-requester-subscription-allowlist` flag and
  `afdRequesterSubscriptionAllowlist` chart value. Empty skips the subscription check; configured
  missing, malformed, or unmatched managed private endpoint IDs fail closed.
- Documented the least-privilege Azure read/write actions separately from unchanged Kubernetes
  RBAC. No tenant-verification claim or inferred tenant lookup was added.

#### Phase 5 tests-first evidence

- The first focused test run failed to compile on the intentionally absent
  `findApprovalConnection`, `ParseRequesterSubscriptionAllowlist`,
  `PrivateEndpointConnectionClient`, and reconciler fields.
- The completed table-driven tests cover exact message and PLS matching, Pending and already
  Approved states, token mismatch/replay, multiple matches, allowlist normalization and fail-closed
  cases, assignment deletion/termination/generation changes, current status, Service and PLS
  revalidation, unrelated connections, and Azure list/get/update errors.

#### Phase 5 course corrections

- The first broad member race run lacked `KUBEBUILDER_ASSETS`; rerunning with the repository-pinned
  Kubernetes 1.33 envtest assets passed all member-controller suites.
- The globally installed `golangci-lint` was newer than the repository configuration format and
  rejected it. The repository-pinned v1.64.7 binary passed the focused packages.
- Helm emitted a warning for an unrelated locally installed `helm-unittest` plugin using an
  unsupported `platformHooks` field; both lint invocations and the enabled render still succeeded.
- No API/RBAC markers or module dependencies changed, so regeneration and `go mod tidy` were not
  required.

#### Phase 5 validation

- Focused race tests passed for the ServiceOriginAssignment controller/client/config and member
  manager construction.
- All member-controller race tests passed with repository-pinned Kubernetes 1.33 envtest assets.
- Adjacent Phase 4 normalized gateway-model race tests passed.
- `go vet ./...` passed.
- Repository-pinned focused `golangci-lint` v1.64.7 passed.
- `make fmt` (Go formatting plus goimports) passed without unrelated changes.
- Helm lint passed for default and feature-enabled member chart values; rendering contains both the
  feature flag and normalized allowlist argument.
- Kubernetes RBAC and `go.mod`/`go.sum` are unchanged.
- `git diff --check` passed.

### Phase 5: Implement POC Phase 6 - status and lifecycle

#### Phase 6 implementation

- Added one coherent AFD Gateway orchestrator and a focused GatewayClass reconciler, registered
  only behind `--enable-afd`, with the Phase 4 provider injected instead of discarded.
- Restricted ownership to `azure-fleet-afd` and its exact controller name, one HTTP/80 listener,
  the documented `PathPrefix /` route subset, same-namespace `MultiClusterBackend`, omitted
  `backendRef.port`, Premium SKU, and a syntactically valid readable WAF policy.
- Resolved current-generation assignments into the normalized model, excluded incomplete members,
  and gated `Programmed=True` on provider readiness and current `InfrastructureReady` plus
  `PrivateLinkApproved`.
- Published current-generation Gateway, listener, route-parent, and backend conditions while
  preserving route parent entries owned by other controllers. The Azure-assigned default endpoint
  hostname is published as a Gateway hostname address when available.
- Added stable Gateway/backend finalizers only after ownership begins. Gateway deletion verifies
  and deletes only its tagged AFD parent; selector withdrawal deletes exact origins under a
  verified parent before releasing assignment cleanup finalizers. External WAF, Services, ILBs,
  and PLS resources remain outside all delete interfaces.
- Kept provider failures fail-static for previously programmed resources and made stable observed
  resources no-op/idempotent.
- Generated Gateway API RBAC for GatewayClass, Gateway, HTTPRoute status and Gateway finalizers.

#### Phase 6 tests-first evidence

- The first focused test run failed on the intentionally absent `GatewayClassName`,
  `validateGateway`, `buildRoute`, reconciler, finalizer, and request helpers.
- Added table-driven validation and fake-provider lifecycle tests for class ownership, listener,
  SKU/WAF, backend kind/namespace/port, current readiness and approval, provider readiness/errors,
  observed generations, hostname addresses, no-early-programming, deletion, status ownership, and
  ownership collisions.

#### Phase 6 validation and course corrections

- Focused and all relevant hub-controller race tests passed with repository-pinned Kubernetes 1.33
  envtest assets; adjacent Phase 3/5 member assignment race tests also passed.
- `go vet ./...` and repository-pinned focused `golangci-lint` v1.64.7 passed.
- `make fmt`, controller-gen manifest/deepcopy generation, and `git diff --check` passed.
- Generated deepcopy output was idempotent; generated RBAC contains the required Gateway API rules.
- No module dependency changed, so `go mod tidy` produced no required module update.
- Helm charts contain no hub Gateway manager deployment, so there was no applicable chart
  lint/render target to change; Phase 7 remains entirely unimplemented.

#### Phase 6 execution checklist

Phase 6 starts from Phase 5 commit `96ff6444c3204ec8c6e464c32ea557b8d8e376cd`.
The prior provider baseline is Phase 4 commit `54caae8f14cba7d8d6a9f632d2c7e3236aecedd9`.
The approved plan below is being followed tests-first; Phase 7 provisioning and harness work remain
explicitly out of scope.

- [x] **Task 5.1: Write Gateway/route status and lifecycle tests first.**
  - Cover accepted/resolved/programmed gates, observed generations, incomplete members,
    selector-change withdrawal ordering, backend/Gateway deletion, ownership failures, and
    controller restart/fail-static behavior.
  - Success criteria: tests prevent early `Programmed=True` and unsafe deletion.
- [x] **Task 5.2: Implement GatewayClass, Gateway, and HTTPRoute reconcilers.**
  - Support only `azure-fleet-afd`, the documented HTTP subset, same-namespace
    `MultiClusterBackend`, Premium SKU, and required WAF policy.
  - Success criteria: parent and Gateway conditions follow Gateway API conventions.
- [x] **Task 5.3: Implement finalizers and withdrawal state machines.**
  - Remove an AFD origin before deleting a deselected assignment.
  - Delete only controller-owned AFD resources before removing Gateway/backend finalizers.
  - Success criteria: external WAF, Services, ILBs, and PLS resources are never deleted.
- [x] **Task 5.4: Preserve fail-static behavior.**
  - Make reconciliation observational and idempotent so controller outage does not alter already
    programmed data-plane state.
  - Success criteria: restart tests preserve existing desired resources without recreation.
- [x] **Task 5.5: Validate, document, commit, and push Phase 6.**
  - Commit as `feat: complete Gateway status and lifecycle` and push.
  - Success criteria: all Phase 6 conditions and deletion-order tests pass.

### Phase 6: Implement POC Phase 7 - human-run real-Azure validation

Phase 7 preparation resumes from commit `37ca0ca9828d17b1b792e73be4bea207f9cbc36f`.
This task is limited to the non-mutating harness preparation requested on 2026-10-08. No Azure
or Kubernetes resource may be created, changed, or deleted, and the resulting changes remain
uncommitted pending fresh cloud-mutation confirmation.

#### Phase 7 preparation plan

1. **Phase 1: Encode the scenario and safety contracts.**
   - [x] **Task 1.1:** Add an authoritative operator checklist and read-only shell evidence helper
     that record resource IDs, conditions, and HTTP results across the complete two-member lifecycle.
   - [x] **Task 1.2:** Add non-mutating validation for required environment, immutable images,
     deterministic naming, inventory boundaries, and explicit mutation acknowledgement.
   - Success criteria: every manual assertion has exact commands/expected output and missing
     configuration fails before any mutating command.
2. **Phase 2: Add bounded infrastructure lifecycle scripts.**
   - [x] **Task 2.1:** Add deterministic setup using one tagged resource group, explicit named
     kubeconfig contexts, and an append-only state inventory.
   - [x] **Task 2.2:** Add idempotent cleanup restricted to inventory entries or the exact tagged
     deterministic resource group after subscription and tag validation.
   - Success criteria: setup traps failures, cleanup has no broad discovery/deletion path, and live
     mutation requires `AFD_PLS_E2E_APPROVED=true`.
3. **Phase 3: Integrate and validate without mutation.**
   - [x] **Task 3.1:** Add Makefile targets and operator documentation for preflight, setup,
     evidence collection, and cleanup.
   - [x] **Task 3.2:** Run shell syntax/static checks, Docker/Helm checks, read-only preflight, and
     `git diff --check`.
   - Success criteria: validation passes without `az`/`kubectl`/Helm mutation and the billable plan
     plus cleanup boundary are printed for explicit approval.

- [x] **Task 6.1: Write the human validation and evidence checklist first.**
  - Add exact human-run Azure/Kubernetes commands and a shell evidence helper for hub, two members,
    Service, ILB, PLS, WAF, Gateway resources, traffic assertions, label withdrawal, outage, and
    cleanup. Phase 7 intentionally has no Go/Ginkgo runner.
  - Success criteria: configuration validation fails before any cloud mutation when required inputs
    are missing.
- [x] **Task 6.2: Add idempotent infrastructure setup and cleanup.**
  - Follow existing test script conventions, use phase-specific deterministic names/tags, and
    record every created resource ID.
  - Success criteria: cleanup targets only resources created by this validation run.
- [ ] **Task 6.3: Obtain cloud-provisioning confirmation.**
  - Present the active subscription/context at a non-sensitive summary level, expected billable
    resources, and cleanup scope.
  - Success criteria: the user explicitly approves live provisioning immediately before mutation.
- [ ] **Task 6.4: Perform the complete human-run Azure validation.**
  - Verify normal traffic reaches both members, `X-POC-Block:true` returns 403, label withdrawal
    removes one origin without interruption, hub-controller outage preserves traffic, Gateway
    deletion removes owned AFD resources, and external resources remain.
  - Success criteria: a human witnesses every Phase 7 data-plane/lifecycle result and retains the
    labelled JSONL evidence snapshots.
- [ ] **Task 6.5: Clean up and verify no owned resources remain.**
  - Run cleanup even after failures and query Azure/Kubernetes for deterministic leftovers.
  - Success criteria: cleanup is idempotent and no controller-owned resource remains.
- [ ] **Task 6.6: Document, commit, and push Phase 7.**
  - Commit only after recording truthful human-validation and cleanup results.
  - Success criteria: Phase 7 is not marked complete on a dry run or unexecuted harness.

### Detailed checklist

- [x] Phase 1 / Task 1.1 completed.
- [x] Phase 1 / Task 1.2 completed.
- [x] Phase 1 / Task 1.3 completed.
- [x] Phase 1 / Task 1.4 completed.
- [ ] Phase 2 / Task 2.1 completed.
- [ ] Phase 2 / Task 2.2 completed.
- [ ] Phase 2 / Task 2.3 completed.
- [ ] Phase 2 / Task 2.4 completed.
- [x] Phase 3 / Task 3.1 completed.
- [x] Phase 3 / Task 3.2 completed.
- [x] Phase 3 / Task 3.3 completed.
- [x] Phase 3 / Task 3.4 completed.
- [x] Phase 3 / Task 3.5 completed.
- [x] Phase 4 / Task 4.1 completed.
- [x] Phase 4 / Task 4.2 completed.
- [x] Phase 4 / Task 4.3 completed.
- [x] Phase 4 / Task 4.4 completed.
- [x] Phase 5 / Task 5.1 completed.
- [x] Phase 5 / Task 5.2 completed.
- [x] Phase 5 / Task 5.3 completed.
- [x] Phase 5 / Task 5.4 completed.
- [x] Phase 5 / Task 5.5 completed.
- [x] Phase 6 / Task 6.1 completed.
- [x] Phase 6 / Task 6.2 completed.
- [ ] Phase 6 / Task 6.3 completed.
- [ ] Phase 6 / Task 6.4 completed.
- [ ] Phase 6 / Task 6.5 completed.
- [ ] Phase 6 / Task 6.6 completed.

### Overall success criteria

- Phases 2 through 6 pass focused local/unit/envtest validation.
- Each phase is committed and pushed independently before the next phase begins.
- Phase 7 provisions only after explicit confirmation, passes every live-Azure assertion, and
  cleans up all controller-owned resources.
- The POC plan and this breadcrumb identify the exact commit and validation results for every
  phase.
- The final branch contains six ordered phase commits after Phase 1.

## Decisions

- Use one shared breadcrumb for the user-requested Phase 2–7 delivery so commit sequencing,
  dependencies, and course corrections remain in one source of truth.
- Preserve one commit per POC phase and push immediately after each successful phase.
- Do not start a dependent phase until the preceding phase's tests, documentation, commit, and
  remote verification are complete.
- Keep Azure SDK calls behind narrow interfaces and use fakes for Phases 3–6.
- Reuse `hubconfig.HubNamespaceNameFormat` for assignment placement.
- Keep both experimental CRDs hub-only; member controllers access assignments through the scoped
  hub client.
- Require a new user confirmation before live Phase 7 provisioning despite the overall plan
  approval, because AKS and AFD Premium resources are billable and cleanup is destructive.
- Do not commit or report Phase 7 complete unless the human-run checklist and cleanup are verified.

## Implementation Details

The user approved the delivery plan.

### Phase 2 implementation

- Added a `MultiClusterBackend` reconciler that filters authoritative Fleet v1beta1
  `MemberCluster` resources to `Joined=True`, evaluates the standard label selector, and sorts
  selected names.
- Added deterministic assignment names derived from backend UID and member name with SHA-256.
- Added cryptographically random 32-character correlation tokens that are preserved across
  idempotent reconciles.
- Reconciled assignments only in namespaces produced by `hubconfig.HubNamespaceNameFormat`.
- Added atomic empty and over-limit rejection without partial assignment creation.
- Added assignment-status aggregation into deterministic member summaries and backend
  `Accepted`, `ResolvedRefs`, and pending `Programmed` conditions.
- Added an AFD-origin cleanup finalizer. Deselecting a ready assignment marks it for deletion;
  removal waits for origin readiness to clear. Assignments without ready infrastructure delete
  immediately.
- Added `MemberCluster` and assignment watches plus AFD-feature-gated manager registration.

### Phase 2 validation

- Tests were added before the reconciler and initially failed on the missing `Reconciler`.
- Unit tests cover joined filtering, selector changes, deterministic placement, idempotent token
  preservation, status aggregation, over-limit rejection, and withdrawal ordering.
- Race-enabled focused tests passed for the controller and hub Gateway manager.
- `go vet`, `golint`, repository formatting, and `git diff --check` passed.

### Phase 7 non-mutating preparation

Tasks 6.1 and 6.2 were reopened on 2026-10-08 after review found that the initial harness
depended on four pre-published images and three externally rendered manifests that do not exist.
The approved preparation work is to make the harness self-contained without running mutations:

1. **Phase 1 — build inputs:** add the missing hub Gateway image definition, deterministic echo
   source, image targets, and a minimal hub Gateway chart.
2. **Phase 2 — lifecycle:** make preflight validate local build inputs and the complete billable
   plan; make setup create a run-scoped ACR, publish and digest-resolve all images, provision
   workload identities and resource-group-scoped built-in roles, bootstrap scoped Fleet member access, render
   digest-only manifests, and apply the complete topology.
3. **Phase 3 — safety and alignment:** preserve the approval guard, explicit kubeconfig contexts,
   tagged cleanup boundary, inventory, and human-readable evidence collection.
4. **Phase 4 — non-mutating validation:** run syntax, read-only preflight, Docker/Helm definition
   checks, and whitespace checks. Tasks 6.1/6.2 may be checked
   again only when these preparation criteria pass; Phase 7 remains incomplete until Tasks
   6.3–6.6 are performed.

- Added an authoritative, copy-paste operator checklist for controller/Fleet deployment, two member
  labels, echo Services, AKS-created ILBs/PLS resources, assignment approval, Gateway/backend
  programming, two-origin traffic, WAF 403, label withdrawal, fail-static outage, and ownership
  deletion. Removed the provisional Go/Ginkgo runner by explicit decision.
- Added a read-only shell evidence helper that appends Kubernetes status, Azure resource IDs, and
  HTTP results to the run-specific JSONL file without reading Secrets.
- Added guarded preflight, setup, and cleanup scripts. Names derive only from a validated run ID;
  every mutation path requires `AFD_PLS_E2E_APPROVED=true`; setup writes a JSON inventory and traps
  failures; cleanup validates subscription, exact deterministic names, and source/run tags before
  deleting the primary group or three unavoidable AKS node resource groups.
- Added the missing hub Gateway Dockerfile/image target, deterministic echo source/image, minimal
  hub Gateway chart, and digest support plus static scoped-token support in the existing member
  chart.
- Made setup self-contained: it creates a deterministic tagged Basic ACR, builds/pushes all four
  images, resolves immutable digests, provisions three workload identities and federated
  credentials, creates exact built-in role assignments, records AKS identities/role assignments, and
  renders/applies only digest-pinned manifests through explicit generated contexts.
- Pinned Fleet registration CRDs to `v0.14.0` and Gateway API standard CRDs to `v1.2.1`, matching
  `go.mod`. Setup reuses the repository's existing E2E registration path: Azure-principal
  RoleBindings from `examples/getting-started/charts/hub`, the member chart's refresh-token
  sidecar, and real `InternalMemberCluster` networking-agent join/heartbeat. Only the aggregate
  selector-facing `MemberCluster/Joined=True` status is validation-scoped because the core Fleet
  MemberCluster controller is not shipped here.
- Added a standalone operator runbook covering tools/login, exact subscription selection,
  preflight, billable resources, the explicit mutation acknowledgement, setup/render behavior,
  tagged execution, evidence, monitoring, reruns, recovery, bounded cleanup, deletion
  verification, and environment teardown.

### Phase 7 preparation validation and preflight evidence

- `bash -n` passed for all new scripts. ShellCheck was unavailable.
- Missing-approval setup failed immediately with status 1 before preflight or mutation.
- The final read-only Azure preflight passed against `AKS Fleet Development/Test`
  (`d712bfad-d238-486f-8f1b-bf61a831b712`): both required providers are registered; East US 2
  reported 0/10000 regional vCPUs and 0/100 DSv3-family vCPUs in use.
- The printed plan used run ID `p7-20261008`, primary resource group
  `fleet-afd-pls-p7-20261008`, Basic ACR `fleetp7p720261008d712`, four image repositories, three
  hub controller identity/federation, AKS-managed identities, five resource-scoped built-in role assignments, three one-node
  `Standard_D2as_v4` clusters, one VNet, two ILBs, two PLS resources, one AFD Premium graph, and one
  WAF policy. No provisioning, image publication, Kubernetes mutation, live validation, or cleanup
  ran.
- The subscription custom-role quota was exhausted during the first successful AKS provisioning
  attempt. Bounded cleanup completed. With explicit user approval, the retry uses built-in
  `Contributor` scoped only to the disposable primary RG for the hub identity and
  `Network Contributor` scoped only to each disposable member node RG.
- Docker buildx `--check` passed for all four Dockerfiles; both Helm charts linted; `bash -n` and
  `git diff --check` passed. ShellCheck was unavailable.
- Tasks 6.3 through 6.6 remain unchecked pending fresh mutation confirmation, the live run,
  verified cleanup, and commit/push.

## Changes Made

- Committed and pushed Phase 1 as `395e965`.
- Added this Phase 2–7 delivery breadcrumb.
- Verified the local branch and fork both point to the Phase 1 commit.
- Confirmed Azure CLI authentication, a Kubernetes context, kubectl, and Helm are available.
- Implemented and validated POC Phase 2.
- Implemented and validated POC Phase 4 after the approved ownership-contract correction.
- Implemented and validated POC Phase 3 member origin discovery.
- Reworked and locally validated self-contained Phase 7 Tasks 6.1 and 6.2 without cloud or
  Kubernetes mutation. Phase 7 remains incomplete.

### Phase 7 live-attempt status

- The user explicitly approved provisioning in `AKS Fleet Development/Test` and bounded cleanup of
  the exact tagged primary and node resource groups.
- The initial attempt rejected `Standard_D2s_v3`; bounded cleanup completed. The runbook now uses
  subscription-allowed `Standard_D2as_v4`.
- A later attempt exposed exhausted custom-role quota; bounded cleanup completed. The user approved
  built-in `Contributor` scoped only to the disposable primary RG and `Network Contributor` scoped
  only to each disposable member node RG.
- Context normalization, RG-scoped role-assignment cleanup guards, explicit deployment waits, and
  pre-cleanup diagnostics were added after subsequent guarded attempts.
- The latest attempt reached controller deployment but
  `hub-gateway-controller-manager` did not become available. The termination trap started bounded
  cleanup and retained `.phase7-p7-20261008.results.setup-failure.log`.
- The complete human checklist was not performed. Tasks 6.4-6.6 remain incomplete, and this
  in-progress runbook commit must not be reported as Phase 7 completion.
- At the user's request, setup failures now preserve the partially provisioned environment for
  manual diagnosis and continuation. Diagnostics still run automatically, but cleanup requires an
  explicit `make phase7-e2e-cleanup`; billable resources remain until the operator runs it.
- After reviewing the repository's existing E2E bootstrap, Phase 7 registration will reuse
  `examples/getting-started/charts/hub` to create reserved namespaces and Azure-principal
  RoleBindings, and the member chart's Azure refresh-token sidecar for hub authentication.
- Setup will create `InternalMemberCluster` join requests and wait for the real networking agent
  `Joined=True` status/heartbeat before marking the selector-facing `MemberCluster` joined. Only
  that aggregate `MemberCluster` status remains synthetic because this repository does not ship
  Fleet's core MemberCluster controller.
- Retained diagnostics identified two deployment blockers: the hub cloud config omitted Azure
  location, and the two-container member pod could not fit beside AKS system add-ons with default
  requests on a one-node validation cluster. The hub chart now requires location, Phase 7 renders
  25m member-container CPU requests, and all generated paths are recomputed from the active run ID
  to prevent stale state-path reuse.

### Phase 7 resumable setup refactor

- Replaced the monolithic setup implementation with seven explicit numbered scripts and matching
  Make targets. `phase7-e2e-setup` is now only a warning wrapper that invokes those stages in
  sequence and is not recommended for resume/debug.
- Added shared deterministic initialization, repository-root state paths, exact state validation,
  keyed resource/identity/role upserts, completed-stage deduplication, prerequisite checks, and
  per-stage failure diagnostics without Secret reads. No stage performs automatic cleanup.
- The network stage distinguishes absent and existing VNets. For an existing VNet it validates the
  exact `/16`, ownership tags, and every present subnet prefix/policy before mutation; it creates
  only absent subnets and never updates/deletes an existing subnet. The retained legacy untagged
  VNet may receive tags only when its exact ID is already recorded in the matching run state.
- The AKS stage validates all existing exact clusters before creating missing ones, including tags,
  location, primary/node RG, subnet, node SKU, Standard LB, OIDC, and workload identity. The
  identity stage reuses tagged identities, exact federations and scoped roles, then refreshes
  deterministic kubeconfig contexts.
- Registration now creates CRDs/RoleBindings/MemberClusters before deployment but defers
  `InternalMemberCluster` join. WAF/manifests creates or validates only the exact WAF graph and
  renders locally. Deploy/join applies manifests, waits for controllers/echo, then creates IMCs,
  observes heartbeat, and patches joined status.
- Rewrote the README around numbered commands, expected checks, exact stage mutation boundaries,
  resume/failure semantics, common fixes, and retained run `p7-10082105`. Its next command is
  Section 6.2's read-only inspection block; the documented recovery explicitly forbids deleting
  the in-use subnet.

### Phase 7 refactor static validation

- `bash -n test/e2e/afdprivatelink/scripts/*.sh` and `git diff --check` passed.
- ShellCheck was not installed, so it was not run.
- `make fmt` passed and introduced no unrelated tracked changes.
- Read-only preflight passed with fresh nonexistent run ID `p7-s1009`, the expected subscription,
  branch, and `5093b017e6df521a08af424b497701d3658381a6` ancestor. It validated all stage scripts and
  Make targets. It performed no Azure/Kubernetes mutation and created no run artifacts.
- Populated Helm renders passed for the registration, hub Gateway, and member controller charts.
  Default `helm lint examples/getting-started/charts/hub` still has the existing
  `templates/ns.yaml: invalid Yaml document separator: apiVersion: v1` issue because its required
  member values are absent; the populated Phase 7 render passes.
- Docker daemon/buildx and build-input checks passed. No image build/push, setup stage, cleanup,
  Azure create/update/delete, Kubernetes apply/delete/patch, or Helm install/upgrade ran.

### Phase 7 literal-command runbook implementation

- Section 6 now sources `common.sh`, initializes deterministic names/state, and presents seven
  literal stages. Every stage has separate read-only preconditions, visible conditional mutation
  commands, expected output, and a read-only verification gate.
- VNet creation is guarded by a successful absence check. Retained VNets/subnets, AKS clusters,
  identities, federations, role assignments, WAF objects, and Kubernetes resources are inspected
  before conditional creation; mismatches stop rather than update or replace retained resources.
- Registry publication visibly builds four images and records five immutable references, including
  refresh-token. Registration visibly applies pinned CRDs and the populated existing chart without
  creating IMCs. Rendering includes exact digest Helm values and visible echo/Gateway heredocs.
- Deployment visibly applies each manifest, waits for all controllers/echo, creates IMCs, observes
  a real `ServiceExportImportAgent` heartbeat and joined condition, and only then patches aggregate
  MemberCluster joined status.
- Section 9 now resumes at the failed literal subsection. Make targets, the sequential wrapper, and
  direct stage scripts moved to an explicitly secondary optional appendix.
- The runbook intentionally duplicates the operational command sequence implemented by the seven
  scripts. This gives operators the requested mutation visibility but creates an unavoidable drift
  risk; future setup changes must update and validate both Section 6 and the corresponding script.

### Phase 7 literal-command validation

- Extracted all README `bash` blocks and passed them through `bash -n`.
- `bash -n test/e2e/afdprivatelink/scripts/*.sh` passed.
- Populated hub, member, and getting-started registration Helm renders passed.
- Read-only preflight passed for fresh valid ID `p7-10091647`; it performed account/provider/quota
  queries only and did not create run artifacts or mutate Azure/Kubernetes.
- `make fmt` and `git diff --check` passed; formatting introduced no unrelated tracked changes.
- No stage/setup/cleanup command, image build/push, Azure create/update/delete, or Kubernetes
  apply/delete/patch was executed. Retained `.phase7` artifacts were not touched.
- Phase 7 remains incomplete pending the full human validation, evidence, bounded cleanup, and
  separately approved commit/push.
- `make -n` resolved all seven numbered targets to their intended stage scripts; all stage scripts
  are executable. Final shell syntax and whitespace checks passed after the last helper changes.
- Changes remain uncommitted. Retained `.phase7` artifacts and attached snapshots were not touched.
  Phase 7 remains incomplete.

## Before/After Comparison

### Before

- Phase 1 provides API types and generated artifacts but no reconcilers or provider.

### After

- Phase 4 is complete. The provider can reconcile and observe the deterministic AFD Premium
  resource graph from normalized private origins. Phase 5 can add narrow PLS approval.

## References

- `docs/design/gateway-api-afd-private-link-poc-plan.md` — frozen POC phases and exit criteria.
- `.github/.copilot/breadcrumbs/2026-10-07-2007-gateway-api-afd-private-link-phase-0.md` — frozen
  contract and security decisions.
- `.github/.copilot/breadcrumbs/2026-10-07-2031-gateway-api-afd-private-link-phase-1.md` — API and
  generated-artifact implementation.
- `395e965` — completed and pushed Phase 1 commit.
- `bc5702b` — completed and pushed Phase 2 commit.
- `github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4` — existing
  read-only load balancer and Private Link Service discovery clients.
- `pkg/common/hubconfig` — authoritative reserved member namespace naming.
- `cmd/hub-gateway-controller-manager` — hub Gateway process and feature gate.
- `cmd/member-net-controller-manager` — member process with local and scoped hub clients.
- `pkg/controllers/hub/gatewaymodel` — provider-neutral normalized model foundation.
- `test/scripts` — repository infrastructure setup and cleanup conventions.
- Azure CLI read-only account, provider, and regional quota queries — Phase 7 preflight evidence.
