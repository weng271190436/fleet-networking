# Gateway API AFD Private Link POC Implementation Plan

## Plan status

- **Status:** Phase 3 complete; ready for Phase 4
- **Date:** 2026-10-07
- **Target repository:** `Azure/fleet-networking`
- **Development branch:** `poc/gateway-api-afd-private-link`
- **Base branch:** `pr/400`
- **Related design:** [GEP-1748 Gateway API for Fleet Global Ingress](gep-1748-gateway-api.md)
- **Related implementation plan:** [GEP-1748 Gateway API Implementation Plan](gep-1748-implementation-plan.md)

## Purpose

Build an end-to-end proof of concept for Gateway API global HTTP ingress with:

- Azure Front Door Premium;
- a pre-created Azure Front Door WAF policy;
- private member-cluster origins through Azure Private Link Service;
- hub-side member selection using Fleet `MemberCluster` labels;
- explicit hub-to-member origin assignments;
- member-side ILB and PLS discovery;
- member-side automated Private Link connection approval; and
- standard Gateway API status on `Gateway` and `HTTPRoute`.

The POC intentionally replaces `ServiceExport` and `ServiceImport` for this feature with a
selector-driven backend API. This is an experiment, not a migration of the existing multi-cluster
service APIs. Existing ServiceExport, ServiceImport, Traffic Manager, and multi-cluster Service
behavior must remain unchanged.

## Decisions

| Area | Decision |
|---|---|
| Global ingress API | Upstream `Gateway` and `HTTPRoute` |
| Logical backend API | New hub-side `MultiClusterBackend` v1alpha1 CRD |
| Member selection | Label selector evaluated against Fleet `MemberCluster` objects |
| Member assignment | New hub-side `ServiceOriginAssignment` v1alpha1 CRD in each member's reserved hub namespace |
| Assignment transport | Member networking controller watches its scoped hub namespace through its existing hub client |
| Origin discovery | Member networking controller reads the local Service, ILB, and PLS |
| Origin publication | Member writes discovered facts to `ServiceOriginAssignment.status` |
| AFD ownership | Hub Gateway controller owns the AFD profile and all child resources |
| AFD SKU | Premium only |
| Origin connectivity | Private Link only |
| PLS approval | Automated by the member networking controller |
| WAF | Required; policy is pre-created and only attached by the POC |
| Member weights | Equal weights in the first POC |
| Incomplete members | Exclude incomplete members while programming all ready members |
| Public origins | Out of scope |
| Custom domains and certificates | Out of scope; use the default AFD endpoint |
| Unit tests | Minimal happy-path coverage |
| Primary validation | Real Azure end-to-end test |
| Eligible members | `cluster.kubernetes-fleet.io/v1beta1` `MemberCluster` with `Joined=True` |
| Selected-member limit | 40 per backend |
| AFD ownership boundary | One controller-owned Premium profile per Gateway |
| AFD SDK and API | `armcdn/v3` v3.0.0, API `2025-06-01` |
| WAF lookup SDK and API | `armfrontdoor/v2` v2.0.0, API `2025-10-01` |
| PLS approval SDK and API | Existing `armnetwork/v4` v4.3.0, API `2023-05-01` |

## Departure from the GEP-1748 proposal

The existing proposal resolves an `HTTPRoute` backend through Fleet `ServiceImport` and derives its
member set from `ServiceExport` contributors. This POC instead makes the hub authoritative for
membership:

```text
HTTPRoute
    |
    v
MultiClusterBackend
    |
    | clusterSelector
    v
Fleet MemberClusters
    |
    | one ServiceOriginAssignment per selected member
    v
Member networking controllers
    |
    | discovered ILB and PLS status
    v
Hub Gateway controller
    |
    v
AFD Premium + WAF + Private Link origins
```

This is a meaningful semantic change rather than a rename:

