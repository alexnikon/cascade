package prometheusmetrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/gateway"
	internalmetrics "github.com/alexnikon/cascade/internal/metrics"
	"github.com/alexnikon/cascade/internal/tunnel"
)

type fakeRuntime struct {
	snapshots []tunnel.RuntimeInterfaceSnapshot
}

func (f fakeRuntime) RuntimeSnapshots() []tunnel.RuntimeInterfaceSnapshot { return f.snapshots }

type fakeGateways struct {
	items []gateway.GatewayWithStatus
	err   error
}

func (f fakeGateways) GetAllGatewaysWithStatus() ([]gateway.GatewayWithStatus, error) {
	return f.items, f.err
}

type downDatabase struct{}

func (downDatabase) PingContext(context.Context) error                        { return errors.New("down") }
func (downDatabase) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }
func (downDatabase) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("down")
}

func TestCollectorExportsCachedRuntimeGatewayAndCounts(t *testing.T) {
	db.Close()
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.DB().Exec(`INSERT INTO interfaces (id, name, protocol, enabled, private_key, public_key) VALUES ('wg10', 'VPN', 'amneziawg-3.1', 1, 'secret-private', 'interface-public')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`INSERT INTO aliases (id, name, type) VALUES ('group-1', 'Family', 'client-group')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`INSERT INTO peers (id, interface_id, name, public_key, private_key, preshared_key, group_id) VALUES ('peer-1', 'wg10', 'Phone', 'peer-public', 'secret-private', 'secret-psk', 'group-1')`); err != nil {
		t.Fatal(err)
	}
	internalmetrics.RecordStatusCommand("wg10", time.Millisecond, true)

	handshake := "2026-08-28T09:58:00Z"
	runtime := fakeRuntime{snapshots: []tunnel.RuntimeInterfaceSnapshot{{
		ID: "wg10", Name: "VPN", Protocol: "amneziawg-3.1", ListenPort: 51820, Enabled: true,
		Peers: []tunnel.RuntimePeerSnapshot{{ID: "peer-1", Name: "Phone", AllowedIPs: "10.8.0.2/32", GroupID: "group-1", PersistentKeepalive: 25, Enabled: true, TotalRx: 123, TotalTx: 456, LatestHandshakeAt: &handshake}},
	}}}
	latency, loss := 42, 10
	gateways := fakeGateways{items: []gateway.GatewayWithStatus{{Gateway: gateway.Gateway{ID: "gw-1", Name: "Finland", Interface: "eth0", MonitorRule: "icmp_only"}, MonitorStatus: gateway.MonitorStatus{Status: "healthy", Latency: &latency, PacketLoss: &loss}}}}
	collector := NewCollector(runtime, gateways, db.DB(), "v1.0.0", "abc123", 180*time.Second)
	collector.now = func() time.Time { return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC) }
	collector.started = collector.now().Add(-time.Hour)

	body := gatherText(t, collector)
	for _, expected := range []string{
		`cascade_build_info{commit="abc123",version="v1.0.0"} 1`,
		`cascade_database_up 1`,
		`cascade_interfaces 1`,
		`cascade_client_groups 1`,
		`cascade_peer_received_bytes_total{interface="wg10",name="Phone",peer_id="peer-1"} 123`,
		`cascade_peer_sent_bytes_total{interface="wg10",name="Phone",peer_id="peer-1"} 456`,
		`cascade_peer_latest_handshake_timestamp_seconds{interface="wg10",name="Phone",peer_id="peer-1"} 1.78791108e+09`,
		`cascade_peer_handshake_age_seconds{interface="wg10",name="Phone",peer_id="peer-1"} 120`,
		`cascade_peer_connected{interface="wg10",name="Phone",peer_id="peer-1"} 1`,
		`cascade_peer_info{allowed_ip="10.8.0.2/32",client_group="Family",interface="wg10",name="Phone",peer_id="peer-1"} 1`,
		`cascade_interface_up{interface="wg10"} 1`,
		`cascade_peers 1`,
		`cascade_gateway_status{gateway="Finland",gateway_id="gw-1"} 1`,
		`cascade_gateway_latency_seconds{gateway="Finland",gateway_id="gw-1"} 0.042`,
		`cascade_gateway_packet_loss_ratio{gateway="Finland",gateway_id="gw-1"} 0.1`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q\n%s", expected, body)
		}
	}
	for _, secret := range []string{"secret-private", "secret-psk", "interface-public", "peer-public"} {
		if strings.Contains(body, secret) {
			t.Errorf("metrics leaked %q", secret)
		}
	}
}

