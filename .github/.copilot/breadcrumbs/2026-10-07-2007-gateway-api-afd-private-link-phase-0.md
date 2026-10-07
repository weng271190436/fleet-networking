# Gateway API AFD Private Link POC Phase 0

## Requirements

- Complete Phase 0 of the Gateway API AFD Private Link POC plan.
- Review and freeze the `MultiClusterBackend` and `ServiceOriginAssignment` v1alpha1 contracts.
- Confirm the authoritative Fleet `MemberCluster` GVK, label source, and eligibility rules.
- Confirm the selected-member hard limit and AFD profile ownership boundary.
- Pin the Azure SDK modules and API versions for AFD, WAF lookup, and PLS approval.
- Record only approval trust checks that can be enforced with Azure's observable connection data.
- Document public and internal API stability expectations.
- Validate the example manifests against the frozen field contract.

## Additional comments from user

- The user requested: "do phase 0".
- The work is based on `docs/design/gateway-api-afd-private-link-poc-plan.md`.

## Plan

### Phase 1: Freeze schema and ownership tests first

- [x] **Task 1.1: Review both proposed CRD schemas field by field.**
  - Record field type, required/optional status, default, validation, mutability, and controller
    ownership.
  - Success criteria: every spec and status field has one authoritative writer and deterministic
    validation semantics.
- [x] **Task 1.2: Review the example resources against the proposed schemas.**
  - Verify required fields, enum values, namespaced references, port ownership, selector syntax,
    condition structure, and status-only fields.
  - Success criteria: the examples contain no ambiguous, duplicated, or unenforceable fields.

### Phase 2: Freeze external contracts and security boundaries

- [x] **Task 2.1: Record the Fleet member-selection contract.**
  - Pin `cluster.kubernetes-fleet.io/v1beta1, Kind=MemberCluster`.
  - Use `metadata.labels` as the selector source.
  - Require `Joined=True`; do not gate on the currently unused `Healthy` condition.
  - Success criteria: member eligibility can be implemented without inferring undocumented state.
- [x] **Task 2.2: Record AFD scale and ownership boundaries.**
  - Keep a hard limit of 40 selected members against the documented 50-origin AFD origin-group
    limit.
  - Keep one controller-owned AFD Premium profile per Gateway.
  - Success criteria: over-limit selection fails atomically and resource ownership is unambiguous.
- [x] **Task 2.3: Pin Azure SDK and REST API surfaces.**
  - AFD resource graph: `armcdn/v3 v3.0.0`, API `2025-06-01`.
  - Existing WAF policy lookup: `armfrontdoor/v2 v2.0.0`, API `2025-10-01`, for
    `Microsoft.Network/frontdoorWebApplicationFirewallPolicies`.
  - PLS connection approval: existing `armnetwork/v4 v4.3.0`, API `2023-05-01`.
  - Success criteria: every Azure resource type maps to a matching SDK client and stable API.
- [x] **Task 2.4: Freeze enforceable Private Link approval checks.**
  - Require the active assignment UID and generation, exact PLS resource ID, pending connection
    state, exact high-entropy request token, and still-valid local Service/PLS.
  - Parse and validate the managed private endpoint subscription only when an explicit allowlist is
    configured.
  - Do not claim direct tenant verification because the PLS connection response does not expose a
    tenant ID.
  - Success criteria: the POC never approves by connection order, name prefix, or broad pending
    state, and the documentation distinguishes correlation from authentication.

### Phase 3: Document and validate the frozen contract

- [x] **Task 3.1: Update the POC plan with final Phase 0 decisions.**
  - Add schema tables, ownership rules, eligibility semantics, version pins, approval constraints,
    and stability classifications.
  - Mark the Phase 0 checklist complete only after validation.
  - Success criteria: the plan is sufficient input for Phase 1 type and CRD generation.
- [x] **Task 3.2: Run documentation and repository validation.**
  - Check formatting, links, YAML parsing, focused consistency searches, and the final diff.
  - Success criteria: examples parse, references resolve, the worktree contains only intended
    documentation changes, and all Phase 0 exit criteria are met.
