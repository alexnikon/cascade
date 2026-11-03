# Prometheus monitoring

Cascade provides an optional native Prometheus endpoint. It consumes Cascade's
existing tunnel poller, in-memory peer state, network-rate collector, gateway
monitor, and configuration database. A Prometheus scrape does not run `wg` or
`awg`, access Docker, or read Cascade's SQLite files from another process.

## Configuration

Administrators configure metrics in **Settings → Metrics**. Select exactly one
access mode, then enable the endpoint:

- **VPN interface**: select a Cascade interface and TCP port (default `9351`).
  The HTTP listener binds only to that interface's configured address. An optional
  bearer token adds authentication within the VPN. If the interface stops, is
  removed, or loses its address, access stops. Cascade automatically reconciles
  the listener when the selected interface becomes available again.
- **Public HTTPS**: scrape `https://PUBLIC_IP/metrics` (or the installed TLS
  hostname) on port `443`. This uses the existing Caddy certificate and requires
  a bearer token. No separate metrics TCP port is opened. It works independently
  of VPN-only access restrictions on the administrative panel.

Changes apply without restarting Cascade, Caddy, or the VPN. The local metrics
history setting is independent of Prometheus access.

**Upgrade behavior:** existing endpoints are disabled once when access modes are
introduced. Select a mode and enable scraping again. The token, port, and local
history are preserved. New installations also start with the endpoint disabled.
`METRICS_ENABLED` no longer enables an endpoint at bootstrap.

The initial optional token (`METRICS_TOKEN`) and local-history preference
(`METRICS_HISTORY_ENABLED`, default `true`) are imported only once. Settings and
SQLite are authoritative afterwards. Only a SHA-256 token hash is stored; the
API never returns the token. A blank token field preserves the current token.
An enabled Public HTTPS endpoint cannot have its token removed.

### Caddy and certificates

New deployment bundles include the `/metrics` route. Existing deployments must
also upgrade their Caddyfile; updating only the Cascade image does not add routes.
Review and apply the bundled helper from the runtime directory:

```bash
python3 deploy/update-web-access-proxy.py deploy/caddy/Caddyfile --check
python3 deploy/update-web-access-proxy.py deploy/caddy/Caddyfile
docker restart cascade-caddy
```

The helper preserves unrelated routes, rejects unrecognized metrics routes,
validates the proposed configuration inside the running Caddy container before
writing, and saves a backup. Review customized configurations manually.

Public HTTPS uses the existing acme.sh certificate issuance and renewal. Prometheus
must validate the certificate for the exact IP or hostname being scraped. If
installation fell back to a self-signed certificate, explicitly configure its
trusted CA/certificate or install a publicly trusted certificate. Do not use
`insecure_skip_verify: true`.

The standard host deployment requires a `127.0.0.1` management bind. The standard
bridge deployment publishes only the management port to host loopback; its Caddy
requests arrive from the container's Ethernet gateway. Do not expose the backend
management port publicly. Remove any old `9351:9351` Docker mapping; VPN access
uses the tunnel address inside the container and HTTPS uses the existing
management mapping. Custom networking requires a separate trust review.

### Prometheus configuration

For public collection:

```yaml
scrape_configs:
  - job_name: Cascade
    scheme: https
    metrics_path: /metrics
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/secrets/nlbox-cascade-token
    static_configs:
      - targets: ["PUBLIC_IP:443"]
        labels:
          instance: nlbox
```

Mount the token file read-only into Prometheus. Use a separate scrape job or
credential configuration for servers with different tokens. Keep existing
`job` and `instance` labels when replacing a target to preserve dashboard history.
Prometheus must route the public target through ordinary Internet access, without
a dependency on the VPN being monitored.

For VPN collection, use `scheme: http` and the selected VPN address and port,
for example `10.9.0.1:9351`. Omit `authorization` only if no token is configured.

The output excludes private keys, preshared keys, credentials, API tokens,
configuration secrets, runtime endpoints, and public keys. Peer IDs and names,
allowed IPs, groups, and interface/gateway metadata are internal information.

## Grafana dashboard

The importable dashboard is stored at
[`grafana/cascade-dashboard.json`](../grafana/cascade-dashboard.json). In
Grafana, select **Dashboards → New → Import**, upload the JSON file, and map the
`DS_PROMETHEUS` input to the Prometheus datasource that scrapes Cascade.

