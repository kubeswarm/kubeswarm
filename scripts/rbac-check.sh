#!/usr/bin/env bash
# rbac-check.sh - Compare generated RBAC role.yaml against Helm clusterrole.yaml.
# Usage: scripts/rbac-check.sh config/rbac/role.yaml helm-charts/.../clusterrole.yaml
#
# Checks two directions:
#   1. Every (resource, verb) pair in the generated role must appear in Helm.
#      Catches the case where Helm forgot to track a new controller permission.
#   2. Every (resource, verb) pair in Helm must either appear in the generated
#      role or be listed in ALLOWED_EXTRAS below. Catches phantom/dead entries
#      (e.g. permissions for a CRD that was never shipped).
#
# Requires: yq (mikefarah, v4+). Install: brew install yq.

set -euo pipefail

GENERATED="${1:?usage: rbac-check.sh <generated-role.yaml> <helm-clusterrole.yaml>}"
HELM="${2:?usage: rbac-check.sh <generated-role.yaml> <helm-clusterrole.yaml>}"

# Permissions intentionally granted in Helm that are not (or cannot be)
# expressed as +kubebuilder:rbac markers. Keep this list small and explain
# each entry - if something sits here for long, promote it to a marker.
ALLOWED_EXTRAS=$(cat <<'EOF'
scaledobjects:create
scaledobjects:delete
scaledobjects:get
scaledobjects:list
scaledobjects:patch
scaledobjects:update
scaledobjects:watch
secrets:create
configmaps:create
configmaps:patch
configmaps:update
EOF
)

# Emit one "resource:verb" line per rule. Helm template expressions are
# stripped first so yq sees plain YAML.
extract_pairs() {
  sed 's/{{[^}]*}}//g' "$1" \
    | yq '.rules[] | .resources[] as $r | .verbs[] as $v | $r + ":" + $v' \
    | sort -u
}

GENERATED_PAIRS=$(extract_pairs "$GENERATED")
HELM_PAIRS=$(extract_pairs "$HELM")

STATUS=0

# Direction 1: role.yaml must be a subset of Helm.
MISSING_FROM_HELM=$(comm -23 <(echo "$GENERATED_PAIRS") <(echo "$HELM_PAIRS"))
if [[ -n "$MISSING_FROM_HELM" ]]; then
  echo "rbac-check: ERROR: resource:verb pairs in generated role.yaml missing from Helm clusterrole:"
  while IFS=: read -r resource verb; do
    echo "  - resource=$resource verb=$verb"
  done <<< "$MISSING_FROM_HELM"
  echo
  echo "Update the Helm clusterrole.yaml template to include the missing permissions."
  STATUS=1
fi

# Direction 2: Helm must be a subset of (role.yaml union ALLOWED_EXTRAS).
ALLOWED_PAIRS=$(echo "$GENERATED_PAIRS"; echo "$ALLOWED_EXTRAS")
ALLOWED_PAIRS=$(echo "$ALLOWED_PAIRS" | sort -u)
EXTRA_IN_HELM=$(comm -23 <(echo "$HELM_PAIRS") <(echo "$ALLOWED_PAIRS"))
if [[ -n "$EXTRA_IN_HELM" ]]; then
  echo "rbac-check: ERROR: resource:verb pairs in Helm clusterrole not in generated role.yaml or ALLOWED_EXTRAS:"
  while IFS=: read -r resource verb; do
    echo "  - resource=$resource verb=$verb"
  done <<< "$EXTRA_IN_HELM"
  echo
  echo "Either add a +kubebuilder:rbac marker that covers this, remove the Helm entry,"
  echo "or add it to ALLOWED_EXTRAS in scripts/rbac-check.sh with a comment explaining why."
  STATUS=1
fi

if [[ $STATUS -eq 0 ]]; then
  echo "rbac-check: RBAC in sync."
fi

exit $STATUS
