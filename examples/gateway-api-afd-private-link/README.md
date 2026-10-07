# Gateway API AFD Private Link POC examples

These manifests illustrate the experimental Gateway API AFD Private Link POC contract.

Install the generated hub CRDs before creating the user-facing resources:

```bash
helm upgrade --install hub-net-controller-manager ./charts/hub-net-controller-manager \
  --set crdInstaller.enabled=true
```

Apply `hub-resources.yaml` only after:

- the `azure-fleet-afd` GatewayClass exists;
- the two target Fleet MemberClusters have the example labels;
- the `echo` Service, internal load balancer, and Private Link Service exist in the
  `afd-private-demo` namespace of each selected member; and
- the referenced Front Door WAF policy exists.

Replace the WAF policy resource ID before applying:

```bash
kubectl apply -f examples/gateway-api-afd-private-link/hub-resources.yaml
```

`internal-serviceoriginassignment.yaml` documents the hub-to-member transport shape. Do not apply
it directly. The hub backend-selection controller creates assignments in each selected member's
reserved hub namespace.
