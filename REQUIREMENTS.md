# Cascade requirements

This document records the product and implementation constraints for Cascade.
The running Go implementation and the API reference are authoritative when a
historical requirement conflicts with current behavior.

## Product model

Cascade manages WireGuard 1.0 and AmneziaWG tunnel interfaces, peers, routing,
NAT, firewall policy, gateways, aliases, remote Cascade instances, metrics, and
administrative settings from one web application.

An interface is either a client-facing tunnel or an interconnect. Client-facing
interfaces allocate peer addresses and provide downloadable configurations.
Interconnect interfaces accept explicit remote networks and do not assume that
the peer is an end-user device.

## Backend requirements

- The backend is a single Go service using Fiber and SQLite.
- Persistent mutations must be transactional where concurrent requests can
  violate an invariant.
- Interface and peer caches must use database IDs as their only identity keys.
- Startup must load persistent state before restoring routes, NAT, firewall,
  aliases, gateways, and background monitoring.
- External commands must use bounded timeouts and return useful stderr details.
- WireGuard 1.0 uses `wg` and `wg-quick`; AmneziaWG uses the matching AWG tools.
- The service must not depend on the original fork repository at runtime.

## Interfaces

Each interface has a stable ID, display name, protocol, address prefix, listen
port, key pair, DNS override, route behavior, MTU override, and optional AWG
parameters. New interfaces start disabled unless the creation workflow
explicitly starts them.

Quick creation selects a free subnet and UDP port from the configured pools.
Manual creation exposes all protocol-specific settings. Address ranges must not
overlap existing interfaces. Ports must be valid, available, and inside the
configured pool when a pool is enforced.

`disableRoutes` distinguishes an interconnect-style interface from a normal
client VPN interface. It suppresses automatic route installation but does not
disable explicit routing, NAT, or firewall configuration.

## Peers

Peers have stable IDs, interface ownership, keys, allowed IPs, optional endpoint
and keepalive values, enable state, expiry, group membership, traffic counters,
rate limits, and optional one-time download tokens.

- Client peer addresses are allocated from the parent interface subnet.
- Interconnect peers may advertise multiple networks and require explicit
  validation against conflicting routes.
- Private keys and preshared keys must be omitted from aggregate API responses.
- One-time configuration links must be redeemed atomically in SQLite.
- Configuration generation must complete before token redemption, so a
  generation failure does not destroy a valid link.
- Concurrent redemption may return the configuration to exactly one caller.
- Cache refreshes and repeated updates must never create duplicate peer aliases.

## Routing, NAT, firewall, and gateways

- Static routes and policy routing must be restored after interfaces exist.
- Command output parsing must work without requiring JSON support from `ip`.
- NAT rules must be idempotent and use `iptables-nft` in supported images.
- Generated forwarding rules must cover both traffic directions as required.
- Gateway health changes must be debounced to avoid route flapping.
- Alias-backed firewall and routing rules must tolerate asynchronous alias
  refresh without exposing a partially built set.

## Settings

Global settings include DNS, client allowed IPs, host/address discovery, MTU,
subnet and port pools, language, theme-independent service settings, update
manifest URL, avatar providers, and feature flags. AWG templates are persistent,
named profiles that can be generated, edited, imported, and exported.

Browser avatar services must be optional. Disabling DiceBear and Gravatar must
prevent browser requests to those services.

## Frontend requirements

- The embedded frontend uses Vue 2 and the statically generated CSS bundle.
- New markup may only rely on utilities present in the bundled stylesheet.
- Array replacement must preserve Vue 2 reactivity, using reactive assignment
  or `splice` where needed.
- Desktop GridStack layout state must remain independent from compact layouts.
- Phone and tablet layouts must respect safe-area insets and native scrolling.
- Russian product localization remains supported; repository documentation is
  English-only.
- Light, dark, and automatic themes must synchronize page content, CSS
  `color-scheme`, Safari browser chrome, and standalone status-bar appearance.

## Security and operations

- Authentication middleware protects administrative API routes.
- Login, token, backup, and remote-proxy workflows must not leak credentials.
- Release artifacts are addressed by a provider-neutral base URL and verified by
  checksum. OCI images use immutable digest references.
- Rollback must retain the previously running image and configuration.
- Official GitHub/GHCR publishing is an adapter, not an application dependency.
- Critical build and runtime inputs must be pinned and verifiable.

## Development invariants

- Update [docs/API.md](docs/API.md) whenever an API contract changes.
- Keep comments and documentation in English.
- Preserve unrelated working-tree changes.
- Run Go tests, focused race tests, frontend syntax/embed tests, shell tests,
  autonomy checks, `git diff --check`, Compose rendering, and an image build for
  release-sensitive changes.
- Commit, publish, release, and deploy are separate explicit operations.