The dashboard supports multiple Cascade targets through the standard Prometheus
`instance` label. Its variables are hierarchical: selecting an instance limits
the available interfaces, peers, and gateways. The exported dashboard retains its saved selections; use **All** in the query
selectors to include every eligible value.

Every dashboard query uses a metric documented below. Gateway latency and packet
loss panels naturally remain empty when no gateway is monitored or when the
monitor has not produced those optional measurements yet. Interface throughput
panels appear after Cascade's system collector has produced its first network
snapshot. The dashboard deliberately has no node-exporter dependency and does
not expect CPU or memory metrics that the native endpoint does not expose.

The Traffic section includes outbound totals for the current calendar day and
calendar month. These panels use the available Prometheus samples and retention;
when scraping started after the calendar boundary, they show traffic from the
first available sample rather than reconstructing earlier traffic.

## Metrics

Traffic values are accumulated totals maintained by Cascade across interface
restarts and are exposed as Prometheus counters from snapshots. Manual peer
traffic resets lower the affected counters; historical queries use `increase()`
to handle resets observed between scrapes.

| Metric | Type | Labels |
|--------|------|--------|
| `cascade_build_info` | gauge | `version`, `commit` |
| `cascade_uptime_seconds` | gauge | none |
| `cascade_database_up` | gauge | none |
| `cascade_interface_up` | gauge | `interface` |
| `cascade_interface_enabled` | gauge | `interface` |
| `cascade_interface_peers` | gauge | `interface` |
| `cascade_interface_peers_connected` | gauge | `interface` |
| `cascade_interface_received_bytes_total` | counter | `interface` |
| `cascade_interface_sent_bytes_total` | counter | `interface` |
| `cascade_interface_rx_bits_per_second` | gauge | `interface` |
| `cascade_interface_tx_bits_per_second` | gauge | `interface` |
| `cascade_interface_listen_port` | gauge | `interface` |
| `cascade_interface_info` | gauge | `interface`, `name`, `protocol` |
| `cascade_interface_role_info` | gauge | `interface`, `role` |
| `cascade_peer_received_bytes_total` | counter | `interface`, `peer_id`, `name` |
| `cascade_peer_sent_bytes_total` | counter | `interface`, `peer_id`, `name` |
| `cascade_peer_latest_handshake_timestamp_seconds` | gauge | `interface`, `peer_id`, `name` |
| `cascade_peer_handshake_age_seconds` | gauge | `interface`, `peer_id`, `name` |
| `cascade_peer_connected` | gauge | `interface`, `peer_id`, `name` |
| `cascade_peer_enabled` | gauge | `interface`, `peer_id`, `name` |
| `cascade_peer_persistent_keepalive_seconds` | gauge | `interface`, `peer_id`, `name` |
| `cascade_peer_info` | gauge | `interface`, `peer_id`, `name`, `allowed_ip`, `client_group` |
| `cascade_gateway_status` | gauge | `gateway_id`, `gateway` |
| `cascade_gateway_latency_seconds` | gauge | `gateway_id`, `gateway` |
| `cascade_gateway_packet_loss_ratio` | gauge | `gateway_id`, `gateway` |
| `cascade_gateway_info` | gauge | `gateway_id`, `gateway`, `interface`, `monitor_type`, `state` |
| `cascade_interfaces` | gauge | none |
| `cascade_peers` | gauge | none |
| `cascade_gateways` | gauge | none |
| `cascade_gateway_groups` | gauge | none |
| `cascade_routes` | gauge | none |
| `cascade_nat_rules` | gauge | none |
| `cascade_firewall_rules` | gauge | none |
| `cascade_aliases` | gauge | none |
| `cascade_client_groups` | gauge | none |
| `cascade_remote_servers` | gauge | none |
| `cascade_metrics_collection_errors_total` | counter | none |
| `cascade_metrics_last_collection_timestamp_seconds` | gauge | none |

`cascade_interface_up` is `1` only when the interface is enabled and its most
recent shared runtime poll succeeded. `cascade_gateway_status` maps `healthy` to
`1`, `degraded` to `0.5`, and `down`, `admin_down`, or `unknown` to `0`; the exact
state is also present on `cascade_gateway_info`.

