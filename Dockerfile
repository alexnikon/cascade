# ============================================================
# Cascade — Go/Fiber build
# ============================================================
# Stage 1: Build Go binary
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy source first (go mod tidy needs imports to resolve indirect deps).
COPY go.mod ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Resolve full dependency graph, update go.mod with indirect deps, generate go.sum.
# Cached unless go.mod or source changes.
RUN go mod tidy

# Version metadata injected at build time so the binary knows its own version.
# ARG VERSION defaults to "dev" for local builds without explicit --build-arg.
# CI passes the git tag (e.g. "v1.2.3") and short commit hash.
ARG VERSION=dev
ARG GIT_COMMIT=unknown

# Build static binary.
# CGO_ENABLED=0: fully static binary, no libc dependency.
# -ldflags="-s -w": strip debug symbols → smaller binary.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w \
      -X github.com/alexnikon/cascade/internal/version.Version=${VERSION} \
      -X github.com/alexnikon/cascade/internal/version.GitCommit=${GIT_COMMIT}" \
    -o cascade \
    ./cmd/awg-easy

# ============================================================
# Stage 2: Runtime image
# Base: amneziawg-go (has awg-quick, awg, wg-quick, wg tools)
# Intentionally unpinned. In Kernel module run mode, the `awg` CLI's netlink
# wire format (H1-H4 as packed ranges, etc.) must match the amneziawg kernel
# module's netlink policy, or `awg setconf` fails with
# "Unable to modify interface: Invalid argument" (confirmed via bisection —
# see issue: CLI v1.0.20210914 + kernel module 3.0.x = EINVAL on H1-H4;
# CLI v3.0.20260730 + kernel module 3.0.x = works). deploy/setup.sh's
# ppa:amnezia/ppa is also unpinned and currently only ships 3.0.x, so tracking
# :latest here keeps both sides on the same major line automatically.
# Keep both the tag and manifest digest pinned. A version bump requires
# configuration compatibility review and an updated tools-version assertion.
# ============================================================
FROM amneziavpn/amneziawg-go:3.1.20260814@sha256:4450928744b051589bb3ba5cf6dd0cd8d7dc470b9432dc32d03d5ff5ede11b7a

# The official image checks the fork's platform-neutral release manifest.
# Operators can override this value or set it to an empty string.

RUN awg --version | grep -F 'amneziawg-tools v3.1.20260812'

HEALTHCHECK --interval=1m --timeout=5s --retries=3 \
    CMD /usr/bin/timeout 5s /bin/sh -c "/usr/bin/wg show | /bin/grep -q interface || exit 1"

# Runtime dependencies:
# - dumb-init: proper PID 1 signal handling
# - iptables / iptables-legacy: firewall management
# - iproute2: ip route/rule commands
# - ipset: alias ipsets for firewall rules
# NOTE: no node, no libstdc++, no libgcc — Go binary is static
RUN apk add --no-cache \
    dumb-init \
    iptables \
    iptables-legacy \
    iproute2 \
    ipset \
    sqlite \
    conntrack-tools \
    iperf3 \
    iputils-ping \
    traceroute \
    tcpdump \
    coreutils

# Use iptables-legacy as default iptables.
# Alpine does not provide update-alternatives; that command belongs to dpkg/Debian.
RUN ln -sf /sbin/iptables-legacy         /sbin/iptables && \
    ln -sf /sbin/iptables-legacy-restore /sbin/iptables-restore && \
    ln -sf /sbin/iptables-legacy-save    /sbin/iptables-save

# Copy the static Go binary and entrypoint from build stage
COPY --from=builder /app/cascade /usr/local/bin/cascade
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Data directory (mapped via volume in docker-compose)
RUN mkdir -p /etc/wireguard/data

# entrypoint.sh:
#   - enables ip_forward / src_valid_mark in this netns
#   - if WAIT_FOR_NETWORK=1: waits for a default route (OVS isolated mode)
#   - then exec's cascade
CMD ["/usr/bin/dumb-init", "/usr/local/bin/entrypoint.sh", "--data-dir", "/etc/wireguard/data"]
