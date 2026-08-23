# Performance baseline and data-plane gate

This fork keeps Cascade, its UI, REST API, SQLite database, and AmneziaWG lifecycle in one container until measurements prove that `amneziawg-go` is the limiting resource. AWG protocol changes and a data-plane sidecar are intentionally outside this phase.

## Implemented controls

- The backend status collector uses `STATUS_POLL_INTERVAL` with a three-second default. Values from one second through one minute are accepted.
- HTTP requests use cached interface and peer state; status collection remains in the background.
- `GET /api/peers` returns sanitized cached peers for every interface in one response.
- The UI uses one five-second scheduler, pauses it while the tab is hidden, refreshes immediately when visible, and prevents overlapping refreshes.
- Monitoring history can be disabled with `METRICS_HISTORY_ENABLED=false`. Live metrics remain available.
- `/api/metrics` exposes bounded HTTP latency buckets, response bytes, status command duration/errors, reconcile errors, snapshot age, SQLite wait statistics, and Cascade/`amneziawg-go` CPU and RSS on Linux.
- Peer lifetime counters remain buffered and are persisted once per minute; status collection does not persist RX/TX on every pass.

The frontend skipped-refresh counter is process-local browser state and is attached to the live metrics snapshot as `frontend.resourcePollSkipped`. It is not persisted.

The existing WGBot routes remain unchanged: health, interface list/detail, per-interface peer list/create/update/delete, enable/disable, expiry fields, client groups, and config download. The aggregate route is additive and is used only by the Cascade dashboard.

## Relevant upstream issue inventory

| Issue | Scope | Resolution in this phase |
|---|---|---|
| #99, one-time link config failure and duplicated peer cache entries | WGBot-compatible peer/config lifecycle | Regression test plus cache-key normalization; one-time config generation reloads the authoritative SQLite row before consuming the token. |
| #58, Diagnostics becomes slow with longer history | UI responsiveness | Hidden-tab pause, active-page-only history refresh, parallel graph fetches, and non-overlapping history refresh. Browser benchmarking is still required. |
| #56, optional metrics collection | Disk and SQLite background work | `METRICS_HISTORY_ENABLED=false` disables history writes and history reads while preserving live metrics. |
| #98, profile Save and Exit | Report appears tied to AWG profile work not reproducible on the tracked master code | No speculative change. Re-evaluate when AWG3 becomes an approved phase. |

Other open feature requests are not part of the performance or WGBot contract scope.

## Reproducible load test

Use a dedicated server restored from sanitized production fixtures. Run each matrix cell with the production peer count and again with twice that count:

| VPN load | API scenario |
|---|---|
| idle | Aggregate peers and dashboard navigation |
| 50% of target throughput | Aggregate peers and peer lifecycle actions |
| 80% of target throughput | Aggregate peers, dashboard navigation, and lifecycle actions |

Generate VPN load with the existing throughput harness or `iperf3` through the tunnel. Keep the stream count, packet size, direction, CPU count, and target rate identical between the original image and this fork.

Build the API benchmark locally:

```bash
go build -o /tmp/cascade-bench ./cmd/cascade-bench
CASCADE_API_TOKEN='ws_...' /tmp/cascade-bench \
  -base-url 'https://server.example/secret-path/api' \
  -path /peers -duration 5m -concurrency 4
```

Before and after every run, save `/api/metrics`, the benchmark JSON, `iperf3 --json`, peer/interface counts, image digest, commit SHA, vCPU count, and server type. Do not put API tokens into result files.

The no-sidecar gate passes only when all of these hold:

- peer/dashboard API p95 is at most 500 ms;
- browser navigation and peer actions complete within one second;
- API p95 at 80% VPN load is no more than 20% above idle;
- throughput and jitter do not regress from the original Cascade image;
- there is no growing request queue, SQLite lock error, or missed lifecycle operation.

Use one and multiple tabs, a hidden tab, an expired session, multiple interfaces, and delayed/error API responses in the browser pass. Verify from runtime counters that UI polling does not trigger extra `awg show` calls.

## Sidecar decision

Implement a data-plane sidecar only when the UI/API gate still fails, `amneziawg-go` consistently consumes most available CPU, API latency correlates with VPN load, and profiling excludes the UI, SQLite, and other control-plane work. Container separation alone does not isolate CPU.

## Image and rollback policy

CI publishes `latest` and an immutable commit-SHA tag for every `master` build. Version tags are also published for `v*` releases. Deployment Compose files use an exact release tag:

```yaml
image: ghcr.io/alexnikon/cascade:X.Y.Z
```

Keep the previous tag and a backup of the SQLite/config data directory for rollback. This phase adds no database migration. Deployment, image publication, and production changes require separate approval.

The local `upstream-master` branch tracks the original repository. Feature work belongs on `codex/*` branches; update the tracking branch before rebasing or merging upstream changes.
