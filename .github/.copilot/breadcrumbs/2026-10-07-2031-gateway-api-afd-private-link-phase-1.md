# Gateway API AFD Private Link POC Phase 1

## Requirements

- Complete Phase 1 of the Gateway API AFD Private Link POC plan.
- Add `MultiClusterBackend` and `ServiceOriginAssignment` v1alpha1 API types.
- Add condition and reason constants for both resources.
- Register both resources with the existing `networking.fleet.azure.com/v1alpha1` scheme.
- Generate deepcopy code, CRDs, and RBAC with repository tooling.
- Ensure the existing hub chart CRD-installer path installs both CRDs.
- Add POC example manifests.
- Validate creation, schema constraints, status subresources, packaging, and generated artifacts.

## Additional comments from user

- The user requested: "do phase 1".
- Phase 0 was completed and committed as `1e2d8d4`.

## Plan

### Phase 1: Add failing API and packaging tests first

- [x] **Task 1.1: Add scheme-registration unit tests.**
  - Extend the hub Gateway manager's table-driven scheme test for `MultiClusterBackend` and
    `ServiceOriginAssignment`.
  - Success criteria: the tests fail until both root types are registered.
- [x] **Task 1.2: Add envtest API-contract tests.**
  - Test valid creation, required fields, selector/port/path/token/connectivity validation,
    defaulting, and status-subresource behavior for both resources.
  - Success criteria: the tests encode the frozen Phase 0 field contract and initially fail because
    the types and CRDs do not exist.
- [x] **Task 1.3: Add CRD-installer packaging expectations.**
  - Extend the hub-mode CRD collection test to require both generated CRDs and confirm member mode
    excludes them.
  - Success criteria: the packaging test fails until the generated CRDs are present.

### Phase 2: Implement the v1alpha1 APIs

- [x] **Task 2.1: Add `MultiClusterBackend` API types.**
  - Add root/list types, service/selector/probe spec types, status/member summaries, validation and
    default markers, print columns, and scheme registration.
  - Success criteria: generated schema enforces the frozen selector, port, path, list-size, and
    status contracts.
- [x] **Task 2.2: Add `ServiceOriginAssignment` API types.**
  - Add root/list types, immutable backend identity, local Service reference, Private Link enum,
    approval token, discovered origin status, validation markers, print columns, and scheme
    registration.
  - Success criteria: generated schema rejects invalid connectivity and tokens and enables the
    status subresource.
- [x] **Task 2.3: Add condition and reason constants.**
  - Define typed condition and reason constants for acceptance, reference resolution, programming,
    member readiness, Service/PLS discovery, and connection approval.
  - Success criteria: later controller phases can publish status without string literals.
- [x] **Task 2.4: Add future-controller RBAC markers.**
  - Add least-privilege hub Gateway manager markers for the new resources, status, finalizers, and
    Fleet `MemberCluster` reads.
  - Success criteria: generated `config/rbac/role.yaml` contains the Phase 1 resource permissions
    without granting assignment-status writes to the hub.

### Phase 3: Generate and package artifacts

- [x] **Task 3.1: Run repository code and manifest generation.**
  - Run `make generate manifests` and format API code with repository tooling.
  - Success criteria: deepcopy, both CRDs, and RBAC are deterministic and contain status
    subresources.
- [x] **Task 3.2: Wire chart CRD installation.**
  - Use the existing hub chart init container and `net-crd-installer` image, which packages all
    generated `config/crd/bases` resources in hub mode.
  - Update its collection test for both new CRDs; do not install these hub-only resources in member
    clusters.
  - Success criteria: the chart packaging test proves both CRDs are selected in hub mode and
    excluded in member mode.
- [x] **Task 3.3: Add POC example manifests.**
  - Add user-facing Gateway, HTTPRoute, and MultiClusterBackend examples plus an explicitly
    internal, non-user-applied ServiceOriginAssignment example.
  - Success criteria: all examples parse and match the generated CRD schemas.

### Phase 4: Validate and document completion

- [x] **Task 4.1: Run focused unit and envtest suites.**
  - Run tests for the API package, hub Gateway manager scheme, v1alpha1 API integration suite, and
    CRD installer utilities.
  - Success criteria: all focused tests pass.
- [x] **Task 4.2: Run static and generated-artifact checks.**
  - Run formatting, `go vet` for modified packages, `git diff --check`, regeneration idempotence,
    and example validation.
  - Success criteria: checks pass and regeneration leaves no additional changes.
- [x] **Task 4.3: Update the POC plan and this breadcrumb.**
  - Mark Phase 1 complete only after all tests pass and record files, decisions, validation, and
    any course corrections.
  - Success criteria: documentation accurately reflects the implementation.

### Detailed checklist

- [x] Phase 1 / Task 1.1 completed.
- [x] Phase 1 / Task 1.2 completed.
- [x] Phase 1 / Task 1.3 completed.
- [x] Phase 2 / Task 2.1 completed.
- [x] Phase 2 / Task 2.2 completed.
- [x] Phase 2 / Task 2.3 completed.
- [x] Phase 2 / Task 2.4 completed.
- [x] Phase 3 / Task 3.1 completed.
- [x] Phase 3 / Task 3.2 completed.
- [x] Phase 3 / Task 3.3 completed.
- [x] Phase 4 / Task 4.1 completed.
- [x] Phase 4 / Task 4.2 completed.
- [x] Phase 4 / Task 4.3 completed.

### Overall success criteria