WireGuard is handshake-based and does not provide authoritative session state.
`cascade_peer_connected` is therefore a recency signal: it is `1` only when the
peer and interface are enabled and the most recent handshake is no older than
the fixed **180-second** connected-peer threshold. This is a recent-handshake
signal, not a reachability guarantee. The deprecated
`METRICS_CONNECTED_PEER_THRESHOLD` environment variable and API threshold input
are ignored. Raw handshake timestamps and ages remain available for custom
PromQL thresholds. A missing, invalid, future-only, or older
handshake produces `0`.

## Migration from awgexporter

The native endpoint intentionally does not emit legacy `awg_*` metrics. Keeping
them would preserve public-key and changing endpoint labels and would make the
new model less safe. Update dashboards and alerts as follows:

| Standalone metric | Native replacement |
|-------------------|--------------------|
| `awg_sent_bytes` | `cascade_peer_sent_bytes_total` |
| `awg_received_bytes` | `cascade_peer_received_bytes_total` |
| `awg_latest_handshake_seconds` | `cascade_peer_latest_handshake_timestamp_seconds` |
| `awg_peer_endpoint` | dropped; endpoint labels have unbounded mobile-client cardinality |
| `awg_peer_info` | `cascade_peer_info` and `cascade_peer_enabled` |
| `awg_clients_total` | `cascade_peers` (stable peer records; not unique display names) |
| `awg_peers_total` | `cascade_peers` |
| `awg_interface_status` | `cascade_interface_up` |
| `awg_status` | `cascade_database_up` plus `cascade_metrics_collection_errors_total` |

After Prometheus and dashboards use the native endpoint, remove the entire
`awgexporter` service from Compose. Also remove its Docker socket mount, Cascade
data/SQLite directory mount, host-network port `9351`, and exporter-only
variables (`HTTP_PORT`, `LISTEN_ADDR`, `SCRAPE_INTERVAL`, `CASCADE_DB`, and
`AWG_SHOW_TEMPLATE`). No replacement Docker socket or SQLite mount is needed.

Gateway failure and state-change counters are not exported yet because the
current gateway monitor retains sliding-window state but does not persist
monotonic event counters. Latency, packet loss, enumerated state, and status are
exported directly and reliably; persistent event counters can be added later at
the monitor ownership boundary without reconstructing them during scrapes.

## Monthly peer traffic and S2S visibility

**Top Peers by Traffic This Month** ranks the top 10 client peers by RX + TX
increases since `now/M`, capped by their current Cascade RX + TX totals. A manual
**Reset trafic** therefore clears usage from before the reset after the next
Prometheus scrape and panel refresh. Repeated resets use the same cap. Historical
Prometheus samples are retained. This panel always requires interface role
`client`, regardless of the S2S switch; S2S interfaces and targets without role
metadata are excluded. Upgrade older targets to display their clients.

The period uses the dashboard timezone (`browser` by default). An absolute
dashboard time range takes precedence over the month override, as for **Traffic
Month**; the cap still uses the counters at the query evaluation time. Available
history, scrape intervals, extrapolation, and resets missed between scrapes can
limit accuracy. This is not a billing record. Other traffic panels retain their
historical `increase()` behavior after manual resets.

The **S2S interfaces** variable defaults to **Show**. **Hide** excludes S2S
interfaces and their peers from interface/peer counts, other traffic panels,
rates, handshake panels, and the Interface/Peer selectors. Gateway health and server-wide panels
remain visible. A mixed interface containing any interconnect peer is treated as
S2S in its entirety.

Cascade emits `cascade_interface_role_info{interface,role="client|s2s"} 1`
from the shared runtime snapshot. An uplink or an interface with an interconnect
peer has role `s2s`; every other interface has role `client`. Existing traffic
labels are unchanged. The dashboard uses `unless on (instance, interface)`
before aggregation and ranking for panels controlled by the switch, so older
targets without role metadata remain visible there even in Hide mode. Upgrade
those targets to enable automatic hiding.


## Local integration validation

Run `bash scripts/test-public-metrics.sh` from a source checkout with a local
Docker engine. The disposable privileged fixture checks real Caddy TLS, bearer
authentication, host/bridge proxy isolation, VPN listener recovery and address
changes, and public scraping while the VPN is down. It does not use host
networking, a Docker socket mount, or production services.
