#!/usr/bin/env bash
# Exercise host and bridge proxy paths inside a disposable network namespace.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TEST_BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cascade-metrics.XXXXXX")"
trap 'rm -rf "$TEST_BUILD_DIR"' EXIT
cd "$ROOT_DIR"
case "$(docker info --format '{{.Architecture}}')" in
  aarch64|arm64) TEST_ARCH=arm64 ;;
  x86_64|amd64) TEST_ARCH=amd64 ;;
  *) echo "Unsupported Docker architecture" >&2; exit 1 ;;
esac
CGO_ENABLED=0 GOOS=linux GOARCH="$TEST_ARCH" go build -o "$TEST_BUILD_DIR/cascade" ./cmd/cascade
cp deploy/caddy/Caddyfile "$TEST_BUILD_DIR/Caddyfile"
cp deploy/tests/metrics_e2e.py "$TEST_BUILD_DIR/metrics_e2e.py"
docker build -f deploy/tests/metrics.Dockerfile -t cascade-metrics-test:local "$TEST_BUILD_DIR"
docker run --rm --privileged cascade-metrics-test:local