- Both resources are registered in `networking.fleet.azure.com/v1alpha1`.
- Generated CRDs enforce the frozen Phase 0 contract and expose status subresources.
- Hub and member status ownership is reflected in generated RBAC and write paths.
- The hub chart CRD installer packages both CRDs; member-mode installation excludes them.
- Both resources can be created and status-updated in envtest.
- Example manifests conform to the generated schemas.
- Focused tests, vet, formatting, and generation-idempotence checks pass.

## Decisions

- Reuse the existing v1alpha1 API package rather than creating a new group or version.
- Keep one type file per root API and keep resource-specific constants with that API.
- Use the existing `AddToScheme` registration path already consumed by hub and member managers.
- Treat both CRDs as hub-cluster resources. Members access `ServiceOriginAssignment` through their
  existing scoped hub client; the CRD is not installed in member clusters.
- Use the existing hub chart's `net-crd-installer` init container rather than adding chart-local
  CRD copies or a second installation mechanism.
- Generate hub-controller RBAC now; member assignment-status RBAC is namespace-scoped hub
  authorization and will be wired with the member controller in Phase 3.
- Keep the hub manager unable to update `ServiceOriginAssignment.status`.

## Implementation Details

The user approved the implementation plan. Scheme, envtest contract, and CRD packaging tests were
added first. The red test run confirmed:

- neither new GVK is registered;
- neither generated CRD exists;
- the new API symbols do not exist; and
- direct envtest execution requires the repository-managed `KUBEBUILDER_ASSETS` path.

### Implemented API surface

- `MultiClusterBackend` includes the frozen Service reference, non-empty label selector, health
  probe default, selected/ready counts, conditions, and map-keyed member summaries.
- `ServiceOriginAssignment` includes immutable backend identity, member-local Service reference,
  Private Link connectivity, correlation token, discovered origin status, and member-owned
  conditions.
- Both root/list types register through the existing v1alpha1 `SchemeBuilder`.
- Typed conditions and reasons cover backend acceptance/resolution/programming, per-member
  readiness, Service/port resolution, infrastructure readiness, and Private Link approval.
- The generated hub RBAC can reconcile backend status and assignment spec but has only read access
  to assignment status.

### Course corrections

- The first generator invocation failed because the configured Microsoft Go proxy returned 401.
  Retrying the pinned repository tools through `https://proxy.golang.org,direct` succeeded.
- Controller-gen does not accept `MinLength` on the `types.UID` alias. The non-empty UID constraint
  is expressed as CEL while preserving `types.UID` and the immutability rule.
- Initial status assertions observed the controller-runtime cache before it converged. Tests now
  wait for the cached status value rather than weakening status-subresource validation.
- Kubernetes create clears submitted status on the in-memory assignment object. The test retains
  the desired status value separately before performing the member-style status update.

### Validation results

- Focused race-enabled tests passed for the hub Gateway manager, v1alpha1 API envtest suite, and
  CRD installer utilities.
- `go vet` passed for all modified Go packages.
- `golint` passed for both new API type files.
- All generated artifacts were unchanged by a second `make generate manifests fmt` run.
- Both examples validated against their generated CRD schemas.
- The hub chart linted and rendered with `crdInstaller.enabled=true`.
- `git diff --check` passed.

## Changes Made

- Added this Phase 1 breadcrumb.
- Reviewed API markers, generation targets, envtest suites, scheme registration, CRD installer,
  chart deployment, Docker packaging, and RBAC generation.
- Added failing scheme-recognition, API-contract, status-subresource, and CRD packaging tests.
- Added both root/list APIs, nested spec/status types, validation/defaulting markers, print columns,
  registration, and typed conditions/reasons.
- Added least-privilege hub Gateway manager RBAC markers, intentionally granting only `get` on
  `ServiceOriginAssignment.status`.
- Generated deepcopy implementations, both CRDs, and combined manager RBAC with controller-gen
  v0.20.0.
- Confirmed both generated CRDs expose status subresources and that generated RBAC does not grant
  the hub assignment-status update or patch.
- Documented opt-in CRD installation through the existing hub chart init container.
- Added user-facing Gateway, HTTPRoute, and MultiClusterBackend examples plus a clearly marked
  internal ServiceOriginAssignment example.
- Confirmed hub-mode CRD collection includes both APIs while member mode excludes them.
- Validated the chart render and both example custom resources against their generated schemas.
- Updated the POC plan status and marked every Phase 1 item complete.

## Before/After Comparison

### Before

- Phase 0 defines the API contracts only in design documentation.
- Neither root type, generated CRD, generated deepcopy implementation, RBAC rule, packaging
  expectation, nor repository example exists.

### After

- Both APIs, schemas, generated artifacts, RBAC, chart installation path, tests, and examples are
  implemented and validated. Phase 2 can add the hub member-selection reconciler.

## References

- `docs/design/gateway-api-afd-private-link-poc-plan.md` — frozen API contract and Phase 1
  checklist.
- `.github/.copilot/breadcrumbs/2026-10-07-2007-gateway-api-afd-private-link-phase-0.md` — approved
  Phase 0 decisions and validation.
- `api/v1alpha1` — existing type, marker, condition, and scheme-registration patterns.
- `test/apis/v1alpha1` — envtest API validation suite.
- `cmd/hub-gateway-controller-manager` — Gateway API scheme and future hub-controller process.
- `cmd/net-crd-installer` — generated CRD packaging and hub/member selection.
- `charts/hub-net-controller-manager` — existing CRD installer init-container deployment.
- `config/crd/bases` and `config/rbac/role.yaml` — controller-gen outputs.
- `Makefile` — `generate`, `manifests`, `fmt`, `vet`, and focused test commands.
