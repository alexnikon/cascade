# Cascade architecture

## Overview

Cascade is a self-contained Go control plane for WireGuard and AmneziaWG. It
embeds its Vue 2 frontend, stores configuration in SQLite, and applies networking
state through bounded system commands. The application does not fetch or execute
code from the original repository.

## Technology stack

| Layer | Technology |
| --- | --- |
| Backend | Go, Fiber |
| Persistence | SQLite |
| Frontend | Vue 2, embedded HTML/CSS/JavaScript |
| VPN | WireGuard 1.0, AmneziaWG 2.0 and 3.1 |
| Network policy | iproute2, iptables-nft, ipset, traffic control |
| Packaging | Docker/OCI and deployment bundles |

## Package boundaries

- `internal/db` owns connections, migrations, and transaction helpers.
- `internal/tunnel` owns interface lifecycle, in-memory interface/peer caches,
  configuration generation, kernel synchronization, and status polling.
- `internal/peer` owns peer persistence, key handling, remote configuration,
  expiry, traffic totals, and atomic one-time-token consumption.
- `internal/settings` owns global settings, templates, and host discovery.
- `internal/version` owns build identity and informational GitHub release checks.
- `internal/routing` owns static routes and policy-routing restoration.
- `internal/nat` owns explicit and interface-derived NAT rules.
- `internal/firewall`, `internal/aliases`, and `internal/ipset` own policy rules
  and reusable address sets.
- `internal/gateway` owns health probes, groups, failover, and anti-flap logic.
- `internal/users` and `internal/tokens` own authentication identities and API
  tokens.
- `internal/api` maps HTTP contracts to domain operations.
- `internal/frontend` embeds the browser application and documentation.

## Startup and shutdown

Initialization is deliberately ordered:

1. Open SQLite and apply migrations.
2. Load settings and authentication state.
3. Load tunnel interfaces and peers; start enabled interfaces.
4. Restore routes, NAT, aliases, firewall policy, and gateways after interfaces
   exist in the kernel.
5. Start status, metrics, expiry, update, and health-monitoring loops.
6. Register authenticated and unauthenticated HTTP routes and serve the embedded
   frontend.

Graceful shutdown stops pollers, flushes traffic totals, stops interfaces where
required, and closes databases only after outstanding persistence work finishes.

## Tunnel and peer identity

`Manager` owns a map of `TunnelInterface` objects keyed by interface ID. Each
interface owns a peer map keyed only by persisted peer ID. `replaceCachedPeer`
removes stale aliases by ID or public key before storing the authoritative row
under `updated.ID`. This invariant protects repeated update and reload paths.

`GetPeerRemoteConfig(interfaceID, peerID)` is the ID-based public wrapper.
`BuildPeerRemoteConfig(interface, peer)` accepts an already authoritative pair
and deliberately performs no second cache lookup. One-time-link handling reloads
the peer from SQLite, builds the configuration, then uses a conditional SQL
update to consume the token. Therefore generation errors preserve the token and
concurrent callers cannot both succeed.

## Interface lifecycle

Interface configuration is regenerated before start and after relevant peer or
interface changes. Protocol selection determines the quick and sync binaries.
Kernel operations are serialized per interface. A bounded sync operation may
fall back to restart when the runtime tool cannot complete safely.

WireGuard 1.0 supports incremental peer updates. Some AmneziaWG changes require
a full regeneration or restart because protocol-specific runtime behavior is not
equivalent. Stop tolerates an interface that is already absent.

## Persistence

The main SQLite database stores settings, templates, interfaces, peers, routes,
NAT rules, aliases, firewall rules, gateways, gateway groups, users, API tokens,
remotes, and schema migration state. Metrics use their dedicated persistent
store where configured.

Runtime-only data includes process locks, poller state, short-lived health
samples, command output, and kernel-derived handshake/traffic snapshots. Durable
traffic totals are periodically flushed and again during graceful shutdown.

## Routing and policy flow

Static routes are persisted separately from kernel state. Restore operations are
idempotent and occur after interfaces are available. Policy routing can mark
traffic in firewall rules and select a dedicated routing table. Gateway groups
update the selected next hop only after health state passes anti-flap rules.

The implementation parses portable text output from `ip` where target systems
cannot be assumed to provide JSON output. Failed route operations return a
bounded, sanitized stderr detail to the API caller.

## NAT and firewall

NAT supports masquerade and SNAT for explicit sources, subnets, or alias-backed
sets. Rules use check-before-add behavior so restart and restore are idempotent.
Tunnel-generated forwarding rules use `iptables-nft` and cover the directions
required by the configured role.

Firewall aliases compile into ipsets. Set refresh builds replacement state
before switching consumers, avoiding a partially updated policy. Trace
simulation explains matched rules without modifying live packet flow.

## Gateways

Gateway monitors use ICMP or HTTP probes. Multiple samples determine healthy,
degraded, or down state. Status-change callbacks reconcile dependent routes and
groups after a debounce interval. A down default path may use an explicit
blackhole route rather than silently escaping through the wrong uplink.

## HTTP API

Unauthenticated routes are registered before authentication middleware and are
limited to login/bootstrap compatibility, health, and one-time configuration
downloads. Administrative routes require a valid session or API token. JSON
responses use consistent error objects and do not expose peer private material
in collection endpoints.

The canonical endpoint reference is [API.md](API.md). It is also embedded in the
frontend and rendered by a viewer that always loads the English canonical file.

## Frontend

The frontend is embedded from `internal/frontend/www`. `app.js` owns Vue state,
polling, navigation, dashboard layout, modals, and remote-instance context.
`api.js` owns the HTTP client. `i18n.js` contains product translations, including
the intentionally retained Russian UI locale.

Desktop GridStack state is not overwritten by responsive list rendering. Mobile
navigation and standalone layout honor safe-area insets. Theme selection is
persisted as light, dark, or auto. A shared early/runtime applicator synchronizes
the root class, CSS `color-scheme`, Safari `theme-color`, page background fallback,
and standalone status-bar style before and after Vue mounts.

## Reverse proxy and certificates

Caddy is an optional publication layer, not part of the application core. It
terminates TLS, applies security headers, and forwards to Cascade. Certificate
automation is deployment-specific. The application remains usable behind any
reverse proxy that preserves required headers and streaming behavior.

## Releases and portability

Application update checks read the latest official GitHub release and are independent
from deployment. Installers prefer `CASCADE_RELEASE_BASE_URL`; the GitHub repository
setting is compatibility shorthand. Release bundles write an exact version tag into
their Compose files.

Reusable scripts run tests, image builds, and bundle creation without
GitHub-specific environment variables. GitHub Actions supplies repository,
registry, version, and credentials as a thin adapter.

## Key invariants

- Use database IDs as cache keys.
- Do not consume one-time links before successful config generation.
- Keep network command execution bounded.
- Restore networking state only after interfaces exist.
- Make NAT, routing, and firewall restoration idempotent.
- Never require upstream, GitHub, or GHCR in application logic.
- Keep secrets out of collection responses, logs, and release bundles.
- Update [API.md](API.md) with every API contract change.
- Keep repository documentation and code comments in English.
