#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=deploy/lib/kernel-module.sh
source "$REPO_DIR/deploy/lib/kernel-module.sh"

assert_reload() {
  if ! cascade_kernel_module_reload_required "$@"; then
    echo "Expected reload for: $*" >&2
    exit 1
  fi
}

assert_no_reload() {
  if cascade_kernel_module_reload_required "$@"; then
    echo "Unexpected reload for: $*" >&2
    exit 1
  fi
}

assert_no_reload 3.1.1 3.1.1 1.0 1.0 loaded
assert_reload 3.1.1 3.1.1 1.0 1.1 loaded
assert_reload 3.1.1 3.1.2 1.1 1.1 loaded
assert_reload "" 3.1.2 1.1 1.1 absent
assert_reload 3.1.0 3.1.1 1.0 1.0 loaded

if ! cascade_kernel_module_versions_match 3.1.1 3.1.1; then
  echo "Matching module versions were rejected." >&2
  exit 1
fi
if cascade_kernel_module_versions_match "" 3.1.1; then
  echo "Unknown loaded module version was accepted." >&2
  exit 1
fi
if cascade_kernel_module_versions_match 3.1.0 3.1.1; then
  echo "Mismatched module versions were accepted." >&2
  exit 1
fi

echo "Kernel module synchronization tests passed."
