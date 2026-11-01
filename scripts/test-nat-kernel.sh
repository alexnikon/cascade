#!/usr/bin/env bash
# Verify NAT lifecycle changes with real iptables-nft in an isolated container.
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

docker run --rm --cap-add NET_ADMIN \
  --mount "type=bind,source=${repo_dir},target=/src,readonly" \
  --workdir /src \
  --env CASCADE_NAT_KERNEL_TEST=1 \
  --env GOFLAGS=-mod=readonly \
  golang:1.23-bookworm \
  bash -c 'apt-get update -qq && apt-get install -y -qq iptables && go test ./internal/nat -run "^TestNAT" -count=1 -timeout 120s -v'
