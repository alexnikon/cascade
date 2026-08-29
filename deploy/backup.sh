#!/usr/bin/env bash
# Cascade production backup.
#
# The archive contains functional state and deployment configuration. Metrics
# history is intentionally opt-in because it can be much larger than the
# configuration database.
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DATA_DIR="$RUNTIME_DIR/data"
CONTAINER="cascade"
TIMESTAMP="$(date '+%Y-%m-%d_%H%M%S')"
DEST="$RUNTIME_DIR/cascade-backup-${TIMESTAMP}.tar.gz"
INCLUDE_METRICS=0

fail() { echo "backup: $*" >&2; exit 1; }
info() { echo "backup: $*"; }

usage() {
  cat <<'EOF'
Usage: bash deploy/backup.sh [--include-metrics] [--dest FILE]

  --include-metrics  Include metrics.db in the backup.
  --dest FILE        Write the archive to FILE instead of the timestamped
                     archive beside the deployment directory.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --include-metrics)
      INCLUDE_METRICS=1
      shift
      ;;
    --dest)
      [[ $# -ge 2 ]] || fail "--dest requires a file path"
      DEST="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[[ -d "$DATA_DIR" ]] || fail "data directory not found: $DATA_DIR"
[[ -f "$DATA_DIR/cascade.db" || -f "$DATA_DIR/wireguard.db" || -f "$DATA_DIR/awg.db" ]] || \
  fail "no Cascade database found in $DATA_DIR"
[[ ! -e "$DEST" ]] || fail "refusing to overwrite existing destination: $DEST"

DEST_PARENT="$(dirname "$DEST")"
mkdir -p "$DEST_PARENT"

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/cascade-backup.XXXXXX")"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT
mkdir -p "$STAGE/data"

ARCHIVE_PATHS=()

copy_database() {
  local name="$1"
  local source="$DATA_DIR/$name"
  local target="$STAGE/data/$name"
  local container_tmp="/tmp/cascade-backup-${name}-$$"

  [[ -f "$source" ]] || return 0

  if command -v docker >/dev/null 2>&1 && \
     docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$CONTAINER"; then
    if docker exec "$CONTAINER" sqlite3 "/etc/wireguard/data/$name" \
      ".backup '$container_tmp'" >/dev/null 2>&1 && \
      docker cp "$CONTAINER:$container_tmp" "$target" >/dev/null 2>&1; then
      docker exec "$CONTAINER" rm -f "$container_tmp" >/dev/null 2>&1 || true
      info "captured $name with SQLite online backup"
      ARCHIVE_PATHS+=("data/$name")
      return 0
    fi
    docker exec "$CONTAINER" rm -f "$container_tmp" >/dev/null 2>&1 || true
  fi

  if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "$source" ".backup '$target'"
    chmod --reference="$source" "$target" 2>/dev/null || true
    info "captured $name with SQLite online backup"
    ARCHIVE_PATHS+=("data/$name")
    return 0
  fi

  # A stopped database with no WAL/SHM files is safe to copy directly. Never
  # fall back to this path while sidecars indicate that a live WAL exists.
  if [[ ! -e "$source-wal" && ! -e "$source-shm" ]]; then
    cp -p "$source" "$target"
    info "captured $name from a stopped database"
    ARCHIVE_PATHS+=("data/$name")
    return 0
  fi

  fail "cannot safely back up $name: install sqlite3 or keep the Cascade container running"
}

copy_database cascade.db
copy_database wireguard.db
copy_database awg.db

if [[ "$INCLUDE_METRICS" -eq 1 ]]; then
  copy_database metrics.db
else
  info "excluding metrics.db; use --include-metrics to preserve dashboard history"
fi

while IFS= read -r -d '' save_file; do
  name="$(basename "$save_file")"
  cp -p "$save_file" "$STAGE/data/$name"
  ARCHIVE_PATHS+=("data/$name")
done < <(find "$DATA_DIR" -maxdepth 1 -type f -name '*.save' -print0)

copy_config() {
  local relative="$1"
  local source="$RUNTIME_DIR/$relative"
  local target="$STAGE/$relative"
  [[ -f "$source" ]] || return 0
  mkdir -p "$(dirname "$target")"
  cp -p "$source" "$target"
  ARCHIVE_PATHS+=("$relative")
}

# These are the known deployment inputs required to recreate the running
# Cascade/Caddy arrangement. Missing optional/generated files are skipped.
for config in \
  .env \
  docker-compose.yml \
  docker-compose.isolated.yml \
  docker-compose.bridge.yml \
  docker-compose.override.yml \
  docker-compose.override.yml.example \
  deploy/docker-compose.bridge.yml.example \
  deploy/setup.sh \
  deploy/switch-mode.sh \
  deploy/caddy/Caddyfile \
  deploy/caddy/docker-compose.yml \
  deploy/caddy/.env; do
  copy_config "$config"
done

{
  echo "Cascade backup"
  echo "Created: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "Metrics included: $([[ "$INCLUDE_METRICS" -eq 1 ]] && echo yes || echo no)"
  echo
  echo "Included paths:"
  printf '  %s\n' "${ARCHIVE_PATHS[@]}"
  echo
  echo "Excluded: SQLite WAL/SHM sidecars, caches, pre-restore archives,"
  echo "container state, and certificates stored outside the deployment root."
} > "$STAGE/BACKUP_MANIFEST.txt"
ARCHIVE_PATHS+=("BACKUP_MANIFEST.txt")

NEED_KB="$(du -sk "$STAGE" | awk '{print $1}')"
AVAIL_KB="$(df -Pk "$DEST_PARENT" | awk 'NR==2 {print $4}')"
if [[ "$AVAIL_KB" =~ ^[0-9]+$ ]] && (( AVAIL_KB < NEED_KB * 12 / 10 )); then
  fail "not enough disk space at $DEST_PARENT (need approximately $((NEED_KB * 12 / 10)) KB)"
fi

tar -czf "$DEST" -C "$STAGE" "${ARCHIVE_PATHS[@]}"
chmod 600 "$DEST"
info "created $DEST"
info "stop Cascade before restoring; restore data/config files, remove old WAL/SHM, then start Compose"
