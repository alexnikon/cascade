#!/usr/bin/env bash
# =============================================================================
# Cascade — Switch AmneziaWG run mode without full re-setup
# Usage:
#   bash deploy/switch-mode.sh --userspace   # switch to amneziawg-go
#   bash deploy/switch-mode.sh --kernel      # switch to kernel module
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="$RUNTIME_DIR/.env"
COMPOSE_FILE="$RUNTIME_DIR/docker-compose.yml"
KERNEL_MODULE_HELPERS="$SCRIPT_DIR/lib/kernel-module.sh"

G='\033[0;32m'; Y='\033[1;33m'; R='\033[0;31m'; B='\033[0;34m'; N='\033[0m'
ok()   { echo -e "  ${G}✓${N} $*"; }
info() { echo -e "  ${B}→${N} $*"; }
warn() { echo -e "  ${Y}⚠${N} $*"; }
fail() { echo -e "  ${R}✗${N} $*"; exit 1; }

[[ -f "$KERNEL_MODULE_HELPERS" ]] || fail "Missing kernel module helpers: $KERNEL_MODULE_HELPERS"
# shellcheck source=deploy/lib/kernel-module.sh
source "$KERNEL_MODULE_HELPERS"

# ── Parse args ────────────────────────────────────────────────────────────────
MODE=""
for arg in "$@"; do
  case "$arg" in
    --userspace) MODE="userspace" ;;
    --kernel)    MODE="kernel"    ;;
    -h|--help)
      echo "Usage: bash deploy/switch-mode.sh --userspace | --kernel"
      echo ""
      echo "  --userspace   Use amneziawg-go (stable, no kernel module required)"
      echo "  --kernel      Use AmneziaWG kernel module (faster, may have deadlock issues)"
      exit 0
      ;;
    *) fail "Unknown argument: $arg. Use --userspace or --kernel" ;;
  esac
done

[[ -z "$MODE" ]] && { echo "Usage: bash deploy/switch-mode.sh --userspace | --kernel"; exit 1; }

# ── Must be root ──────────────────────────────────────────────────────────────
[[ "$(id -u)" -ne 0 ]] && fail "Run as root: sudo bash deploy/switch-mode.sh --$MODE"

# ── Detect docker compose ─────────────────────────────────────────────────────
if docker compose version &>/dev/null 2>&1; then
  COMPOSE_CMD="docker compose"
elif command -v docker-compose &>/dev/null; then
  COMPOSE_CMD="docker-compose"
else
  fail "docker compose not found"
fi

COMPOSE_ARGS=(-f "$COMPOSE_FILE")
if [[ -f "$RUNTIME_DIR/docker-compose.override.yml" ]]; then
  COMPOSE_ARGS+=(-f "$RUNTIME_DIR/docker-compose.override.yml")
fi

# ── Mode helpers ──────────────────────────────────────────────────────────────
apply_userspace() {
  info "Switching to userspace mode (amneziawg-go)..."
  if lsmod | grep -q amneziawg 2>/dev/null; then
    info "Unloading amneziawg kernel module..."
    modprobe -r amneziawg 2>/dev/null || warn "Could not unload — reboot may be required"
  fi
  echo "blacklist amneziawg" > /etc/modprobe.d/amneziawg-blacklist.conf
  rm -f /etc/modules-load.d/amneziawg.conf
  ok "Kernel module blacklisted"
}

