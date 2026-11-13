# Service Gateway for Standard AKS members

## Requirements and approved scope

- Standard AKS members retain ordinary CCM reconciliation.
- Use a distinct Gateway API class/controller, a configured hub Gateway, and
  TCPRoute backends referencing exported ServiceImports.
- Export optional pod node-location metadata without changing ordinary MCS.
- Provision gateway, NAT/public IP and per-application Service LB/public IP.
- Explicit subnet-owner opt-in is required: attachment changes effective egress.
- Reject existing Azure resources without matching Fleet ownership.
- Last application removal restores subnet associations before deleting shared
  resources; readiness loss or API errors must not cause network teardown.
- Existing AFD behavior and live Azure lab resources remain unchanged.

## Plan and success criteria

- [x] Define and validate configuration, ownership and desired-state types.
- [x] Add gated node-location export and focused regression tests.
- [x] Implement Azure lifecycle with ordered cutover/withdrawal and durable state.
- [x] Resolve Gateway/TCPRoute/ServiceImport graph and wire gated managers/charts.
- [x] Regenerate API/RBAC artifacts and document configuration and migration risks.
- [x] Run focused tests and manager build; record exact validation and limitations.

## Decisions

The user approved TCPRoute to ServiceImport instead of another enrollment CRD.
Subnet/VNet resources are never deleted. Shared resource ownership follows
Gateway UID; application ownership follows route/backend identity. The source
lab established static shared-VNet ingress and association-dependent egress,
not general AKS migration support.

## References

- `docs/manual-multi-cluster-service-gateway/README.md` in the neighboring
  multi-cluster-networking-lab repository contains live experiment evidence.
- Existing AFD Gateway code is unrelated to the Service Gateway L4 data plane.

## Implementation and validation

Implemented:

- `pkg/providers/azure/servicegateway`: explicit validated configuration,
  ARM credential/pipeline boundary, Gateway UID ownership tags, conditional
  writes, NAT-before-attachment and detach-before-NAT-deletion, observed
  service/location convergence, application LB/PIP pruning.
- `pkg/controllers/hub/servicegateway`: separate class/controller identity,
  TCPRoute/ServiceImport resolution, reserved-member export checks, status,
  durable rollback annotation/finalizer, and opt-in automatic withdrawal.
- Member EndpointSlice controller: independent optional Service Gateway
  metadata, current Service/Pod UID and node/address validation, Node watches,
  and no changes to ordinary MCS readiness filtering.
- API/deepcopy/EndpointSlice export/import CRDs and generated RBAC updated.
- Member/hub manager flags and charts updated; disabled chart arguments
  remain unchanged. Leader election is required for network ownership.
- Setup and limitations documented in `docs/design/service-gateway-standard-aks.md`.

Validation passed:

- `make manifests generate` with the repository-configured controller-gen.
  Unrelated upstream documentation/import-alias churn was removed.
- Race-enabled affected suites: provider, hub Service Gateway, member
  EndpointSlice, hub EndpointSliceExport, member EndpointSliceImport, both
  manager commands, and v1alpha1 API. Envtest assets: Kubernetes 1.33.0.
- `go vet` for both new packages, member EndpointSlice and both managers.
- `go build` for both affected manager commands.
- Existing Helm suite and new Service Gateway-only rendering regression.
- Provider tests exercise invalid ownership/address inputs, failed Azure writes,
  network cutover and withdrawal ordering, empty backends, and orphan PIP cleanup.
- Member tests exercise provenance, readiness, terminating endpoints, and disabled
  metadata; hub tests exercise AFD isolation, deletion without class, and
  two-member aggregation with named target ports.

Environment notes:

- Installed Microsoft Go 1.26.5 could not satisfy go.mod 1.26.10; validation used
  the required toolchain via `MS_GOTOOLCHAIN_ALLOW_NON_LOCAL=1 GOTOOLCHAIN=auto`.
- Existing global Helm plugin was incompatible with the installed Helm version;
  chart tests used an empty temporary HELM_PLUGINS directory, then removed it.
- No dependencies were added, no commits created, and no live Azure resources
  were provisioned, modified, or deleted.

Unverified risks:

- Live Network API acceptance, permissions, data-plane delivery, and AKS subnet
  migration support remain unverified by this implementation.
- Platform administrators must authorize subnet enrollment and preserve original
  AKS egress resources. Observed ARM detach completion does not prove node
  connectivity recovered.
- Experimental TCPRoute CRDs must be installed explicitly. NSG rules remain
  platform-managed; this controller does not weaken security policy.
- Only same-VNet IPv4/TCP is implemented; this is not Gateway API conformance
  or a production multi-region migration feature.
