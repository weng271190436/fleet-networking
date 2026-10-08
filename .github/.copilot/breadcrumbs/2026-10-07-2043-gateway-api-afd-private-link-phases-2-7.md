# Gateway API AFD Private Link POC Phases 2-7

## Requirements

- Implement POC Phases 2 through 7 in dependency order.
- Start each phase with focused failing tests when possible.
- Preserve the frozen Phase 0 API and security contracts.
- Keep each phase independently reviewable and validated.
- Create and push exactly one implementation commit for each completed phase.
- Do not squash, amend, or combine phase commits.
- Run the real Azure E2E test before claiming Phase 7 complete.
- Obtain fresh confirmation before provisioning or deleting billable Azure/AKS resources.

## Additional comments from user

- The user requested: "commit push. continue with phase 2 to 7, where each phase should have its
  own commit".
- Phase 1 was committed and pushed as `395e965`.
- Azure CLI authentication, a Kubernetes context, Helm, and kubectl are currently available.

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

- [ ] **Task 5.1: Write Gateway/route status and lifecycle tests first.**
  - Cover accepted/resolved/programmed gates, observed generations, incomplete members,
    selector-change withdrawal ordering, backend/Gateway deletion, ownership failures, and
    controller restart/fail-static behavior.
  - Success criteria: tests prevent early `Programmed=True` and unsafe deletion.
- [ ] **Task 5.2: Implement GatewayClass, Gateway, and HTTPRoute reconcilers.**
  - Support only `azure-fleet-afd`, the documented HTTP subset, same-namespace
    `MultiClusterBackend`, Premium SKU, and required WAF policy.
  - Success criteria: parent and Gateway conditions follow Gateway API conventions.
- [ ] **Task 5.3: Implement finalizers and withdrawal state machines.**
  - Remove an AFD origin before deleting a deselected assignment.
  - Delete only controller-owned AFD resources before removing Gateway/backend finalizers.
  - Success criteria: external WAF, Services, ILBs, and PLS resources are never deleted.
- [ ] **Task 5.4: Preserve fail-static behavior.**
  - Make reconciliation observational and idempotent so controller outage does not alter already
    programmed data-plane state.
  - Success criteria: restart tests preserve existing desired resources without recreation.
- [ ] **Task 5.5: Validate, document, commit, and push Phase 6.**
  - Commit as `feat: complete Gateway status and lifecycle` and push.
  - Success criteria: all Phase 6 conditions and deletion-order tests pass.

### Phase 6: Implement POC Phase 7 - real Azure E2E

- [ ] **Task 6.1: Write the E2E scenario and cleanup tests first.**
  - Add a tagged Ginkgo scenario and reusable Azure/Kubernetes helpers for hub, two members, Service,
    ILB, PLS, WAF, Gateway resources, traffic assertions, label withdrawal, outage, and cleanup.
  - Success criteria: configuration validation fails before any cloud mutation when required inputs
    are missing.
- [ ] **Task 6.2: Add idempotent infrastructure setup and cleanup.**
  - Follow existing test script conventions, use phase-specific deterministic names/tags, and
    record every created resource ID.
  - Success criteria: cleanup targets only resources created by this E2E run.
- [ ] **Task 6.3: Obtain cloud-provisioning confirmation.**
  - Present the active subscription/context at a non-sensitive summary level, expected billable
    resources, and cleanup scope.
  - Success criteria: the user explicitly approves live provisioning immediately before mutation.
- [ ] **Task 6.4: Run the complete Azure E2E.**
  - Verify normal traffic reaches both members, `X-POC-Block:true` returns 403, label withdrawal
    removes one origin without interruption, hub-controller outage preserves traffic, Gateway
    deletion removes owned AFD resources, and external resources remain.
  - Success criteria: every Phase 7 data-plane and lifecycle assertion passes.
- [ ] **Task 6.5: Clean up and verify no owned resources remain.**
  - Run cleanup even after failures and query Azure/Kubernetes for deterministic leftovers.
  - Success criteria: cleanup is idempotent and no controller-owned resource remains.
- [ ] **Task 6.6: Document, commit, and push Phase 7.**
  - Commit as `test: add AFD Private Link Azure E2E` and push only after recording truthful test
    results.
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
- [ ] Phase 5 / Task 5.1 completed.
- [ ] Phase 5 / Task 5.2 completed.
- [ ] Phase 5 / Task 5.3 completed.
- [ ] Phase 5 / Task 5.4 completed.
- [ ] Phase 5 / Task 5.5 completed.
- [ ] Phase 6 / Task 6.1 completed.
- [ ] Phase 6 / Task 6.2 completed.
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
- Do not commit or report Phase 7 complete unless the real E2E runs and cleanup is verified.

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

## Changes Made

- Committed and pushed Phase 1 as `395e965`.
- Added this Phase 2–7 delivery breadcrumb.
- Verified the local branch and fork both point to the Phase 1 commit.
- Confirmed Azure CLI authentication, a Kubernetes context, kubectl, and Helm are available.
- Implemented and validated POC Phase 2.
- Implemented and validated POC Phase 4 after the approved ownership-contract correction.
- Implemented and validated POC Phase 3 member origin discovery.

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
- `test/e2e` and `test/scripts` — repository E2E conventions and cleanup patterns.
