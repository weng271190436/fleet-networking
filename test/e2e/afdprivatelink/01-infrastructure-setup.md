# Part 1: reusable platform infrastructure

[Previous: cleanup (for retained runs)](04-cleanup.md) ·
[Next: global Service scenario](02-global-service-scenario.md)

This guide creates the reusable platform required by the customer quickstart:

- one hub and two member AKS clusters;
- run-scoped ACR images pinned by digest;
- Fleet and Gateway API CRDs;
- Fleet registration and real networking-agent joins;
- hub and member networking controllers;
- Azure identities and bounded role assignments; and
- the reusable `azure-fleet-afd` `GatewayClass`.

It intentionally does **not** create application workloads, Services, Private Link Services, WAF,
Gateway, HTTPRoute, MultiClusterBackend, or Azure Front Door resources. Those belong to
[Part 2](02-global-service-scenario.md).

> **Status: Validated boundary.** Clean run `p7-10092311` completed the same Part 1 resource and
> controller boundary on 2026-10-09 and was subsequently cleaned up. The concise wrapper targets
> are statically validated and should be exercised during the next approved live run.

For literal Azure commands, ownership checks, expected resource shapes, and recovery procedures,
see the [detailed operator reference](reference/01-infrastructure-setup-details.md).

## Prerequisites

Run from the repository root with:

- Azure CLI authenticated to `AKS Fleet Development/Test`;
- Docker Engine and buildx;
- `kubectl`, Helm, `jq`, `curl`, Go, Git, and Make; and
- permission to create resource groups, AKS, ACR, managed identities, role assignments, and
  networking resources in the fixed test subscription.

The workflow creates billable Azure resources. Cleanup is explicit and never automatic.

## 1. Choose one run ID

Change only the run ID. Use 3–15 lowercase alphanumeric or hyphen characters:

```bash
cd /home/weiweng/fleet-networking

export AFD_PLS_E2E_RUN_ID="replace-with-your-run-id"
export AZURE_SUBSCRIPTION_ID=d712bfad-d238-486f-8f1b-bf61a831b712
export AFD_PLS_E2E_LOCATION=eastus2
export AFD_PLS_E2E_APPROVED=true
```

Keep the same run ID through setup, Part 2, lifecycle validation, and cleanup.

## 2. Run the read-only preflight

```bash
make phase7-e2e-preflight
```

Review the subscription, quota, deterministic resource names, billable plan, and cleanup boundary.
Preflight does not create Azure or Kubernetes resources.

## 3. Create the platform

Run each target in order. Every target is resumable and validates existing resources before reuse.
If a target fails, stop, read its error and diagnostic path, fix the reported issue, and rerun only
that target.

```bash
make phase7-e2e-step-01-registry-images
make phase7-e2e-step-02-network
make phase7-e2e-step-03-aks
make phase7-e2e-step-04-identities-rbac-kubeconfig
make phase7-e2e-step-05-crds-registration
make phase7-e2e-step-06-platform-controllers
make phase7-e2e-step-07-platform-join
```

The stages perform:

| Stage | Result |
|---|---|
| 01 | Tagged primary resource group, Basic ACR, four published images, five immutable references |
| 02 | One VNet, three AKS subnets, and two PLS NAT subnets |
| 03 | Three one-node workload-identity-enabled AKS clusters created concurrently |
| 04 | Hub identity/federation, seven bounded assignments, and three kubeconfig contexts |
| 05 | Pinned Fleet/Gateway CRDs, registration RBAC, and two MemberClusters |
| 06 | Digest-pinned hub/member networking controllers only |
| 07 | Two real networking-agent joins and the reusable GatewayClass |

No stage in this sequence creates a WAF policy, application, PLS, Gateway, or AFD profile.

## 4. Verify the Part 1 handoff

```bash
make phase7-e2e-verify-platform
```

Expected final output:

```text
Part 1 platform verification passed for <run-id>.
```

The verifier checks:

- all three clusters have a Ready node;
- all three controllers are Available and use digest-pinned images;
- both networking agents have current joined heartbeats;
- the networking CRDs and accepted GatewayClass exist;
- exactly seven run-recorded Azure assignments exist; and
- application, PLS, WAF, Gateway, backend, route, assignment, and AFD resources are absent.

## 5. Continue to the customer quickstart

Part 2 needs only the same run ID:

```bash
export AFD_PLS_E2E_RUN_ID="replace-with-your-run-id"
```

Continue with [Part 2: global Service customer quickstart](02-global-service-scenario.md).

## Resume and troubleshooting

- Rerun only the failed target; stages validate and reuse matching run-owned resources.
- A name collision with missing or mismatched ownership tags is a hard stop.
- Setup failures preserve resources and write a run-scoped diagnostic log without reading Secrets.
- Never use the legacy `phase7-e2e-setup`, `phase7-e2e-step-06-waf-manifests`, or
  `phase7-e2e-step-07-deploy-join` targets for this four-part workflow; they cross the Part 1/Part 2
  boundary.
- Use the [detailed operator reference](reference/01-infrastructure-setup-details.md) when a stage
  needs manual inspection or recovery.

## Cleanup

Billable resources remain until explicitly removed. Follow [Part 4: bounded cleanup](04-cleanup.md)
or run:

```bash
AZURE_SUBSCRIPTION_ID=d712bfad-d238-486f-8f1b-bf61a831b712 \
AFD_PLS_E2E_RUN_ID="replace-with-your-run-id" \
AFD_PLS_E2E_APPROVED=true \
make phase7-e2e-cleanup
```

[Previous: cleanup (for retained runs)](04-cleanup.md) ·
[Next: global Service scenario](02-global-service-scenario.md)
