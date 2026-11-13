# Experimental Fleet Service Gateway for Standard AKS members

This feature is opt-in and disabled by default. It is separate from Fleet's
AFD/HTTP Gateway controller and does not claim Gateway API conformance.

Standard AKS members retain their ordinary cloud controller. Member
`ServiceExport` resources identify ClusterIP Services by namespace and name.
Fleet exports ready pod IPs and node IPv4 locations, then a hub TCPRoute
references the resulting ServiceImport. One Fleet-owned Service LB/VIP per
TCPRoute backend sends traffic directly to member pods.

## Safety and support boundaries

- Only one VNet, one Azure subscription, IPv4, and plain TCP are supported.
- Gateway listeners and TCPRoutes must be in the same namespace, with one
  rule/backend per route and one application per listener port.
- A Gateway is a subnet-wide network enrollment. Attaching its gateway changes
  effective egress in the tested topology. This must be authorized by the
  owners of every listed subnet; creating ServiceExport alone never enrolls it.
- Gateway configuration and its rollback journal are privileged platform
  inputs. Restrict write access to Gateways and the journal annotation to
  trusted platform administrators. Application owners control routes/exports,
  not subnet enrollment.
- No live migration or general AKS support is established by these offline
  tests. Empty-subnet attachment is the guide's documented ordering. Obtain
  preview-owner approval before attaching populated AKS subnets.
- Node and pod addresses must be within the enrolled subnet prefixes. Azure
  CNI overlay addresses, arbitrary external endpoints, and ambiguous locations
  are rejected.
- Network/PIP/NAT/LB creation requires the approved preview and regional
  capacity. Failure is reported; no fallback SKU is substituted.
- NSG rules are **not managed** by this feature. Provision approved
  client-to-pod TCP target-port rules in both subnet and NIC NSGs.
- Only resources tagged with the Gateway UID can be reconciled or deleted.
  Existing unowned resources are rejected, not adopted. Do not point this
  controller at the manually created lab resources.
- The controller records subnet rollback intent on the Gateway before Azure
  writes. Configuration is immutable during enrollment. Do not edit/remove
  its journal or finalizer.

## Enable the controllers

Install Fleet CRDs and the Gateway API **experimental channel** CRDs including
TCPRoute compatible with the pinned Gateway API dependency. They are not
installed automatically by this feature.

Member manager:

```text
--enable-service-gateway-export=true
```

Helm: `enableServiceGatewayExport: true`. This adds read-only Node access;
it does not configure AKS or mutate Azure networking. Ordinary EndpointSlice
exports are unchanged. Optional `spec.serviceGateway` metadata carries only
nonterminating ready pod backends. Missing/invalid node metadata is reported
and clears optional metadata; ordinary MCS endpoints still export.
The metadata is bound to the current Service UID; pod UID, node name, and
assigned pod addresses are checked against actual member objects.

Hub Gateway manager:

```text
--enable-service-gateway=true
--cloud-config=/etc/kubernetes/provider/azure.json
```

Helm: `serviceGateway.enabled: true`. Reuse the chart's existing
`afd.cloudConfigSecret.name/key` for the Azure credential configuration, even
when `afd.enabled` is false. `afd.resourceGroup` is only required for AFD;
Service Gateway resource IDs are explicitly configured on its Gateway.
Leader election is required for the Service Gateway runtime.
Constructing the disabled manager does not create Service Gateway Azure
clients. Enabling both paths does not change the AFD class/controller identity.

The hub identity requires Network Contributor (or narrower equivalent
resource permissions) for declared network/subnet and resource scopes.
Use existing managed/workload identity configuration; never put credentials
in annotations or Git.

## Gateway and route example

