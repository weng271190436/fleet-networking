# Part 4: bounded cleanup

[Previous: lifecycle validation](03-lifecycle-validation.md) ·
[Next: infrastructure setup (new run)](01-infrastructure-setup.md)

> **Status: Pending.** Retained run `p7-10082105` still has its test infrastructure. Do not infer
> Phase 7 completion until bounded cleanup and deletion verification pass.

In a new shell, export the exact approved run values and initialize deterministic names before any
cleanup or recovery command:

```bash
cd /home/weiweng/fleet-networking
export AZURE_SUBSCRIPTION_ID=d712bfad-d238-486f-8f1b-bf61a831b712
export AFD_PLS_E2E_RUN_ID="replace-with-the-approved-run-id"
export AFD_PLS_E2E_LOCATION=eastus2
export AFD_PLS_E2E_APPROVED=true
source test/e2e/afdprivatelink/scripts/common.sh
initialize_names
validate_subscription
test "$AFD_PLS_E2E_APPROVED" = true
```

## 1. Safe reruns and failure recovery

Resume at the failed literal subsection in the applicable operator document, not at an automation
wrapper. Re-source
`common.sh`, initialize names/state and `k`, then run that subsection's **READ ONLY** block before
its conditional **MUTATING** block. Exact resources must match deterministic names, tags, state,
topology, and scope. A conflicting existing resource is never replaced; inspect it and resolve the
ownership/configuration mismatch manually. Never delete an in-use subnet as recovery.

Common fixes:

- missing image state or ACR: resume at Part 1, Stage 6.1;
- missing/wrong subnet: resume at Part 1, Stage 6.2 only when absent; a wrong existing prefix/policy requires
  investigation, not an update;
- missing AKS cluster: resume at Part 1, Stage 6.3; an existing topology mismatch is a hard stop;
- missing identity/context: resume at Part 1, Stage 6.4;
- missing CRD/MemberCluster: resume at Part 1, Stage 6.5;
- controller timeout: inspect pod events/logs, then resume at Part 1, Stage 6.6;
- missing application/WAF/Gateway resources: resume at the matching literal subsection in Part 2.

Billable resources remain active until validation succeeds or you explicitly run:

```bash
# MUTATING / DESTRUCTIVE, but bounded to exact validated run resources
make phase7-e2e-cleanup
```

Never manually broaden the cleanup query, remove the approval guard, rename state entries, or use
subscription-wide deletion.

## 2. Bounded cleanup

Run cleanup after success or failure.

**MUTATING / DESTRUCTIVE:**

```bash
make phase7-e2e-cleanup
```

Cleanup validates subscription, state run ID, deterministic names, RG tags, and assignment IDs,
and then deletes:

- the exact run-recorded built-in role assignments;
- primary RG `fleet-afd-pls-${AFD_PLS_E2E_RUN_ID}`; and
- the three exact tagged node RGs.

It waits for RG deletion, then removes the generated kubeconfig, manifest directory, and state
inventory. It intentionally preserves the JSONL results file.

## 3. Verify all four resource groups are absent

**READ ONLY:**

```bash
for rg in \
  "$AFD_PLS_E2E_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_HUB_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER1_NODE_RESOURCE_GROUP" \
  "$AFD_PLS_E2E_MEMBER2_NODE_RESOURCE_GROUP"; do
  printf '%s: ' "$rg"
  az group exists --name "$rg"
done

```

Every RG query must print `false`. ACR, images, identities, and scoped role assignments are inside
the deleted resource groups or are explicitly removed from the state inventory during cleanup.

## 4. Optional local evidence cleanup

The guarded cleanup removes the generated kubeconfig, manifest directory, and state inventory but
preserves the JSONL evidence. After the four resource groups are confirmed absent and evidence is
no longer needed, remove only the exact run-scoped results file:

```bash
test -n "$AFD_PLS_E2E_RUN_ID"
test "$AFD_PLS_E2E_RESULTS_FILE" = \
  "${PHASE7_REPO_ROOT}/.phase7-${AFD_PLS_E2E_RUN_ID}.results.jsonl"
rm -f -- "$AFD_PLS_E2E_RESULTS_FILE"
```

## 5. Unset the run environment

Keep the results file if evidence is required, then clear all run variables:

```bash
unset AZURE_SUBSCRIPTION_ID
while IFS= read -r name; do unset "$name"; done < <(compgen -A variable AFD_PLS_E2E_)
```

Starting a new shell is an equivalent way to clear exported values.

## Optional automation/reference

`make phase7-e2e-cleanup` accurately represents this document's bounded cleanup boundary. The setup
stage scripts remain secondary implementation references, but their combined stages do not match
the four new product boundaries. For recovery, use the failed literal subsection instead.

[Previous: lifecycle validation](03-lifecycle-validation.md) ·
[Next: infrastructure setup (new run)](01-infrastructure-setup.md)