- the hub selects participating clusters;
- members do not opt in through `ServiceExport`;
- no `ServiceImport` aggregate is created;
- each selected member receives explicit, narrowly scoped discovery work; and
- application Service names and namespaces must be consistent across selected clusters.

The POC must not claim upstream GEP-1748 conformance because its backend kind is an
implementation-specific `MultiClusterBackend`.

## POC topology

```text
                                      Fleet hub
                           +-------------------------------+
                           | Gateway                       |
                           | HTTPRoute                     |
                           | MultiClusterBackend           |
                           | ServiceOriginAssignments      |
                           | Hub Gateway controller        |
                           +---------------+---------------+
                                           |
                                           v
                           Azure Front Door Premium
                               + pre-created WAF
                                  /             \
                                 / Private Link  \ Private Link
                                v                 v
              Member A                                     Member B
    +---------------------------+               +---------------------------+
    | Service                   |               | Service                   |
    | Internal LoadBalancer     |               | Internal LoadBalancer     |
    | Private Link Service      |               | Private Link Service      |
    | Member networking agent   |               | Member networking agent   |
    +---------------------------+               +---------------------------+
```

The minimum E2E environment contains one hub, two AKS members, one logical backend, one Gateway,
one HTTPRoute, one AFD Premium profile, one pre-created WAF policy, and two private origins.

## API design

### MultiClusterBackend

`MultiClusterBackend` is a namespaced, user-facing backend object on the hub. It is referenced by
`HTTPRoute.backendRefs`.

Frozen Phase 0 shape:

```yaml
apiVersion: networking.fleet.azure.com/v1alpha1
kind: MultiClusterBackend
metadata:
  name: echo
  namespace: afd-private-demo
spec:
  service:
    name: echo
    port: 80
  clusterSelector:
    matchLabels:
      environment: poc
    matchExpressions:
    - key: region
      operator: In
      values:
      - eastus
      - westus2
  healthProbe:
    path: /healthz
status:
  observedGeneration: 1
  selectedClusters: 2
  readyOrigins: 2
  conditions:
  - type: Accepted
    status: "True"
    reason: Accepted
  - type: ResolvedRefs
    status: "True"
    reason: ReadyOriginsAvailable
  - type: Programmed
    status: "True"
    reason: Programmed
  members:
  - clusterName: member-a
    conditions:
    - type: Ready
      status: "True"
  - clusterName: member-b
    conditions:
    - type: Ready
      status: "True"
```

Field contract:

| Field | Type | Required | Validation and ownership |
|---|---|---|---|
| `spec.service.name` | string | Yes | DNS-1123 Service name; user-owned |
| `spec.service.port` | int32 | Yes | 1-65535; user-owned and authoritative for the backend port |
| `spec.clusterSelector` | `metav1.LabelSelector` | Yes | Must contain at least one label or expression; user-owned |
| `spec.healthProbe.path` | string | No | Defaults to `/`; absolute path with maximum length 1024 |
| `status.observedGeneration` | int64 | No | Hub backend-selection controller-owned |
| `status.selectedClusters` | int32 | No | 0-40; hub backend-selection controller-owned |
| `status.readyOrigins` | int32 | No | 0-40; hub backend-selection controller-owned |
| `status.conditions` | `[]metav1.Condition` | No | Map keyed by condition type; hub-owned |
| `status.members` | member status list | No | Maximum 40, map keyed by cluster name; hub-owned |

Initial rules:

- the referenced Service has the same namespace as the `MultiClusterBackend`;
- `spec.service.name` and `spec.service.port` are required;
- the selector uses standard `metav1.LabelSelector` semantics;
- an empty selector is rejected to prevent accidental fleet-wide selection;
- the controller evaluates `metadata.labels` from authoritative
  `cluster.kubernetes-fleet.io/v1beta1` `MemberCluster` objects;
