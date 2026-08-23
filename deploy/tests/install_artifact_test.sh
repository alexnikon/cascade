#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT
DIST_DIR="$TMP_DIR/dist"
RUNTIME_DIR="$TMP_DIR/runtime"
FAKE_BIN="$TMP_DIR/bin"
bash "$REPO_DIR/scripts/build-deploy-bundle.sh" v2.0.0 "$DIST_DIR"
mkdir -p "$FAKE_BIN"

cat > "$FAKE_BIN/id" <<'EOF'
#!/bin/bash
[[ "${1:-}" == "-u" ]] && echo 0
EOF
cat > "$FAKE_BIN/curl" <<'EOF'
#!/bin/bash
output=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      output="$2"
      shift 2
      ;;
    http*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done
cp "$CASCADE_TEST_DIST/${url##*/}" "$output"
EOF
cat > "$FAKE_BIN/sha256sum" <<'EOF'
#!/bin/bash
if [[ "${1:-}" == "-c" ]]; then
  exec /usr/bin/shasum -a 256 -c "$2"
fi
exec /usr/bin/shasum -a 256 "$@"
EOF
cat > "$FAKE_BIN/bash" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" > "$CASCADE_TEST_SETUP_CALL"
EOF
chmod 0755 "$FAKE_BIN/id" "$FAKE_BIN/curl" "$FAKE_BIN/sha256sum" "$FAKE_BIN/bash"

CASCADE_TEST_DIST="$DIST_DIR" CASCADE_TEST_SETUP_CALL="$TMP_DIR/setup-call" \
  PATH="$FAKE_BIN:/usr/bin:/bin" /bin/bash "$DIST_DIR/install.sh" \
  --version v2.0.0 --install-dir "$RUNTIME_DIR" --yes

grep -q "$RUNTIME_DIR/deploy/setup.sh --yes" "$TMP_DIR/setup-call"
grep -q 'image: ghcr.io/alexnikon/cascade:2.0.0' "$RUNTIME_DIR/docker-compose.yml"
[[ ! -e "$RUNTIME_DIR/release-manifest.json" ]]
[[ ! -e "$RUNTIME_DIR/.releases" ]]
[[ ! -e "$RUNTIME_DIR/deploy/update.sh" ]]
[[ ! -e "$RUNTIME_DIR/.git" ]]

printf 'ADMIN_PATH=test\n' > "$RUNTIME_DIR/.env"
mkdir -p "$RUNTIME_DIR/data"
printf 'database-placeholder\n' > "$RUNTIME_DIR/data/cascade.db"
sed -i.bak 's|cascade:2\.0\.0|cascade:2.0.1|' "$RUNTIME_DIR/docker-compose.yml"
grep -q 'image: ghcr.io/alexnikon/cascade:2.0.1' "$RUNTIME_DIR/docker-compose.yml"
grep -q '^ADMIN_PATH=test$' "$RUNTIME_DIR/.env"
grep -q '^database-placeholder$' "$RUNTIME_DIR/data/cascade.db"
rm "$RUNTIME_DIR/docker-compose.yml.bak"

BAD_DIST_DIR="$TMP_DIR/bad-dist"
BAD_RUNTIME_DIR="$TMP_DIR/bad-runtime"
cp -R "$DIST_DIR" "$BAD_DIST_DIR"
printf '0%.0s' {1..64} > "$BAD_DIST_DIR/cascade-deploy-v2.0.0.tar.gz.sha256"
printf '  cascade-deploy-v2.0.0.tar.gz\n' >> "$BAD_DIST_DIR/cascade-deploy-v2.0.0.tar.gz.sha256"
if CASCADE_TEST_DIST="$BAD_DIST_DIR" CASCADE_TEST_SETUP_CALL="$TMP_DIR/bad-setup-call" \
  PATH="$FAKE_BIN:/usr/bin:/bin" /bin/bash "$BAD_DIST_DIR/install.sh" \
  --version v2.0.0 --install-dir "$BAD_RUNTIME_DIR" --yes; then
  echo "Installer accepted an invalid deployment checksum." >&2
  exit 1
fi
[[ ! -e "$BAD_RUNTIME_DIR/docker-compose.yml" ]]

echo "Artifact installer test passed."
