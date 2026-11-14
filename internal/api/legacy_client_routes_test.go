package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/alexnikon/cascade/internal/settings"
	"github.com/alexnikon/cascade/internal/tunnel"
)

// Run with CASCADE_CLIENT_ROUTES_TEST=1 only in a disposable Linux container.
// This test follows import_client_configs_test.go, whose fixtures use the original TestMain database.
func TestLegacyClientRoutingRequestsAndBackupsUseManualAllowedIPs(t *testing.T) {
	if os.Getenv("CASCADE_CLIENT_ROUTES_TEST") != "1" || runtime.GOOS != "linux" {
		t.Skip("requires a disposable Linux environment with writable WireGuard configuration storage")
	}
	if _, err := exec.LookPath("wg"); err != nil {
		t.Skip("requires wireguard-tools for interface key generation")
	}
	sta := newSettingsTestApp(t)
	ta := &testApp{app: sta.app, adminToken: sta.token}
	RegisterInterfaces(sta.app.Group("/api", AuthMiddleware))
	RegisterPeers(sta.app.Group("/api", AuthMiddleware))
	iface, err := mgr().CreateInterface(tunnel.CreateInput{Protocol: "wireguard-1.0", Address: "10.201.0.1/24"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr().DeleteInterface(iface.ID) })
	base := "/api/tunnel-interfaces/" + iface.ID
	if _, err := settings.UpdateSettings(map[string]any{"defaultClientAllowedIPs": "198.51.100.0/24"}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, payload any, status int) map[string]any {
		t.Helper()
		resp := ta.do(method, path, sta.token, payload)
		body := decodeBody(resp)
		if resp.StatusCode != status {
			t.Fatalf("%s %s: status=%d body=%v", method, path, resp.StatusCode, body)
		}
		return body
	}
	assertManual := func(body map[string]any, want string) {
		t.Helper()
		if body["clientAllowedIPs"] != want {
			t.Fatalf("manual list lost: %v", body)
		}
		for _, key := range []string{"clientAllowedIPsMode", "clientAllowedIPsAliasId"} {
			if _, exists := body[key]; exists {
				t.Fatalf("retired field returned: %s", key)
			}
		}
	}
	created := request(http.MethodPost, base+"/peers", map[string]any{
		"name": "Legacy request", "publicKey": peerAPublicKey, "privateKey": peerAPrivateKey,
		"allowedIPs": "10.201.0.2/32", "clientAllowedIPsMode": "exclude-ipset", "clientAllowedIPsAliasId": "missing",
	}, http.StatusCreated)["peer"].(map[string]any)
	assertManual(created, "198.51.100.0/24")
	id := created["id"].(string)
	updated := request(http.MethodPatch, base+"/peers/"+id, map[string]any{
		"clientAllowedIPs": "203.0.113.0/24", "clientAllowedIPsMode": 42, "clientAllowedIPsAliasId": nil,
	}, http.StatusOK)
	assertManual(updated, "203.0.113.0/24")

	// Restore an old backup referencing an alias that no longer exists.
	legacy := map[string]any{
		"name": "Restored manual routes", "publicKey": peerAPublicKey, "privateKey": peerAPrivateKey,
		"allowedIPs": "10.201.0.2/32", "clientAllowedIPs": "192.0.2.0/24",
		"clientAllowedIPsMode": "exclude-ipset", "clientAllowedIPsAliasId": "missing",
	}
	request(http.MethodPut, base+"/restore", map[string]any{"file": map[string]any{"peers": []any{legacy}}}, http.StatusOK)
	peers := request(http.MethodGet, base+"/peers", nil, http.StatusOK)["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("restored peers=%v", peers)
	}
	restored := peers[0].(map[string]any)
	assertManual(restored, "192.0.2.0/24")
	restoredID := restored["id"].(string)
	resp := ta.do(http.MethodGet, base+"/peers/"+restoredID+"/config", sta.token, nil)
	config, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(config), "AllowedIPs = 192.0.2.0/24\n") || !strings.Contains(string(config), "PrivateKey = "+peerAPrivateKey) {
		t.Fatalf("restored config=%s status=%d err=%v", config, resp.StatusCode, err)
	}
	for _, path := range []string{"/backup", "/export"} {
		backup := request(http.MethodGet, base+path, nil, http.StatusOK)
		assertManual(backup["peers"].([]any)[0].(map[string]any), "192.0.2.0/24")
	}
	// Native interface import has a separate backup type; it must also ignore old fields.
	raw, err := json.Marshal(map[string]any{
		"interface": map[string]any{"address": "10.202.0.1/24", "protocol": "wireguard-1.0", "privateKey": peerAPrivateKey, "publicKey": peerAPublicKey},
		"peers":     []any{legacy},
	})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := mgr().ImportInterface(tunnel.ImportInterfaceInput{RawJSON: string(raw), ListenPort: 52199})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr().DeleteInterface(imported.Interface.ID) })
	if imported.PeersCreated != 1 || len(imported.PeersFailed) != 0 {
		t.Fatalf("native import: %+v", imported)
	}
	assertManual(request(http.MethodGet, "/api/tunnel-interfaces/"+imported.Interface.ID+"/peers", nil, http.StatusOK)["peers"].([]any)[0].(map[string]any), "192.0.2.0/24")
}
