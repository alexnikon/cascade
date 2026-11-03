package prometheusmetrics

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func TestPublicHTTPSAuthModeAndProxy(t *testing.T) {
	manager, err := NewManager(newSettingsDatabase(t), Config{HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, NewCollector(nil, nil, nil, "test", "test", time.Minute))
	server.ConfigureProxy("127.0.0.1", "host")
	// App.Test uses 0.0.0.0 as its transport peer; verify remote rejection below.
	server.proxyPeer = func() (netip.Addr, error) { return netip.MustParseAddr("0.0.0.0"), nil }
	app := fiber.New()
	app.Get(InternalPath, server.PublicHandler)
	update := Update{Enabled: true, Mode: ModePublicHTTPS, Port: 9351, Token: "metrics-secret", HistoryEnabled: true}
	if _, err := server.Apply(Update{Enabled: true, Mode: ModePublicHTTPS, Port: 9351}); err == nil {
		t.Fatal("accepted tokenless HTTPS")
	}
	if manager.Current().Enabled {
		t.Fatal("failed update changed state")
	}
	if _, err := server.Apply(update); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		auth, proxy, port string
		status            int
	}{
		{"Bearer metrics-secret", "1", "443", 200},
		{"", "1", "443", 401},
		{"Bearer wrong", "1", "443", 401},
		{"metrics-secret", "1", "443", 401},
		{"Bearer ws_api-token", "1", "443", 401},
		{"Bearer metrics-secret", "", "443", 404},
		{"Bearer metrics-secret", "1", "80", 404},
	} {
		req := httptest.NewRequest("GET", InternalPath, nil)
		req.Header.Set("Authorization", tc.auth)
		req.Header.Set("X-Cascade-Metrics-Proxy", tc.proxy)
		req.Header.Set("X-Cascade-HTTPS-Port", tc.port)
		req.Header.Set("Cookie", "session=admin")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("%+v: status %d", tc, resp.StatusCode)
		}
	}
	update.Token = ""
	update.ClearToken = true
	if _, err := server.Apply(update); err == nil {
		t.Fatal("removed required token")
	}
	if !manager.Authorize("metrics-secret") {
		t.Fatal("failed removal changed token")
	}
	update.ClearToken = false
	update.Token = "new-secret"
	if _, err := server.Apply(update); err != nil {
		t.Fatal(err)
	}
	if manager.Authorize("metrics-secret") || !manager.Authorize("new-secret") {
		t.Fatal("token rotation failed")
	}
	req := httptest.NewRequest("GET", InternalPath, nil)
	req.Header.Set("Authorization", "Bearer new-secret")
	req.Header.Set("X-Cascade-Metrics-Proxy", "1")
	req.Header.Set("X-Cascade-HTTPS-Port", "443")
	server.proxyPeer = func() (netip.Addr, error) { return netip.MustParseAddr("127.0.0.1"), nil }
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("spoofed proxy headers bypassed transport peer check")
	}
	update.Enabled = false
	update.Token = ""
	if _, err := server.Apply(update); err != nil {
		t.Fatal(err)
	}
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("disabled public endpoint remains available")
	}
}

func TestBridgeGatewayAndUnsafeBackend(t *testing.T) {
	routes := "Iface Destination Gateway Flags RefCnt Use Metric Mask\nwg0 00000000 0100090A 0003 0 0 0 00000000\neth0 00000000 010012AC 0003 0 0 0 00000000\n"
	ip, err := bridgeGateway(routes)
	if err != nil || ip.String() != "172.18.0.1" {
		t.Fatalf("gateway=%s err=%v", ip, err)
	}
	if _, err := bridgeGateway(strings.ReplaceAll(routes, "eth0", "wg1")); err == nil {
		t.Fatal("trusted VPN gateway as Caddy")
	}
	for _, bind := range []string{"", "0.0.0.0", "10.9.0.1", "::"} {
		if _, err := publicProxyPeer(bind, "host"); err == nil {
			t.Fatalf("accepted unsafe backend bind %q", bind)
		}
	}
}

func TestVPNReconcileAndSwitchModes(t *testing.T) {
	manager, err := NewManager(newSettingsDatabase(t), Config{HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, NewCollector(nil, nil, nil, "test", "test", time.Minute))
	server.ConfigureProxy("127.0.0.1", "host")
	defer server.Shutdown()
	address := "127.0.0.1"
	server.resolveVPN = func(string) (string, error) {
		if address == "" {
			return "", errors.New("interface is down")
		}
		return address, nil
	}
	update := Update{Enabled: true, Mode: ModeVPN, InterfaceID: "wg0", Port: availablePort(t), HistoryEnabled: true}
	if _, err := server.Apply(update); err != nil {
		t.Fatal(err)
	}
	reconcile := func() { server.mu.Lock(); defer server.mu.Unlock(); server.reconcileLocked() }
	request := func(host string) bool {
		client := http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := client.Get("http://" + host + ":" + strconv.Itoa(update.Port) + Path)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	if !request(address) {
		t.Fatal("VPN listener unavailable")
	}
	address = ""
	reconcile()
	if listening, err := server.Status(); listening || err == "" {
		t.Fatal("missing interface did not stop listener")
	}
	address = "127.0.0.1"
	reconcile()
	if !request(address) {
		t.Fatal("VPN listener did not recover")
	}
	update.Mode = ModePublicHTTPS
	update.Token = "secret"
	if _, err := server.Apply(update); err != nil {
		t.Fatal(err)
	}
	if request(address) {
		t.Fatal("HTTPS left old HTTP listener open")
	}
	update.Mode = ModeVPN
	update.Token = ""
	update.ClearToken = true
	if _, err := server.Apply(update); err != nil {
		t.Fatal(err)
	}
	if !request(address) {
		t.Fatal("switch back to VPN failed")
	}
}