- only members with the `Joined` condition set to `True` are eligible;
- the currently unused `Healthy` condition is not an eligibility gate;
- the selected member count must be between 1 and the configured POC limit;
- the initial selected-member limit is 40 to leave headroom below the AFD origin-group limit;
- selecting more than the limit fails explicitly and does not partially select members;
- every member has equal origin weight;
- Private Link connectivity is implicit and mandatory;
- the health probe uses HTTP and defaults to `/`; and
- cross-namespace Service references are not supported.

Example route:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: echo
  namespace: afd-private-demo
spec:
  parentRefs:
  - name: global
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /
    backendRefs:
    - group: networking.fleet.azure.com
      kind: MultiClusterBackend
      name: echo
```

The port is defined only by `MultiClusterBackend.spec.service.port`. The POC should reject a
non-empty `backendRef.port` to avoid two authoritative port values.

### ServiceOriginAssignment

`ServiceOriginAssignment` is an internal, namespaced transport resource stored on the hub in the
selected member's reserved namespace:

```text
fleet-member-<member-name>
```

The hub owns `spec`. The selected member networking controller owns `status`. No controller writes
both sides of this ownership boundary.

Frozen Phase 0 shape:

```yaml
apiVersion: networking.fleet.azure.com/v1alpha1
kind: ServiceOriginAssignment
metadata:
  name: echo-2c66d893
  namespace: fleet-member-member-a
spec:
  backendRef:
    namespace: afd-private-demo
    name: echo
    uid: 2c66d893-0000-0000-0000-000000000000
  serviceRef:
    namespace: afd-private-demo
    name: echo
    port: 80
  connectivity:
    type: PrivateLink
  approval:
    requestToken: 8384086c-0000-0000-0000-000000000000
status:
  observedGeneration: 1
  origin:
    azureLocation: eastus
    loadBalancerAddress: 10.0.1.4
    privateLinkServiceID: /subscriptions/.../privateLinkServices/echo
  conditions:
  - type: ServiceResolved
    status: "True"
    reason: ServiceResolved
  - type: InfrastructureReady
    status: "True"
    reason: InfrastructureReady
  - type: PrivateLinkApproved
    status: "True"
    reason: ConnectionApproved
```

Field contract:

| Field | Type | Required | Validation and ownership |
|---|---|---|---|
| `spec.backendRef.namespace` | string | Yes | DNS-1123 namespace; hub-owned |
| `spec.backendRef.name` | string | Yes | DNS-1123 backend name; hub-owned |
| `spec.backendRef.uid` | `types.UID` | Yes | Non-empty immutable backend identity; hub-owned |
| `spec.serviceRef.namespace` | string | Yes | Member-local Service namespace; hub-owned |
| `spec.serviceRef.name` | string | Yes | Member-local DNS-1123 Service name; hub-owned |
| `spec.serviceRef.port` | int32 | Yes | 1-65535; hub-owned |
| `spec.connectivity.type` | string enum | Yes | The only POC value is `PrivateLink`; hub-owned |
| `spec.approval.requestToken` | string | Yes | 32-64 URL-safe characters, unique per assignment UID; hub-owned |
| `status.observedGeneration` | int64 | No | Selected member controller-owned |
| `status.origin.azureLocation` | string | No | Discovered Azure location; member-owned |
| `status.origin.loadBalancerAddress` | string | No | Discovered ILB address; member-owned |
| `status.origin.privateLinkServiceID` | string | No | Full discovered PLS resource ID; member-owned |
| `status.conditions` | `[]metav1.Condition` | No | Map keyed by condition type; member-owned |

Initial rules:

- the assignment name is deterministic from backend UID and member identity;
- `backendRef.uid` prevents adoption after delete and recreate with the same name;
- the hub creates an assignment only in the namespace reserved for the selected member;
- the member hub identity can read assignments and update status only in its own namespace;
- the member derives its cluster identity from trusted configuration, not from assignment labels;
- origin facts are accepted only from the member to which the namespace belongs;
- requester subscription allowlists are trusted member-controller configuration and are never
  supplied through the hub-owned assignment;
- the member does not create, update, or delete AFD resources;
- the hub does not update assignment status; and
- assignment status contains no credentials or secrets.

The approval request token is a correlation value, not a credential. It must be unpredictable and
unique per assignment UID. The AFD shared Private Link request message is exactly
`fleet:<assignment-uid>:<request-token>` so the member can compare the complete message rather than
performing a substring or prefix match.

### Gateway configuration

Reuse the PR's Gateway annotations:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: global
  namespace: afd-private-demo
  annotations:
    networking.fleet.azure.com/afd-sku: Premium_AzureFrontDoor
    networking.fleet.azure.com/afd-waf-policy-id: >-
      /subscriptions/.../resourceGroups/.../providers/Microsoft.Network/frontdoorWebApplicationFirewallPolicies/poc-waf
spec:
  gatewayClassName: azure-fleet-afd
  listeners:
  - name: http
    protocol: HTTP
    port: 80
```