- [x] **Task 3.3: Complete this breadcrumb.**
  - Record implementation details, changes, validation results, and any course corrections.
  - Success criteria: the breadcrumb accurately describes the approved and completed work.

### Detailed checklist

- [x] Phase 1 / Task 1.1 completed.
- [x] Phase 1 / Task 1.2 completed.
- [x] Phase 2 / Task 2.1 completed.
- [x] Phase 2 / Task 2.2 completed.
- [x] Phase 2 / Task 2.3 completed.
- [x] Phase 2 / Task 2.4 completed.
- [x] Phase 3 / Task 3.1 completed.
- [x] Phase 3 / Task 3.2 completed.
- [x] Phase 3 / Task 3.3 completed.

### Overall success criteria

- Both CRD contracts are detailed enough to implement without unresolved field ownership.
- Member selection uses the authoritative Fleet GVK, labels, and joined condition.
- The 40-member limit and profile-per-Gateway boundary are explicit.
- Azure SDK modules and their embedded API versions are pinned.
- Private Link approval checks match data actually exposed by Azure.
- Public and internal API stability expectations are explicit.
- Example manifests conform to the frozen contract.
- The Phase 0 checklist in the POC plan is complete.

## Decisions

- **Approved:** Keep `MultiClusterBackend` as a namespaced, user-facing experimental v1alpha1 API.
- **Approved:** Keep `ServiceOriginAssignment` as a namespaced, internal experimental v1alpha1 API
  with hub-owned spec and member-owned status.
- **Approved:** Use `cluster.kubernetes-fleet.io/v1beta1, Kind=MemberCluster`,
  `metadata.labels`, and `Joined=True`; the `Healthy` condition is currently documented as unused.
- **Approved:** Keep the hard limit at 40 members, leaving 20 percent headroom below AFD's
  50-origin-per-origin-group platform limit.
- **Approved:** Keep one AFD Premium profile per Gateway, with ownership tags including hub
  identity, Gateway namespace/name/UID, and controller identifier.
- **Approved:** Use `armcdn/v3 v3.0.0` (`2025-06-01`) for AFD, `armfrontdoor/v2 v2.0.0`
  (`2025-10-01`) for the pre-created Microsoft.Network WAF policy, and the existing
  `armnetwork/v4 v4.3.0` (`2023-05-01`) for PLS approval.
- **Approved:** Treat the request token as correlation, not authentication. Tenant identity is not
  directly exposed by the PLS API; requester subscription validation is conditional on a configured
  allowlist of observable AFD-managed private endpoint subscription IDs.

## Implementation Details

The user approved the implementation plan.

### Schema review

`MultiClusterBackend` has one writer for `spec` (the user) and one writer for all of `status` (the
hub backend-selection controller):

- `spec.service.name`: required DNS-1123 Service name.
- `spec.service.port`: required integer from 1 through 65535.
- `spec.clusterSelector`: required standard `metav1.LabelSelector`; an empty selector is rejected
  to prevent accidental fleet-wide selection.
- `spec.healthProbe.path`: optional absolute path, default `/`.
- `status.observedGeneration`, counts, conditions, and member summaries: hub-owned and read-only to
  users through the status subresource.

`ServiceOriginAssignment` has a strict split writer model:

- The hub backend-selection controller owns all of `spec`.
- The selected member controller owns all of `status` through the status subresource.
- `spec.backendRef.uid` is required to prevent adoption after backend recreation.
- `spec.serviceRef` carries the member-local Service namespace, name, and numeric port.
- `spec.connectivity.type` is required and has the single POC value `PrivateLink`.
- `spec.approval.requestToken` is required, unique per assignment UID, unpredictable, and short
  enough for Azure's 140-character request-message limit.
- Requester subscription trust policy is not assignment data; it is member-local trusted
  controller configuration.
- `status.origin` contains only discovered location, ILB address, and PLS resource ID.
- `status.conditions` contains member-owned discovery and approval state.

The examples are structurally compatible after removing `trustedSubscriptionID` from the
assignment and documenting the member-local trust policy. `HTTPRoute.backendRef.port` remains
unset because `MultiClusterBackend.spec.service.port` is authoritative.

### External contract review

