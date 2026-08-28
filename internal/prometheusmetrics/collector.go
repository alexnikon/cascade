package prometheusmetrics

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/alexnikon/cascade/internal/gateway"
	internalmetrics "github.com/alexnikon/cascade/internal/metrics"
	"github.com/alexnikon/cascade/internal/tunnel"
)

// RuntimeProvider returns the tunnel manager's already-polled runtime cache.
type RuntimeProvider interface {
	RuntimeSnapshots() []tunnel.RuntimeInterfaceSnapshot
}

// GatewayProvider returns gateway monitor snapshots without starting probes.
type GatewayProvider interface {
	GetAllGatewaysWithStatus() ([]gateway.GatewayWithStatus, error)
}

// Database is the small database contract required by the collector.
type Database interface {
	PingContext(context.Context) error
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type Collector struct {
	runtime           RuntimeProvider
	gateways          GatewayProvider
	database          Database
	started           time.Time
	now               func() time.Time
	version           string
	commit            string
	threshold         time.Duration
	thresholdProvider func() time.Duration
	errors            atomic.Uint64

	desc map[string]*prometheus.Desc
}

func NewCollector(runtime RuntimeProvider, gateways GatewayProvider, database Database, version, commit string, threshold time.Duration) *Collector {
	if threshold <= 0 {
		threshold = defaultConnectedPeerThreshold
	}
	c := &Collector{runtime: runtime, gateways: gateways, database: database, started: time.Now(), now: time.Now, version: version, commit: commit, threshold: threshold, desc: map[string]*prometheus.Desc{}}
	c.initDescriptions()
	return c
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.desc {
		ch <- desc
	}
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	now := c.now()
	c.emit(ch, "build_info", prometheus.GaugeValue, 1, c.version, c.commit)
	c.emit(ch, "uptime_seconds", prometheus.GaugeValue, now.Sub(c.started).Seconds())

	databaseUp := 0.0
	groups := map[string]string{}
	counts := map[string]float64{}
	if c.database != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := c.database.PingContext(ctx); err == nil {
			databaseUp = 1
			var err error
			counts, groups, err = collectDatabaseMetadata(ctx, c.database)
			if err != nil {
				c.errors.Add(1)
			}
		} else {
			c.errors.Add(1)
		}
		cancel()
	}
	c.emit(ch, "database_up", prometheus.GaugeValue, databaseUp)
	for name, value := range counts {
		c.emit(ch, name, prometheus.GaugeValue, value)
	}

	system := internalmetrics.Current()
	status := internalmetrics.CurrentRuntime()
	if c.runtime != nil {
		for _, iface := range c.runtime.RuntimeSnapshots() {
			labels := []string{iface.ID}
			up := 0.0
			if iface.Enabled && status.StatusCommands[iface.ID].LastSuccess {
				up = 1
			}
			c.emit(ch, "interface_up", prometheus.GaugeValue, up, labels...)
			c.emit(ch, "interface_enabled", prometheus.GaugeValue, boolFloat(iface.Enabled), labels...)
			c.emit(ch, "interface_peers", prometheus.GaugeValue, float64(len(iface.Peers)), labels...)
			c.emit(ch, "interface_listen_port", prometheus.GaugeValue, float64(iface.ListenPort), labels...)
			c.emit(ch, "interface_info", prometheus.GaugeValue, 1, iface.ID, iface.Name, iface.Protocol)

			var rx, tx int64
			connected := 0
			for _, peer := range iface.Peers {
				rx += peer.TotalRx
				tx += peer.TotalTx
				peerLabels := []string{iface.ID, peer.ID, peer.Name}
				c.emit(ch, "peer_received_bytes_total", prometheus.CounterValue, nonNegative(peer.TotalRx), peerLabels...)
				c.emit(ch, "peer_sent_bytes_total", prometheus.CounterValue, nonNegative(peer.TotalTx), peerLabels...)
				c.emit(ch, "peer_enabled", prometheus.GaugeValue, boolFloat(peer.Enabled), peerLabels...)
				c.emit(ch, "peer_persistent_keepalive_seconds", prometheus.GaugeValue, float64(peer.PersistentKeepalive), peerLabels...)
				threshold := c.threshold
				if c.thresholdProvider != nil {
					threshold = c.thresholdProvider()
				}
				handshake, age, isConnected := handshakeValues(now, peer.LatestHandshakeAt, threshold)
				if isConnected && peer.Enabled && iface.Enabled {
					connected++
				}
				c.emit(ch, "peer_latest_handshake_timestamp_seconds", prometheus.GaugeValue, handshake, peerLabels...)
				c.emit(ch, "peer_handshake_age_seconds", prometheus.GaugeValue, age, peerLabels...)
				c.emit(ch, "peer_connected", prometheus.GaugeValue, boolFloat(isConnected && peer.Enabled && iface.Enabled), peerLabels...)
				c.emit(ch, "peer_info", prometheus.GaugeValue, 1, iface.ID, peer.ID, peer.Name, peer.AllowedIPs, groups[peer.GroupID])
			}
			c.emit(ch, "interface_peers_connected", prometheus.GaugeValue, float64(connected), labels...)
			c.emit(ch, "interface_received_bytes_total", prometheus.CounterValue, nonNegative(rx), labels...)
			c.emit(ch, "interface_sent_bytes_total", prometheus.CounterValue, nonNegative(tx), labels...)
			if system != nil {
				if netStat, ok := system.Net[iface.ID]; ok {
					c.emit(ch, "interface_rx_bits_per_second", prometheus.GaugeValue, netStat.RxMbps*1e6, labels...)
					c.emit(ch, "interface_tx_bits_per_second", prometheus.GaugeValue, netStat.TxMbps*1e6, labels...)
				}
			}
		}
	}

	if c.gateways != nil {
		gateways, err := c.gateways.GetAllGatewaysWithStatus()
		if err != nil {
			c.errors.Add(1)
		} else {
			for _, item := range gateways {
				statusValue := gatewayStatusValue(item.Status)
				c.emit(ch, "gateway_status", prometheus.GaugeValue, statusValue, item.ID, item.Name)
				if item.Latency != nil {
					c.emit(ch, "gateway_latency_seconds", prometheus.GaugeValue, float64(*item.Latency)/1000, item.ID, item.Name)
				}
				if item.PacketLoss != nil {
					c.emit(ch, "gateway_packet_loss_ratio", prometheus.GaugeValue, float64(*item.PacketLoss)/100, item.ID, item.Name)
				}
				c.emit(ch, "gateway_info", prometheus.GaugeValue, 1, item.ID, item.Name, item.Interface, item.MonitorRule, item.Status)
			}
		}
	}
	c.emit(ch, "metrics_collection_errors_total", prometheus.CounterValue, float64(c.errors.Load()))
	c.emit(ch, "metrics_last_collection_timestamp_seconds", prometheus.GaugeValue, float64(now.Unix()))
}

