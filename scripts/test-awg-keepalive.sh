#!/usr/bin/env bash
# Verify keepalive ranges against the pinned AWG runtime in an isolated container.
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

cd "$repo_dir"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o "$test_dir/tunnel.test" ./internal/tunnel

docker run --rm --platform linux/amd64 --cap-add NET_ADMIN --device /dev/net/tun \
  --mount "type=bind,source=${test_dir}/tunnel.test,target=/tests/tunnel.test,readonly" \
  --env CASCADE_AWG_KEEPALIVE_TEST=1 \
  --entrypoint /tests/tunnel.test \
  amneziavpn/amneziawg-go:3.1.20260814@sha256:4450928744b051589bb3ba5cf6dd0cd8d7dc470b9432dc32d03d5ff5ede11b7a \
  -test.run '^TestKeepaliveRangeRuntime$' -test.v -test.timeout 60s
