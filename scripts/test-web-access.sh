#!/usr/bin/env bash
# Run only against an isolated local Docker engine; no host networking is used.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TEST_BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cascade-web-access.XXXXXX")"
trap 'rm -rf "$TEST_BUILD_DIR"' EXIT
cd "$ROOT_DIR"
case "$(docker info --format '{{.Architecture}}')" in
  aarch64|arm64) TEST_ARCH=arm64 ;;
  x86_64|amd64) TEST_ARCH=amd64 ;;
  *) echo "Unsupported Docker architecture" >&2; exit 1 ;;
esac
CGO_ENABLED=0 GOOS=linux GOARCH="$TEST_ARCH" go build -o "$TEST_BUILD_DIR/cascade" ./cmd/cascade
cp deploy/caddy/Caddyfile "$TEST_BUILD_DIR/Caddyfile"
cp deploy/tests/web_access_e2e.py "$TEST_BUILD_DIR/web_access_e2e.py"
cp deploy/tests/web_access_http3.py "$TEST_BUILD_DIR/web_access_http3.py"
cp deploy/tests/web_access_http3_migration.py "$TEST_BUILD_DIR/web_access_http3_migration.py"
cp deploy/tests/web_access_browser_proxy.py "$TEST_BUILD_DIR/web_access_browser_proxy.py"
docker build -f deploy/tests/web-access.Dockerfile -t cascade-web-access-test:local "$TEST_BUILD_DIR"
docker run --rm --privileged cascade-web-access-test:local