- Only `MemberCluster` objects with `Joined=True` are eligible before label selection. The
  `Healthy` MemberCluster condition is explicitly documented as unused in Fleet v0.14.0 and is not
  an eligibility gate.
- The controller matches the required non-empty `clusterSelector` against `metadata.labels`.
- Selection is deterministic and fails atomically when zero or more than 40 eligible members
  match.
- One AFD Premium profile is exclusively owned per Gateway. Existing resources without the full
  ownership-tag set are rejected rather than adopted.
- AFD profile, endpoint, origin-group, origin, route, and security-policy operations use
  `armcdn/v3 v3.0.0`, whose clients send API `2025-06-01`.
- Reading the pre-created
  `Microsoft.Network/frontdoorWebApplicationFirewallPolicies` resource uses
  `armfrontdoor/v2 v2.0.0`, whose client sends API `2025-10-01`. The controller never uses its
  create, update, or delete operations.
- PLS connection listing and approval reuse `armnetwork/v4 v4.3.0`, whose client sends API
  `2023-05-01`.
- Approval requires an active matching assignment UID and generation, exact discovered PLS ID,
  pending connection state, exact request token, and revalidated local Service/PLS.
- The provider connection exposes a managed private endpoint resource ID but no direct tenant ID.
  An optional member-local subscription allowlist can validate the subscription parsed from that
  ID. If the allowlist is configured and the ID is absent or unmatched, approval fails closed.

### Validation results

- Parsed all four fenced YAML examples successfully.
- Confirmed both related design-document links resolve in the repository.
- Confirmed no stale `trustedSubscriptionID`, trusted-tenant claim, proposed-shape label, or
  incomplete Phase 0 wording remains.
- `git diff --check` passed.
- Documentation-only change; Go tests, vet, and lint were not required.

## Changes Made

- Added this Phase 0 breadcrumb.
- Completed repository, Fleet API, Azure SDK, and Azure platform-limit research.
- Updated the POC plan status to Phase 0 complete and approved for Phase 1.
- Added field-level schema and ownership contracts for both experimental CRDs.
- Pinned Fleet member eligibility, the 40-member limit, and one AFD profile per Gateway.
- Pinned the AFD, WAF lookup, and PLS approval SDK modules and embedded API versions.
- Removed hub-supplied requester trust policy from `ServiceOriginAssignment.spec`.
- Replaced unenforceable tenant verification with documented Azure observability constraints and an
  optional member-local requester subscription allowlist.
- Added explicit public and internal API stability classifications.
- Marked every Phase 0 checklist item complete in the POC plan.
- Validated YAML examples, document references, stale wording, and diff whitespace.

## Before/After Comparison

### Before

- The POC plan contained proposed CRD examples and several unconfirmed Phase 0 decisions.
- Azure API versions and enforceable requester identity checks were not pinned.

### After

- The POC contract is detailed enough to implement Phase 1 types and generated schemas without
  unresolved field ownership or external API-version choices.
- The approval model now distinguishes correlation from authentication and fails closed when a
  configured requester subscription cannot be verified.

## References

- `docs/design/gateway-api-afd-private-link-poc-plan.md` — POC requirements and Phase 0 checklist.
- `docs/design/gep-1748-gateway-api.md` — existing Gateway architecture, annotations, ownership,
  and status conventions.
- `.github/.copilot/breadcrumbs/2026-08-17-2356-gep-1748-gateway-api.md` — prior Gateway design and
  foundation decisions.
- Fleet `go.goms.io/fleet@v0.14.0/apis/cluster/v1beta1` — authoritative MemberCluster GVK,
  labels, and conditions.
- Azure SDK for Go `armcdn/v3 v3.0.0` — AFD resource clients using API `2025-06-01`.
- Azure SDK for Go `armfrontdoor/v2 v2.0.0` — Microsoft.Network WAF policy client using API
  `2025-10-01`.
- Azure SDK for Go `armnetwork/v4 v4.3.0` — PLS connection client using API `2023-05-01`.
- Microsoft Learn, Azure Front Door Standard and Premium service limits — 50 origins per origin
  group.
- Microsoft Learn, Secure your origin with Private Link in Azure Front Door Premium — AFD creates
  a managed private endpoint whose connection requires origin-side approval.
