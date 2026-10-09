#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

cat <<'EOF'
WARNING: phase7-e2e-setup runs all seven mutating stages sequentially.
It is optional and not recommended for debugging or resuming a retained run.
Prefer the numbered phase7-e2e-step-* Make targets documented in the README.
EOF

for stage in \
    step-01-registry-images \
    step-02-network \
    step-03-aks \
    step-04-identities-rbac-kubeconfig \
    step-05-crds-registration \
    step-06-waf-manifests \
    step-07-deploy-join; do
    bash "${SCRIPT_DIR}/${stage}.sh"
done