POC validation requires:

- `Premium_AzureFrontDoor`;
- a non-empty, syntactically valid WAF policy resource ID;
- successful read access to the WAF policy;
- no public or automatic connectivity mode; and
- no resource adoption.

The controller attaches the existing WAF policy but never creates, modifies, or deletes the policy.

### Frozen Azure API versions

The POC uses SDK-generated clients and their embedded stable REST API versions:

| Resource operations | Go SDK module | SDK version | REST API version |
|---|---|---|---|
| AFD profiles, endpoints, origin groups, origins, routes, and security policies | `github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cdn/armcdn/v3` | `v3.0.0` | `2025-06-01` |
| Read-only validation of `Microsoft.Network/frontdoorWebApplicationFirewallPolicies` | `github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/frontdoor/armfrontdoor/v2` | `v2.0.0` | `2025-10-01` |
| PLS private endpoint connection list, get, and approval | Existing `github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4` | `v4.3.0` | `2023-05-01` |

The WAF client is separate from `armcdn` because the required pre-created policy uses the
`Microsoft.Network/frontdoorWebApplicationFirewallPolicies` resource type. The WAF client is
exposed behind a read-only interface; create, update, and delete operations are not part of the
provider contract.

## Controller responsibilities

### Hub backend-selection controller

Watch:

- `MultiClusterBackend`;
- Fleet `MemberCluster`; and
- `ServiceOriginAssignment`.

Responsibilities:

1. Validate the backend and label selector.
2. List joined and eligible `MemberCluster` objects.
3. Evaluate the selector deterministically.
4. Reject an empty or over-limit selected set.
5. Create or update one assignment in each selected member namespace.
6. Withdraw assignments for members that are no longer selected.
7. Summarize assignment status into `MultiClusterBackend.status.members`.
8. Enqueue attached routes when ready origins change.

For the POC, a `MemberCluster` label change may enqueue all `MultiClusterBackend` objects. Add
indexes or selector-aware fan-out only if measurements show this is necessary.

### Member origin controller

Run in the existing member networking controller manager and use:

- the member client for local `Service` inspection; and
- the existing scoped hub client for `ServiceOriginAssignment`.

Responsibilities:

1. Watch assignments in the member's reserved hub namespace.
2. Resolve the local Service.
3. Validate the requested port and `LoadBalancer` type.
4. require an internal Azure load balancer;
5. discover the ILB address;
6. discover the associated PLS resource ID and Azure location;
7. publish discovery results to assignment status;
8. poll pending PLS private endpoint connections while assigned;
9. validate pending requests against the active assignment and trust policy;
10. approve only the matching AFD Private Link connection; and
11. update `PrivateLinkApproved` after Azure reports approval.

The POC assumes the Service, ILB, and PLS already exist. The member controller discovers and
approves them but does not create or delete them.

### Hub Gateway and route controllers

