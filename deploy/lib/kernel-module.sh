#!/usr/bin/env bash

# Return success when the loaded amneziawg module must be reloaded.
# Arguments: loaded version, installed version, DKMS version before install,
# DKMS version after install, and either "loaded" or "absent".
cascade_kernel_module_reload_required() {
  local loaded_version="$1"
  local installed_version="$2"
  local package_before="$3"
  local package_after="$4"
  local loaded_state="$5"

  [[ "$loaded_state" != "loaded" ]] && return 0
  [[ -z "$loaded_version" || -z "$installed_version" ]] && return 0
  [[ "$package_before" != "$package_after" ]] && return 0
  [[ "$loaded_version" != "$installed_version" ]] && return 0
  return 1
}

# Return success only when both exact module versions are known and equal.
cascade_kernel_module_versions_match() {
  local loaded_version="$1"
  local installed_version="$2"
  [[ -n "$loaded_version" && -n "$installed_version" && "$loaded_version" == "$installed_version" ]]
}
