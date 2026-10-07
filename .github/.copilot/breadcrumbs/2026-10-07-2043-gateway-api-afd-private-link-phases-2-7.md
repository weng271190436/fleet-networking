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

- [ ] **Task 2.1: Write discovery and status tests first.**
  - Cover Service not found, port not found, non-LoadBalancer Service, non-internal load balancer,
    pending ingress, PLS missing, ready origin facts, status-only writes, and unchanged-status
    no-ops.
  - Success criteria: tests define actionable conditions and member-only status ownership.
- [ ] **Task 2.2: Implement Service, ILB, and PLS discovery.**
  - Add a member assignment reconciler with separate scoped hub and local member clients.
  - Put Azure discovery behind a narrow interface and reuse existing cloud configuration.
  - Success criteria: valid pre-created infrastructure yields location, ILB IP, and full PLS ID.
- [ ] **Task 2.3: Register the member controller.**
  - Reuse the existing member manager hub cache restricted to its reserved namespace.
  - Add only required local Service and hub assignment RBAC.
  - Success criteria: the member writes assignment status only and cannot modify assignment spec.
- [ ] **Task 2.4: Validate, document, commit, and push Phase 3.**
  - Run focused tests and static/generated checks.
  - Commit as `feat: add member origin discovery controller` and push.
  - Success criteria: Phase 3 exit criteria pass and the commit is isolated from later work.

### Phase 3: Implement POC Phase 4 - AFD Premium provider

- [ ] **Task 3.1: Write provider contract and reconciliation tests first.**
  - Cover stable names/order, create/update/no-op/delete, provisioning-state observation, ownership
    collisions, partial failures, Private Link enforcement, and read-only WAF behavior.
  - Success criteria: all Azure SDK operations are exercised through fakes.
- [ ] **Task 3.2: Extend the normalized model.**
  - Resolve ready assignment status into private origins grouped by `MultiClusterBackend`.
  - Preserve deterministic ordering and the assignment approval request message.
  - Success criteria: two ready assignments produce two stable private origins.
- [ ] **Task 3.3: Implement narrow Azure SDK adapters.**
  - Add `armcdn/v3` v3.0.0 and `armfrontdoor/v2` v2.0.0, update dependencies, and run `go mod
    tidy`.
  - Expose only required AFD operations and WAF `Get`.
  - Success criteria: credentials are not logged and contexts/errors are bounded and classified.
- [ ] **Task 3.4: Reconcile the AFD resource graph.**
  - Implement profile, endpoint, origin group, private origins, routes, and WAF security policy with
    deterministic names and complete ownership tags.
  - Success criteria: unchanged input is a no-op, foreign resources are rejected, and external WAF
    deletion is impossible through the interface.
- [ ] **Task 3.5: Validate, document, commit, and push Phase 4.**
  - Run focused tests, vet/lint, dependency tidy, generation, and diff checks.
  - Commit as `feat: add AFD Premium provider` and push.
  - Success criteria: Phase 4 exit criteria pass against fakes and the remote contains the isolated
    provider commit.

### Phase 4: Implement POC Phase 5 - automated PLS approval

- [ ] **Task 4.1: Write approval matcher tests first.**
  - Cover exact assignment UID/token message, PLS ID, pending state, current generation, Service/PLS
    revalidation, optional member-local subscription allowlist, unrelated connections, deleted
    assignments, and replay attempts.
  - Success criteria: only one exact active connection can match.
- [ ] **Task 4.2: Implement the PLS Azure client adapter.**
  - Reuse `armnetwork/v4` v4.3.0 for list/get/update connection operations.
  - Keep list and approval behind narrow interfaces.
  - Success criteria: approval updates only the matched connection to `Approved`.
- [ ] **Task 4.3: Integrate approval with the member reconciler.**
  - Build the exact `fleet:<assignment-uid>:<request-token>` message, revalidate immediately before
    update, report actionable pending/failure conditions, and publish `PrivateLinkApproved`.
  - Success criteria: unrelated pending connections remain untouched.
- [ ] **Task 4.4: Validate, document, commit, and push Phase 5.**
  - Commit as `feat: automate PLS connection approval` and push after focused validation.
  - Success criteria: Phase 5 exit criteria pass and no broad approval path exists.

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
- [ ] Phase 3 / Task 3.1 completed.
- [ ] Phase 3 / Task 3.2 completed.
- [ ] Phase 3 / Task 3.3 completed.
- [ ] Phase 3 / Task 3.4 completed.
- [ ] Phase 3 / Task 3.5 completed.
- [ ] Phase 4 / Task 4.1 completed.
- [ ] Phase 4 / Task 4.2 completed.
- [ ] Phase 4 / Task 4.3 completed.
- [ ] Phase 4 / Task 4.4 completed.
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

## Before/After Comparison

### Before

- Phase 1 provides API types and generated artifacts but no reconcilers or provider.

### After

- Phase 2 is complete. Phase 3 can implement member-local Service, ILB, and PLS discovery.

## References

- `docs/design/gateway-api-afd-private-link-poc-plan.md` — frozen POC phases and exit criteria.
- `.github/.copilot/breadcrumbs/2026-10-07-2007-gateway-api-afd-private-link-phase-0.md` — frozen
  contract and security decisions.
- `.github/.copilot/breadcrumbs/2026-10-07-2031-gateway-api-afd-private-link-phase-1.md` — API and
  generated-artifact implementation.
- `395e965` — completed and pushed Phase 1 commit.
- `pkg/common/hubconfig` — authoritative reserved member namespace naming.
- `cmd/hub-gateway-controller-manager` — hub Gateway process and feature gate.
- `cmd/member-net-controller-manager` — member process with local and scoped hub clients.
- `pkg/controllers/hub/gatewaymodel` — provider-neutral normalized model foundation.
- `test/e2e` and `test/scripts` — repository E2E conventions and cleanup patterns.