Responsibilities:

1. Accept only the `azure-fleet-afd` GatewayClass.
2. Accept only supported HTTP listeners and route features.
3. Resolve `MultiClusterBackend` references.
4. Include only assignments with `InfrastructureReady=True`.
5. Build the provider-neutral normalized model.
6. Reconcile the AFD profile and child resources.
7. create Private Link origins with the assignment request token;
8. attach the pre-created WAF policy;
9. observe Azure provisioning rather than treating API acceptance as completion; and
10. publish Gateway API status after observing desired Azure state.

### Azure provider

Use Azure SDK for Go clients behind narrow interfaces. The provider must not read Kubernetes
objects directly.

Initial mapping:

| Kubernetes concept | Azure Front Door resource |
|---|---|
| `Gateway` | Premium profile and AFD endpoint |
| Gateway WAF annotation | Security policy association to the pre-created WAF policy |
| `HTTPRoute` | Route |
| `MultiClusterBackend` | Origin group |
| `ServiceOriginAssignment` | Origin with shared Private Link configuration |
| Backend health probe | Origin-group health probe |

Every owned Azure resource must include ownership tags for:

- hub identity;
- Gateway namespace and name;
- Gateway UID; and
- controller identifier.

The provider must reject an existing resource that does not have matching ownership tags.

## Private Link approval protocol

The member controller must never approve all pending connections on a PLS.

Approval sequence:

1. The hub creates an assignment containing a unique request token.
2. The member discovers and publishes its PLS resource ID.
3. The hub creates the AFD origin with the exact
   `fleet:<assignment-uid>:<request-token>` shared Private Link request message.
4. Azure creates a pending private endpoint connection on the member's PLS.
5. The member finds the pending connection.
6. The member verifies:
   - the assignment still exists;
   - the assignment UID and generation are current;
   - the PLS matches the assignment's discovered PLS;
   - the connection state is `Pending`;
   - the complete request message exactly matches the value derived from the assignment;
   - if a member-local subscription allowlist is configured, the subscription parsed from the
     managed private endpoint resource ID is allowlisted; and
   - the local Service and PLS remain valid.
7. The member approves the connection.
8. The member reports `PrivateLinkApproved=True`.
9. The hub observes both approval and successful AFD provisioning.

If any check fails, the member leaves the connection pending and reports an actionable condition.
It must not silently approve or reject an unrelated connection.

The Azure PLS private endpoint connection response exposes the managed private endpoint resource ID
but does not expose a tenant ID that the member can authenticate directly. Phase 0 therefore does
not claim tenant verification. The optional requester subscription allowlist is member-local
trusted configuration, not assignment spec written by the hub. The request token provides
correlation and replay resistance within an assignment lifecycle; it is not a credential or an
independent requester identity.

## Readiness and failure policy

### MultiClusterBackend

- `Accepted=True`: the spec, selector, service reference, and selected-member count are valid.
- `ResolvedRefs=True`: at least one selected member has a ready ILB and PLS origin.
- `Programmed=True`: the origin group and all currently included ready origins match observed Azure
  state.

A selected member that is not ready is excluded and reported in `status.members`. Other ready
members continue to be programmed.

### Gateway and HTTPRoute

- `Accepted=True`: the class, listener, route, backend kind, SKU, and WAF reference are supported.
- `ResolvedRefs=True`: referenced backends exist and each accepted backend has at least one ready
  origin.
- `Programmed=True`: the AFD profile, endpoint, routes, origin groups, ready origins, Private Link
  approvals, and WAF security-policy association match the current desired generation.

AFD health-probe failure does not by itself set `Programmed=False`; `Programmed` describes
configuration state. Backend health must be reported separately.

### Incomplete members