func (c *Collector) initDescriptions() {
	add := func(name, help string, labels ...string) {
		c.desc[name] = prometheus.NewDesc("cascade_"+name, help, labels, nil)
	}
	add("build_info", "Cascade build information.", "version", "commit")
	add("uptime_seconds", "Seconds since the Cascade process started.")
	add("database_up", "Whether the Cascade configuration database is reachable.")
	for _, name := range []string{"interfaces", "peers", "gateways", "gateway_groups", "routes", "nat_rules", "firewall_rules", "aliases", "client_groups", "remote_servers"} {
		add(name, "Current number of managed "+name+".")
	}
	for _, name := range []string{"interface_up", "interface_enabled", "interface_peers", "interface_peers_connected", "interface_received_bytes_total", "interface_sent_bytes_total", "interface_rx_bits_per_second", "interface_tx_bits_per_second", "interface_listen_port"} {
		add(name, "Cascade interface metric: "+name+".", "interface")
	}
	add("interface_info", "Stable Cascade interface metadata.", "interface", "name", "protocol")
	for _, name := range []string{"peer_received_bytes_total", "peer_sent_bytes_total", "peer_latest_handshake_timestamp_seconds", "peer_handshake_age_seconds", "peer_connected", "peer_enabled", "peer_persistent_keepalive_seconds"} {
		add(name, "Cascade peer metric: "+name+".", "interface", "peer_id", "name")
	}
	add("peer_info", "Stable Cascade peer metadata.", "interface", "peer_id", "name", "allowed_ip", "client_group")
	for _, name := range []string{"gateway_status", "gateway_latency_seconds", "gateway_packet_loss_ratio"} {
		add(name, "Cascade gateway metric: "+name+".", "gateway_id", "gateway")
	}
	add("gateway_info", "Stable gateway metadata and current enumerated state.", "gateway_id", "gateway", "interface", "monitor_type", "state")
	add("metrics_collection_errors_total", "Total Prometheus collection subsystem errors.")
	add("metrics_last_collection_timestamp_seconds", "Unix timestamp of the latest Prometheus collection.")
}

func (c *Collector) emit(ch chan<- prometheus.Metric, name string, valueType prometheus.ValueType, value float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(c.desc[name], valueType, value, labels...)
}
func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
func nonNegative(value int64) float64 {
	if value < 0 {
		return 0
	}
	return float64(value)
}

func handshakeValues(now time.Time, raw *string, threshold time.Duration) (float64, float64, bool) {
	if raw == nil {
		return 0, 0, false
	}
	parsed, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return 0, 0, false
	}
	age := now.Sub(parsed).Seconds()
	if age < 0 {
		age = 0
	}
	return float64(parsed.Unix()), age, age <= threshold.Seconds()
}

func gatewayStatusValue(status string) float64 {
	switch status {
	case "healthy":
		return 1
	case "degraded":
		return 0.5
	default:
		return 0
	}
}

func collectDatabaseMetadata(ctx context.Context, database Database) (map[string]float64, map[string]string, error) {
	row := database.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM interfaces), (SELECT COUNT(*) FROM peers),
		(SELECT COUNT(*) FROM gateways), (SELECT COUNT(*) FROM gateway_groups),
		(SELECT COUNT(*) FROM routes), ((SELECT COUNT(*) FROM nat_rules) + (SELECT COUNT(*) FROM nat_dnat_rules)),
		(SELECT COUNT(*) FROM firewall_rules), (SELECT COUNT(*) FROM aliases),
		(SELECT COUNT(*) FROM remotes)`)
	var interfaces, peers, gateways, gatewayGroups, routes, natRules, firewallRules, aliases, remotes int
	if err := row.Scan(&interfaces, &peers, &gateways, &gatewayGroups, &routes, &natRules, &firewallRules, &aliases, &remotes); err != nil {
		return nil, nil, err
	}
	counts := map[string]float64{
		"interfaces": float64(interfaces), "peers": float64(peers), "gateways": float64(gateways),
		"gateway_groups": float64(gatewayGroups), "routes": float64(routes), "nat_rules": float64(natRules),
		"firewall_rules": float64(firewallRules), "aliases": float64(aliases), "remote_servers": float64(remotes),
	}
	rows, err := database.QueryContext(ctx, "SELECT id, name FROM aliases WHERE type = 'client-group'")
	if err != nil {
		return counts, nil, err
	}
	defer rows.Close()
	groups := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return counts, nil, err
		}
		groups[id] = name
	}
	counts["client_groups"] = float64(len(groups))
	return counts, groups, rows.Err()
}
