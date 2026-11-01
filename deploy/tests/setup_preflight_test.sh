#!/usr/bin/env bash
# Verify that setup rejects invalid arguments before making host changes.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

bash "$REPO_DIR/deploy/setup.sh" --help > "$TMP_DIR/help"
grep -q 'Usage: setup.sh' "$TMP_DIR/help"
for args in --unknown '--yes --unknown'; do
  # Intentional splitting supplies the two flags in the second case.
  if bash "$REPO_DIR/deploy/setup.sh" $args > "$TMP_DIR/error" 2>&1; then
    echo "Setup accepted invalid arguments: $args" >&2
    exit 1
  fi
  grep -q 'Unknown argument: --unknown' "$TMP_DIR/error"
  if grep -q 'cascade setup started' "$TMP_DIR/error"; then
    echo "Setup started deployment before validating arguments." >&2
    exit 1
  fi
done

echo "Setup preflight tests passed."