- missing Service: exclude and report `ServiceNotFound`;
- invalid port: exclude and report `PortNotFound`;
- ILB pending: exclude and report `LoadBalancerNotReady`;
- PLS missing or pending: exclude and report `PrivateLinkServiceNotReady`;
- approval pending: keep the desired origin pending but do not count it as programmed;
- transient application health failure after programming: leave the origin configured and rely on
  AFD health probes; and
- no ready members: do not program an empty backend and set `ResolvedRefs=False`.

## Deletion and selector-change behavior

### Member begins matching

1. Create the assignment.
2. Wait for member discovery.
3. Create the AFD origin.
4. Wait for member approval.
5. Include the origin in programmed status.

Existing ready origins remain active throughout this process.

### Member stops matching

1. Mark the member as withdrawing in backend status.
2. Remove the AFD origin.
3. Wait for Azure to confirm deletion.
4. Delete the assignment.
5. Stop reporting the member in backend status.

The Service, ILB, PLS, and pre-created WAF policy remain untouched.

### Gateway deletion

1. Stop accepting new backend changes.
2. Delete owned routes, origins, origin groups, security-policy associations, endpoint, and profile.
3. Wait for Azure deletion.
4. Delete remaining assignments owned by the Gateway's backends.
5. Remove finalizers.

Externally owned WAF policies, member Services, ILBs, and PLS resources must never be deleted.

## Security and RBAC

### Hub identity

Grant the hub Gateway controller only the permissions required to:

- read the pre-created WAF policy;
- create, read, update, and delete the owned AFD Premium resource graph;
- request Private Link connections to selected PLS resources; and
- read provisioning state.

Scope Azure permissions to POC resource groups whenever possible. Do not grant subscription-wide
Contributor.

### Member identity

Grant each member controller:

- read access to its local Services;
- read access to its local load balancer and PLS;
- permission to approve private endpoint connections only on member-owned PLS resources;
- read access to assignments in its reserved hub namespace; and
- status-update permission for assignments in its reserved hub namespace.

A member must not create assignments, modify assignment spec, modify another member namespace, or
write Gateway and backend status.

### Kubernetes RBAC

Generate and review RBAC for:

- `multiclusterbackends` and status;
- `serviceoriginassignments` and status;
- Gateway API resources and status;
- Fleet `MemberCluster` read access; and
- member-local Service read access.

## Phase 0 frozen contract and stability

Phase 0 freezes the following implementation inputs:

- `MultiClusterBackend` and `ServiceOriginAssignment` use
  `networking.fleet.azure.com/v1alpha1`.
- `MultiClusterBackend` is a user-facing experimental POC API. It is not a production compatibility
  promise. Any post-POC promotion requires a separate API review and conversion/storage strategy.
- `ServiceOriginAssignment` is an internal experimental transport API. Users must not create or
  depend on it, and it may change or be removed without migration if the POC does not proceed.
- Upstream `Gateway` and `HTTPRoute` remain standard APIs, but only the feature subset documented
  by this POC is supported by the `azure-fleet-afd` GatewayClass.
- Existing AFD annotation meanings remain implementation API as documented by the Gateway design;
  this POC does not redefine them.
- Existing `ServiceExport`, `ServiceImport`, Traffic Manager, and multi-cluster Service APIs and
  behavior are unchanged.
- Spec/status ownership is exclusive: users own `MultiClusterBackend.spec`, the hub backend
  controller owns `MultiClusterBackend.status` and `ServiceOriginAssignment.spec`, and the selected
  member controller owns `ServiceOriginAssignment.status`.

The example `MultiClusterBackend`, `ServiceOriginAssignment`, `Gateway`, and `HTTPRoute` manifests
have been reviewed against the frozen field contract. They contain all required fields, use valid
enum and selector values, leave `HTTPRoute.backendRef.port` unset, and do not place member-local
trust policy in hub-owned assignment spec.

## Implementation phases

### Phase 0: Freeze the POC contract

