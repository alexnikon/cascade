#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

[[ -x "$REPO_DIR/deploy/backup.sh" ]]
command -v sqlite3 >/dev/null 2>&1

RUNTIME_DIR="$TMP_DIR/runtime"
mkdir -p "$RUNTIME_DIR/deploy/caddy" "$RUNTIME_DIR/data"
cp "$REPO_DIR/deploy/backup.sh" "$RUNTIME_DIR/deploy/backup.sh"
chmod 0755 "$RUNTIME_DIR/deploy/backup.sh"

sqlite3 "$RUNTIME_DIR/data/cascade.db" 'CREATE TABLE settings (key TEXT, value TEXT); INSERT INTO settings VALUES ("mode", "test");'
printf 'ipset snapshot\n' > "$RUNTIME_DIR/data/clients.save"
printf 'WG_HOST=test.example\n' > "$RUNTIME_DIR/.env"
printf 'services:\n  cascade:\n    image: test\n' > "$RUNTIME_DIR/docker-compose.yml"
printf 'services:\n  cascade:\n    image: test-isolated\n' > "$RUNTIME_DIR/docker-compose.isolated.yml"
printf 'services:\n  cascade:\n    image: test-bridge\n' > "$RUNTIME_DIR/docker-compose.bridge.yml"
printf 'services:\n  cascade:\n    image: local\n' > "$RUNTIME_DIR/docker-compose.override.yml"
printf 'example\n' > "$RUNTIME_DIR/docker-compose.override.yml.example"
printf 'bridge example\n' > "$RUNTIME_DIR/deploy/docker-compose.bridge.yml.example"
printf 'Caddyfile\n' > "$RUNTIME_DIR/deploy/caddy/Caddyfile"
printf 'services:\n' > "$RUNTIME_DIR/deploy/caddy/docker-compose.yml"

(
  cd "$RUNTIME_DIR"
  bash deploy/backup.sh
)
DEST="$(find "$RUNTIME_DIR" -maxdepth 1 -type f -name 'cascade-backup-*.tar.gz' -print -quit)"
[[ "$(basename "$DEST")" =~ ^cascade-backup-[0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{6}\.tar\.gz$ ]]
[[ -f "$DEST" ]]
if stat -c '%a' "$DEST" >/dev/null 2>&1; then
  MODE="$(stat -c '%a' "$DEST")"
else
  MODE="$(stat -f '%Lp' "$DEST")"
fi
[[ "$MODE" == "600" ]]
CONTENTS="$(tar -tzf "$DEST")"
grep -q '^data/cascade.db$' <<< "$CONTENTS"
grep -q '^data/clients.save$' <<< "$CONTENTS"
grep -q '^.env$' <<< "$CONTENTS"
grep -q '^docker-compose.override.yml$' <<< "$CONTENTS"
grep -q '^deploy/caddy/Caddyfile$' <<< "$CONTENTS"
! grep -q 'metrics.db' <<< "$CONTENTS"
! grep -qE '(-wal|-shm)$' <<< "$CONTENTS"
mkdir -p "$TMP_DIR/extract"
tar -xzf "$DEST" -C "$TMP_DIR/extract" data/cascade.db
[[ "$(sqlite3 "$TMP_DIR/extract/data/cascade.db" 'SELECT value FROM settings WHERE key = "mode";')" == "test" ]]

sqlite3 "$RUNTIME_DIR/data/metrics.db" 'CREATE TABLE history (value INTEGER); INSERT INTO history VALUES (1);'
bash "$RUNTIME_DIR/deploy/backup.sh" --include-metrics --dest "$TMP_DIR/with-metrics.tar.gz"
tar -tzf "$TMP_DIR/with-metrics.tar.gz" | grep -q '^data/metrics.db$'

if bash "$RUNTIME_DIR/deploy/backup.sh" --dest "$DEST" 2>/dev/null; then
  echo "backup script overwrote an existing archive" >&2
  exit 1
fi

echo "Backup script test passed."