apply_kernel() {
  info "Switching to kernel module mode..."

  dkms_version_before="$(dpkg-query -W -f='${Version}' amneziawg-dkms 2>/dev/null || true)"

  if [[ ! -e "/lib/modules/$(uname -r)/build" ]]; then
    info "Installing kernel headers for $(uname -r) (required for DKMS)..."
    if ! apt-get install -y "linux-headers-$(uname -r)" 2>/dev/null; then
      apt-get update -qq
      apt-get install -y "linux-headers-$(uname -r)" || \
        fail "Could not install kernel headers for $(uname -r); install matching headers and retry"
    fi
  fi

  rm -f /etc/modprobe.d/amneziawg-blacklist.conf

  if ! dpkg-query -W -f='${db:Status-Status}' amneziawg-dkms 2>/dev/null | grep -qx installed; then
    info "Installing amneziawg kernel module (ppa:amnezia/ppa)..."
    add-apt-repository -y ppa:amnezia/ppa > /dev/null 2>&1
    apt-get update -qq
  else
    info "Checking for AmneziaWG DKMS updates (ppa:amnezia/ppa)..."
    apt-get update -qq
  fi

  apt-get install -y amneziawg amneziawg-dkms

  module_loaded() {
    lsmod | grep -q '^amneziawg[[:space:]]' 2>/dev/null
  }
  module_version() {
    [[ -r /sys/module/amneziawg/version ]] || return 0
    tr -d '[:space:]' < /sys/module/amneziawg/version
  }
  installed_module_version() {
    modinfo -F version amneziawg 2>/dev/null | head -n 1 | tr -d '[:space:]'
  }
  dkms_version_after="$(dpkg-query -W -f='${Version}' amneziawg-dkms 2>/dev/null || true)"
  installed_version="$(installed_module_version)"
  [[ -n "$installed_version" ]] || fail "Cannot determine installed amneziawg module version; verify /lib/modules and kmod"

  loaded_version=""
  loaded_state="absent"
  if module_loaded; then
    loaded_state="loaded"
    loaded_version="$(module_version)"
  fi

  if ! cascade_kernel_module_reload_required "$loaded_version" "$installed_version" "$dkms_version_before" "$dkms_version_after" "$loaded_state"; then
    echo "amneziawg" > /etc/modules-load.d/amneziawg.conf
    ok "amneziawg already loaded and synchronized (${installed_version}); DKMS ${dkms_version_after}"
    return
  fi

  if module_loaded; then
    info "Synchronizing loaded module (${loaded_version:-unknown} → ${installed_version}); DKMS ${dkms_version_before:-unknown} → ${dkms_version_after}..."
    modprobe -r amneziawg || fail "Could not unload the old amneziawg module; Cascade was not restarted"
  else
    info "Loading amneziawg kernel module (${installed_version})..."
  fi

  modprobe amneziawg || fail "Could not load amneziawg ${installed_version}; Cascade was not restarted"
  echo "amneziawg" > /etc/modules-load.d/amneziawg.conf

  loaded_version="$(module_version)"
  if ! module_loaded || ! cascade_kernel_module_versions_match "$loaded_version" "$installed_version"; then
    fail "Loaded amneziawg module version ${loaded_version:-unknown} does not match installed ${installed_version}; Cascade was not restarted"
  fi
  ok "amneziawg synchronized and loaded (${loaded_version})"
}

# ── Update .env ───────────────────────────────────────────────────────────────
update_env() {
  local userspace_val=""
  [[ "$MODE" == "userspace" ]] && userspace_val="amneziawg-go"

  if [[ -f "$ENV_FILE" ]]; then
    # Update existing values
    if grep -q "^AWG_USERSPACE_IMPL=" "$ENV_FILE"; then
      sed -i "s|^AWG_USERSPACE_IMPL=.*|AWG_USERSPACE_IMPL=${MODE}|" "$ENV_FILE"
    else
      echo "AWG_USERSPACE_IMPL=${MODE}" >> "$ENV_FILE"
    fi
    if grep -q "^WG_QUICK_USERSPACE_IMPLEMENTATION=" "$ENV_FILE"; then
      sed -i "s|^WG_QUICK_USERSPACE_IMPLEMENTATION=.*|WG_QUICK_USERSPACE_IMPLEMENTATION=${userspace_val}|" "$ENV_FILE"
    else
      echo "WG_QUICK_USERSPACE_IMPLEMENTATION=${userspace_val}" >> "$ENV_FILE"
    fi
  else
    # Create minimal env file
    printf "AWG_USERSPACE_IMPL=%s\nWG_QUICK_USERSPACE_IMPLEMENTATION=%s\n" "$MODE" "$userspace_val" > "$ENV_FILE"
  fi
  ok ".env updated"
}

# ── Main ──────────────────────────────────────────────────────────────────────
cd "$RUNTIME_DIR"

echo ""
echo -e "${B}── Cascade: switching AWG mode → ${MODE}${N}"
echo ""

if [[ "$MODE" == "userspace" ]]; then
  apply_userspace
else
  apply_kernel
fi

update_env

# Restart container if running
if $COMPOSE_CMD "${COMPOSE_ARGS[@]}" ps --quiet 2>/dev/null | grep -q .; then
  info "Restarting Cascade container..."
  $COMPOSE_CMD "${COMPOSE_ARGS[@]}" down
  $COMPOSE_CMD "${COMPOSE_ARGS[@]}" up -d
  ok "Container restarted"

  sleep 2
  echo ""
  echo -e "${B}── Verification${N}"
  WG_QUICK_VAL=$(docker exec cascade env 2>/dev/null | grep WG_QUICK || echo "(not found)")
  echo "  WG_QUICK_USERSPACE_IMPLEMENTATION: $(echo "$WG_QUICK_VAL" | cut -d= -f2)"

  if [[ "$MODE" == "userspace" ]]; then
    PROC=$(docker exec cascade ps aux 2>/dev/null | grep amneziawg-go | grep -v grep || echo "")
    if [[ -n "$PROC" ]]; then
      ok "amneziawg-go process running"
    else
      info "amneziawg-go will start when first interface is brought up"
    fi
  else
    if lsmod | grep -q amneziawg; then
      ok "amneziawg kernel module loaded"
    else
      warn "Module not loaded — check dmesg"
    fi
  fi
else
  info "Container is not running — start with:"
  echo "  $COMPOSE_CMD -f docker-compose.yml up -d"
fi

echo ""
ok "Done. Mode: ${MODE}"
echo ""