The resource IDs below are placeholders chosen by a platform administrator.
Gateway/NAT/public-IP resources must either be absent or already carry
matching Fleet ownership; the VNet/subnets must already exist. Listed subnets
must initially have no gateway association. The resource group, VNet, subnets,
and NSGs are never created or deleted by this controller.

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: fleet-service-gateway
spec:
  controllerName: networking.fleet.azure.com/service-gateway
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: regional-service-gateway
  namespace: apps
  annotations:
    networking.fleet.azure.com/service-gateway-config: |
      {
        "location": "eastus2euap",
        "gatewayID": "/subscriptions/SUB/resourceGroups/fleet-network/providers/Microsoft.Network/serviceGateways/regional",
        "vnetID": "/subscriptions/SUB/resourceGroups/member-network/providers/Microsoft.Network/virtualNetworks/shared",
        "subnetIDs": [
          "/subscriptions/SUB/resourceGroups/member-network/providers/Microsoft.Network/virtualNetworks/shared/subnets/member-a",
          "/subscriptions/SUB/resourceGroups/member-network/providers/Microsoft.Network/virtualNetworks/shared/subnets/member-b"
        ],
        "natGatewayID": "/subscriptions/SUB/resourceGroups/fleet-network/providers/Microsoft.Network/natGateways/regional",
        "egressPublicIPID": "/subscriptions/SUB/resourceGroups/fleet-network/providers/Microsoft.Network/publicIPAddresses/regional-egress",
        "approveSubnetEgressCutover": true
      }
spec:
  gatewayClassName: fleet-service-gateway
  listeners:
    - name: web
      protocol: TCP
      port: 80
---
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata:
  name: web
  namespace: apps
spec:
  parentRefs:
    - name: regional-service-gateway
      sectionName: web
  rules:
    - backendRefs:
        - group: networking.fleet.azure.com
          kind: ServiceImport
          name: web
          port: 80
```

Create `apps/web` as a ClusterIP Service and ServiceExport in each participating
member. Do not manually create the hub ServiceImport or forge its status.
The controller selects accepted exports only in each member's reserved hub
namespace, with current unconflicted status. Node-location metadata must be
enabled on the members. Named target ports are resolved from EndpointSlice
ports, not the Service's frontend port.

## Lifecycle

1. Validate configuration, accepted exports, addresses and ownership.
2. Persist rollback journal and finalizer on the Gateway.
3. Create owned Service Gateway, Standard v2 egress public IP and NAT.
4. Register default outbound service, then attach approved subnets.
5. Create owned application public IP/Service LB and register inbound service.
6. Reconcile all owned pod/location membership; prune withdrawn owned apps.

No-ready-backend applications retain enrollment and withdraw their pod
membership. Temporary Kubernetes/Azure read failures are reported and never
treated as application removal. Gateway and route identities use Kubernetes
UIDs, not names alone.

Removing the last route/backend ServiceImport withdraws application membership,
restores recorded subnet associations and waits for subnet provisioning to
succeed before deleting NAT/gateway and owned public IP/LB resources. Gateway
deletion runs the same ordered finalizer cleanup without depending on the
continued existence of its class. Foreign association/ownership changes block
cleanup rather than being overwritten.

**Automatic detach does not prove original AKS egress is functional.** The
platform must maintain the original cluster egress configuration throughout
enrollment, and verify restored node connectivity during rollout. If that
cannot be guaranteed, do not enable automatic lifecycle control in production.

## Validation limits

Offline tests cover ownership conflicts, invalid backends, Azure failures,
NAT-before-attachment, detach-before-NAT-deletion, readiness loss, and existing
AFD isolation. They do not prove live Azure permissions, provisioning,
packet delivery, subnet migration support, or runtime AKS egress recovery.
This implementation is experimental; use a dedicated approved test environment.

## Opt-in live controller E2E

The [Service Gateway proof](../../test/e2e/servicegateway_test.go) uses the
installed controllers, real member ClusterIP workloads and ServiceExports,
and Gateway/TCPRoute objects. It never calls provider provisioning methods
directly. It verifies owned Azure resources, the exact Service LB rule and
pod/node mappings, 100 successful requests reaching both members, readiness
withdrawal/restoration, and last-route teardown. Final assertions require
restored subnet links, removed owned resources, fresh node leases, and new
ready workloads after detach.

It is **not run by default**, and has not been executed against live Azure.
See the [E2E prerequisites and isolated invocation](../../test/README.md#service-gateway-tcp-live-proof).
Do not point it at the retained manual lab or existing unowned resources.
Failure retains Kubernetes fixtures and enrolled networking for diagnosis;
an administrator must recover/clean them explicitly.