- [x] Review and approve the two CRD shapes.
- [x] Confirm the authoritative Fleet `MemberCluster` GVK and label source.
- [x] Confirm the selected-member hard limit.
- [x] Confirm the AFD profile-per-Gateway ownership boundary.
- [x] Confirm the WAF policy and Private Link Azure API versions.
- [x] Record the enforceable requester subscription and tenant constraints used for approval.

**Exit criteria**

- Example manifests pass schema review.
- There are no fields with multiple controller owners.
- Public and internal API stability expectations are documented.

### Phase 1: Add APIs and generated artifacts

- [x] Add `MultiClusterBackend` v1alpha1 types.
- [x] Add `ServiceOriginAssignment` v1alpha1 types.
- [x] Add condition and reason constants.
- [x] Register both APIs with the repository scheme.
- [x] Generate deepcopy code, CRDs, and RBAC.
- [x] Add chart installation for both CRDs.
- [x] Add example POC manifests.

**Exit criteria**

- Generated artifacts are clean and reproducible.
- Both resources can be created in envtest.
- Status subresources enforce the intended ownership split.

### Phase 2: Implement hub member selection

- [x] Add a `MultiClusterBackend` reconciler.
- [x] Evaluate `metav1.LabelSelector` against joined MemberClusters.
- [x] Sort selected members deterministically.
- [x] Reject empty and over-limit selections.
- [x] Create assignments in reserved member hub namespaces.
- [x] Delete assignments for deselected members only after origin withdrawal.
- [x] Aggregate assignment conditions into backend status.

**Exit criteria**

- Changing a MemberCluster label creates or withdraws the expected assignment.
- No assignment is written outside the selected member's reserved namespace.
- Reconciliation is idempotent.

### Phase 3: Implement member discovery

- [x] Add the assignment controller to the member networking manager.
- [x] Reuse the existing scoped hub client and member client.
- [x] Resolve the local Service and requested port.
- [x] Discover ILB address, PLS resource ID, and Azure location.
- [x] Publish `ServiceResolved` and `InfrastructureReady`.
- [x] Report actionable failure conditions.

**Exit criteria**

- A valid ILB/PLS Service produces ready origin facts.
- Missing or pending infrastructure remains excluded without blocking ready members.
- The member writes only assignment status.

### Phase 4: Implement the AFD Premium provider

- [ ] Define narrow Azure SDK interfaces.
- [ ] Update the normalized model for `MultiClusterBackend`.
- [ ] Reconcile profile, endpoint, origin groups, origins, and routes.
- [ ] Require Private Link on every origin.
- [ ] Attach the pre-created WAF policy through a security policy.
- [ ] Add deterministic names and ownership tags.
- [ ] Observe Azure provisioning state.
- [ ] Implement idempotent update and deletion.

**Exit criteria**

- One Gateway and backend produce the expected AFD resource graph.
- Reconciliation does not recreate unchanged resources.
- Unowned existing resources are rejected.
- WAF policy deletion is never attempted.

### Phase 5: Automate PLS approval

- [ ] Include the assignment token in the AFD Private Link request message.
- [ ] Add a member Azure client for PLS private endpoint connections.
- [ ] Find the matching pending connection.
- [ ] Enforce token, PLS, assignment, connection-state, and configured subscription checks.
- [ ] Approve only the matching connection.
- [ ] Publish `PrivateLinkApproved`.

**Exit criteria**

- The intended AFD connection is approved without manual intervention.
- An unrelated pending connection remains untouched.
- Deleting an assignment prevents future approval.

### Phase 6: Complete status and lifecycle

- [ ] Publish `MultiClusterBackend` conditions and member summaries.
- [ ] Publish Gateway API parent and Gateway conditions.
- [ ] Track observed generations.
- [ ] Implement selector-change withdrawal ordering.
- [ ] Implement Gateway and backend finalizers.
- [ ] Preserve fail-static behavior during controller outages.

**Exit criteria**

