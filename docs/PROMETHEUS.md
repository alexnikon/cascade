# Prometheus monitoring

Cascade provides an optional native Prometheus endpoint. It consumes Cascade's
existing tunnel poller, in-memory peer state, network-rate collector, gateway
monitor, and configuration database. A Prometheus scrape does not run `wg` or
`awg`, access Docker, or read Cascade's SQLite files from another process.

## Configuration

Administrators configure metrics in **Settings → Metrics**. Changes to the
endpoint state, port, token, connected-peer threshold, and local history apply
immediately without restarting Cascade.

On the first startup after metrics settings are introduced, Cascade imports the
following environment variables into its configuration database:

```dotenv
METRICS_ENABLED=true
METRICS_CONNECTED_PEER_THRESHOLD=180s
METRICS_TOKEN=replace-with-a-long-random-token
METRICS_HISTORY_ENABLED=true
```

| Variable | Default | Behavior |
|----------|---------|----------|
| `METRICS_ENABLED` | `false` | Initial endpoint state |
| `METRICS_CONNECTED_PEER_THRESHOLD` | `180s` | Initial positive Go duration used by `cascade_peer_connected` |
| `METRICS_TOKEN` | unset | Initial optional bearer token |
| `METRICS_HISTORY_ENABLED` | `true` | Initial local metrics-history state |

Environment variables are bootstrap values only. After the first import,
Settings and SQLite are authoritative and later environment changes are
ignored. Cascade stores only a SHA-256 token hash and never returns the token
through the API. Leaving the token field blank preserves an existing token;
use **Remove token** to clear it explicitly.

The endpoint always uses `/metrics` on a dedicated listener bound to all network
interfaces. Its port is configured in Settings and defaults to `9351`, so the
default URL is `http://SERVER:9351/metrics`. It is independent of the Web UI
port, Caddy, and `ADMIN_PATH`. If `METRICS_TOKEN` is unset, restrict access to
the port using a trusted VPN or firewall. Do not expose it directly to the
public Internet.

Host-network and isolated deployments need no Docker port mapping. A bridge
deployment must explicitly publish the selected TCP port (for example,
`9351:9351`); changing the setting cannot change Docker's published ports.

The output excludes private keys, preshared keys, credentials, API tokens,
configuration secrets, runtime endpoints, and public keys. Peer IDs and names,
allowed IPs, group names, interface metadata, and gateway names are operational
metadata and should still be treated as internal information.

## Prometheus configuration

```yaml
scrape_configs:
  - job_name: cascade
    authorization:
      type: Bearer
      credentials: replace-with-a-long-random-token
    static_configs:
      - targets:
          - 10.8.0.1:9351
```

For multiple Cascade servers, add all targets to the job. Prometheus's standard
`instance` label identifies each server; Cascade does not add a redundant
`server` label. Friendly names can be attached as target labels or with
relabeling.

## Grafana dashboard

The importable dashboard is stored at
[`grafana/cascade-dashboard.json`](../grafana/cascade-dashboard.json). In
Grafana, select **Dashboards → New → Import**, upload the JSON file, and map the
`DS_PROMETHEUS` input to the Prometheus datasource that scrapes Cascade.

The dashboard supports multiple Cascade targets through the standard Prometheus
`instance` label. Its variables are hierarchical: selecting an instance limits
the available interfaces, peers, and gateways. All selectors support multiple
values and default to **All**.

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

Traffic values are lifetime totals maintained by Cascade across interface
restarts and are exposed as Prometheus counters from snapshots.

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
the **Connected peer threshold** configured in Settings. A missing, invalid, future-only, or older
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
