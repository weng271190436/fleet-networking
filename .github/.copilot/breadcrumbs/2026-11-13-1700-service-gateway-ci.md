# Service Gateway E2E CI

## Requirements and plan

The user requested adding Service Gateway to the existing E2E CI, following
the AFD matrix pattern rather than a separate unrelated workflow.

- [x] Add an isolated shared-VNet Service Gateway matrix entry, explicit
  preview/cutover approval, and CI service-principal RBAC support.
- [x] Reuse setup/tests, OIDC authentication/refresh, and scoped log artifacts.
- [x] Preserve failed enrolled environments. Delete the dedicated tagged
  resource group only after the full live proof and report pass.
- [x] Add offline workflow, identity and cleanup contract regressions.
- [x] Update instructions and run script/workflow validation; do not trigger CI.

Existing AFD and non-Service-Gateway paths must remain unchanged. The change
does not authorize a live CI run or mutate existing Azure lab resources.

## Implementation

- Existing E2E matrix gained one shared-VNet row with Service Gateway enabled,
  AFD and Traffic Manager disabled. It uses the same OIDC refresh and Make
  setup/test sequence, with a unique run/attempt-scoped resource-group name.
- The row selects the protected `service-gateway-e2e` GitHub environment.
  Explicit environment variable `SERVICE_GATEWAY_E2E_CUTOVER_APPROVED=true`
  is required before provisioning. Region/version are environment-configurable.
  Administrators must configure required reviewers and OIDC trust; code does
  not configure repository secrets, Azure enrollment or GitHub protection.
- Bootstrap supports explicit User or ServicePrincipal identities. CI discovers
  only the Object ID claim in the trusted CLI ARM token locally. It never logs
  tokens or calls Graph. Explicit Object IDs and legacy local user input remain
  supported.
- Logs include the Gateway manager and use the isolated kubeconfig. Distinct
  artifacts collect only scoped logs, report and non-secret setup inventory,
  not kubeconfig, SSH keys, tokens, or the complete local state directory.
- Failed or cancelled Service Gateway jobs retain resources for inspected
  recovery. On success, cleanup requires matching setup inventory, a passing
  labelled proof report, scoped network IDs, observed detached subnets, absent
  gateway/NAT/PIP resources, and matching dedicated resource-group ID/tag.
  Only then is deletion requested for the dedicated resource group.
- Existing ordinary/AFD cleanup paths remain unchanged.

## Validation and limitations

Passed:

- All `test/scripts` regression tests with race detection, including existing
  AFD tests, new matrix/approval contracts, Graph-free CI identity discovery,
  rejection of user tokens, success-only cleanup and linked/stale/foreign/
  failed/unrelated-report refusal cases.
- `go vet ./test/scripts`.
- Bash syntax for affected scripts and workflow run blocks.
- Workflow YAML checks: six matrix rows and exactly one isolated SG scenario.
- Documentation file links and `git diff --check`.

The incompatible global Helm plugin was isolated via a temporary empty
HELM_PLUGINS directory during offline tests, then removed.
No live Azure commands, GitHub workflow dispatch, repository/environment
configuration changes, commits or pushes were performed.
CI OIDC environment trust, service-principal Kubernetes authorization, regional
quota/capacity and successful cloud CI execution remain unverified.