- `Programmed=True` is reported only after WAF, AFD, and Private Link are observed ready.
- Deletion removes only hub-owned AFD resources.
- Existing AFD traffic continues while the hub controller is stopped.

### Phase 7: Build the real Azure E2E test

- [ ] Provision one hub and two AKS members.
- [ ] Label both MemberClusters for selection.
- [ ] Deploy the same test Service to both members.
- [ ] Provision internal load balancers and PLS resources.
- [ ] Pre-create a WAF policy with a deterministic custom block rule.
- [ ] Deploy the hub Gateway and member controller changes.
- [ ] Create Gateway, MultiClusterBackend, and HTTPRoute.
- [ ] Wait for assignments, discovery, approval, and programmed status.
- [ ] Send normal traffic and observe responses from both members.
- [ ] Send a WAF-blocked request and verify HTTP 403.
- [ ] Remove one member's selection label and verify its origin is removed.
- [ ] Verify traffic continues through the remaining member.
- [ ] Stop the hub controller and verify programmed traffic continues.
- [ ] Delete the Gateway and verify owned AFD resources are deleted.
- [ ] Verify the WAF policy, Services, ILBs, and PLS resources remain.

**Exit criteria**

- Every required data-plane and lifecycle assertion passes from a clean environment.
- Test output records Azure resource IDs, Kubernetes conditions, and HTTP results.
- Cleanup is idempotent and leaves no controller-owned Azure resources.

## Minimal unit-test plan

Keep unit coverage deliberately narrow for the POC:

1. `MultiClusterBackend` plus two matching MemberClusters produces two deterministic assignments.
2. Assignment status for two ready members produces a normalized backend with two private origins.
3. Stable input produces stable Azure resource names and normalized ordering.
4. Gateway cannot become programmed until AFD, WAF association, and Private Link approval are
   observed ready.
5. The member approval matcher accepts the expected token and rejects a mismatched token.

Use the real Azure E2E test as the primary validation, but keep Azure SDK calls behind interfaces so
the controller's happy path can be tested without Azure.

## E2E WAF assertion

Configure the pre-created WAF policy with a deterministic custom rule, for example:

```text
If request header X-POC-Block equals true, block the request.
```

The E2E test must prove both paths:

```text
GET /                         -> application response
GET / with X-POC-Block:true   -> WAF rejection, expected HTTP 403
```

Checking only that a security policy association exists is insufficient.

## POC success criteria

The POC is complete when:

1. An HTTPRoute can reference `MultiClusterBackend`.
2. MemberCluster labels determine the desired origin set.
3. Each selected member receives an explicit assignment.
4. Members publish ready ILB and PLS facts without AFD permissions.
5. Members securely approve only their assigned Private Link connection.
6. AFD Premium routes traffic privately to two member clusters.
7. The pre-created WAF policy demonstrably blocks the test request.
8. Incomplete members are excluded while ready members continue serving.
9. Removing a member label removes only that member's AFD origin.
10. A hub controller outage does not interrupt already programmed traffic.
11. Gateway deletion removes controller-owned AFD resources but preserves external WAF, ILB, PLS,
    and Service resources.

## Post-POC decision gate

Do not treat the new APIs as production contracts until the POC answers:

- Is selector-driven membership operationally preferable to ServiceExport opt-in?
- Is central hub authority sufficient, or is member-local consent required?
- Is assignment status an adequate transport for origin facts at production scale?
- Can PLS approval be authenticated strongly enough without broad Azure permissions?
- Are AFD origin limits compatible with expected selector sizes?
- Does one AFD profile per Gateway provide acceptable isolation, cost, and quota behavior?
- Should the implementation retain `MultiClusterBackend`, return to `ServiceImport`, or support
  both through separate GatewayClasses?

If the selector model proceeds, update the main Gateway design and implementation plan in a
separate review. If it does not proceed, remove the experimental CRDs without changing the existing
ServiceExport and ServiceImport contracts.
