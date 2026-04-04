#!/usr/bin/env bash
# rbac-check.sh - Compare generated RBAC role.yaml against Helm clusterrole.yaml
# Usage: scripts/rbac-check.sh config/rbac/role.yaml helm-charts/.../clusterrole.yaml
#
# Extracts all "- <item>" lines from both files (stripping whitespace and Helm
# template expressions), then checks every item in the generated role exists
# in the Helm template. This catches missing resources and missing verbs.

set -euo pipefail

GENERATED="${1:?usage: rbac-check.sh <generated-role.yaml> <helm-clusterrole.yaml>}"
HELM="${2:?usage: rbac-check.sh <generated-role.yaml> <helm-clusterrole.yaml>}"

# Extract all "- value" items from a YAML file, one per line, sorted unique.
extract_items() {
  grep -E '^\s+- ' "$1" | sed 's/^[[:space:]]*- //' | sort -u
}

GENERATED_ITEMS=$(extract_items "$GENERATED")
HELM_ITEMS=$(extract_items "$HELM")

MISSING=()
while IFS= read -r item; do
  [[ -z "$item" ]] && continue
  # Skip apiGroup values (they look like "kubeswarm.io", "", "apps", etc.)
  # We only care about resources and verbs
  if echo "$HELM_ITEMS" | grep -qxF "$item"; then
    :
  else
    MISSING+=("$item")
  fi
done <<< "$GENERATED_ITEMS"

if [[ ${#MISSING[@]} -gt 0 ]]; then
  echo "rbac-check: ERROR: items in generated role.yaml missing from Helm clusterrole:"
  for m in "${MISSING[@]}"; do
    echo "  - $m"
  done
  echo ""
  echo "Add the missing items to the Helm clusterrole.yaml template."
  exit 1
fi

echo "rbac-check: RBAC in sync."