func TestCollectorHandlesUnavailableSourcesAndDisconnectedPeer(t *testing.T) {
	handshake := "2026-08-28T09:00:00Z"
	collector := NewCollector(fakeRuntime{[]tunnel.RuntimeInterfaceSnapshot{{ID: "wg10", Enabled: true, Peers: []tunnel.RuntimePeerSnapshot{{ID: "p", Enabled: true, LatestHandshakeAt: &handshake}}}}}, fakeGateways{err: errors.New("unavailable")}, downDatabase{}, "dev", "unknown", 180*time.Second)
	collector.now = func() time.Time { return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC) }
	body := gatherText(t, collector)
	if !strings.Contains(body, "cascade_database_up 0") {
		t.Fatal(body)
	}
	if !strings.Contains(body, `cascade_peer_connected{interface="wg10",name="",peer_id="p"} 0`) {
		t.Fatal(body)
	}
	if !strings.Contains(body, "cascade_metrics_collection_errors_total 2") {
		t.Fatal(body)
	}
}

func TestCollectorUsesDynamicConnectedPeerThreshold(t *testing.T) {
	handshake := "2026-08-28T09:58:00Z"
	collector := NewCollector(fakeRuntime{[]tunnel.RuntimeInterfaceSnapshot{{
		ID: "wg10", Enabled: true,
		Peers: []tunnel.RuntimePeerSnapshot{{ID: "p", Enabled: true, LatestHandshakeAt: &handshake}},
	}}}, nil, nil, "dev", "unknown", time.Minute)
	collector.now = func() time.Time { return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC) }
	threshold := time.Minute
	collector.thresholdProvider = func() time.Duration { return threshold }
	if body := gatherText(t, collector); !strings.Contains(body, `cascade_peer_connected{interface="wg10",name="",peer_id="p"} 0`) {
		t.Fatal(body)
	}
	threshold = 3 * time.Minute
	if body := gatherText(t, collector); !strings.Contains(body, `cascade_peer_connected{interface="wg10",name="",peer_id="p"} 1`) {
		t.Fatal(body)
	}
}

func TestServerEnabledDisabledAndToken(t *testing.T) {
	collector := NewCollector(nil, nil, downDatabase{}, "dev", "unknown", time.Minute)
	database, err := sql.Open("sqlite", t.TempDir()+"/settings.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(database, Config{ConnectedPeerThreshold: time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, collector)
	t.Cleanup(func() { _ = server.Shutdown() })
	port := availablePort(t)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	request := func(path, auth string) int {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		req.Header.Set("Authorization", auth)
		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := request("/metrics", ""); got != 0 {
		t.Fatalf("disabled status=%d", got)
	}

	if _, err := server.Apply(Update{Enabled: true, Port: port, ConnectedPeerThresholdSeconds: 60, HistoryEnabled: true, Token: "token"}); err != nil {
		t.Fatal(err)
	}
	if got := request("/metrics", ""); got != fiber.StatusUnauthorized {
		t.Fatalf("missing token status=%d", got)
	}
	if got := request("/metrics", "Bearer token"); got != fiber.StatusOK {
		t.Fatalf("valid token status=%d", got)
	}
	if got := request("/other", "Bearer token"); got != fiber.StatusNotFound {
		t.Fatalf("unexpected path status=%d", got)
	}
	if _, err := server.Apply(Update{Enabled: false, Port: port, ConnectedPeerThresholdSeconds: 60, HistoryEnabled: true, ClearToken: true}); err != nil {
		t.Fatal(err)
	}
	if got := request("/metrics", ""); got != 0 {
		t.Fatalf("disabled status=%d", got)
	}
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func gatherText(t *testing.T, collector prometheus.Collector) string {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	recorder := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	return recorder.Body.String()
}
