// Package api — HTTP-level tests for PUT /api/settings.
//
// Tests cover explicit validation added in the quick-create feature:
//   - portPool: invalid values return 400 with a message; valid values return 200
//   - subnetPool: host-bits-set and non-CIDR values return 400; valid CIDR returns 200
//   - Other fields (dns, routerName, …) pass through without 400
//
// These complement the unit tests in internal/settings/settings_test.go which
// cover ParsePortPool and isValidSettingValue at the model layer.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/prometheusmetrics"
	"github.com/alexnikon/cascade/internal/tokens"
	"github.com/alexnikon/cascade/internal/users"
)

// ── Harness ───────────────────────────────────────────────────────────────────

// settingsTestApp is a minimal Fiber application with the settings routes
// registered behind AuthMiddleware.  A single "owner" user is pre-created
// with a raw API token that can be passed to ta.do().
type settingsTestApp struct {
	app     *fiber.App
	token   string // raw Bearer token for the owner user
	metrics *prometheusmetrics.Manager
}

func newSettingsTestApp(t *testing.T) *settingsTestApp {
	t.Helper()

	dir, err := os.MkdirTemp("", "cascade-settings-api-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	if err := db.Init(dir); err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})

	InitAuth() // initialise session store

	owner, err := users.Create("owner", "ownerpass1")
	if err != nil {
		t.Fatalf("Create owner: %v", err)
	}
	_, rawToken, err := tokens.Create(owner.ID, "settings-test-token")
	if err != nil {
		t.Fatalf("tokens.Create: %v", err)
	}

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			msg := err.Error()
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
				msg = e.Message
			}
			return c.Status(code).JSON(fiber.Map{"error": msg})
		},
	})

	api := app.Group("/api", AuthMiddleware)
	RegisterSettings(api)
	metricsManager, err := prometheusmetrics.NewManager(db.DB(), prometheusmetrics.Config{Path: "/metrics", ConnectedPeerThreshold: 3 * time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	RegisterMetricsSettings(api, metricsManager)

	return &settingsTestApp{app: app, token: rawToken, metrics: metricsManager}
}

// put is a convenience wrapper: PUT /api/settings with JSON body.
func (sta *settingsTestApp) put(t *testing.T, body any) *http.Response {
	t.Helper()
	ta := &testApp{app: sta.app, adminToken: sta.token}
	return ta.do("PUT", "/api/settings", sta.token, body)
}

// ── portPool validation ───────────────────────────────────────────────────────

func TestPutSettings_PortPool_Valid_Returns200(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"portPool": "51831-51840"})
	if resp.StatusCode != http.StatusOK {
		body := decodeBody(resp)
		t.Errorf("valid portPool: expected 200, got %d; body=%v", resp.StatusCode, body)
	}
	body := decodeBody(resp)
	if body["portPool"] != "51831-51840" {
		t.Errorf("portPool in response = %v, want '51831-51840'", body["portPool"])
	}
}

func TestPutSettings_PortPool_PrivilegedPort_Returns200(t *testing.T) {
	// Ports 1–1023 must be accepted (Docker/root context).
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"portPool": "433-442"})
	if resp.StatusCode != http.StatusOK {
		body := decodeBody(resp)
		t.Errorf("privileged portPool 433-442: expected 200, got %d; body=%v", resp.StatusCode, body)
	}
}

func TestPutSettings_PortPool_Port0_Returns400(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"portPool": "0"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("portPool=0: expected 400, got %d", resp.StatusCode)
	}
	body := decodeBody(resp)
	errMsg, _ := body["error"].(string)
	if errMsg == "" {
		t.Error("expected non-empty error message in body")
	}
}

func TestPutSettings_PortPool_NotAPort_Returns400(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"portPool": "not-a-port"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("portPool=not-a-port: expected 400, got %d", resp.StatusCode)
	}
}

func TestPutSettings_PortPool_AboveMax_Returns400(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"portPool": "70000"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("portPool=70000: expected 400, got %d", resp.StatusCode)
	}
}

func TestPutSettings_PortPool_StartGreaterThanEnd_Returns400(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"portPool": "51840-51831"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("portPool start>end: expected 400, got %d", resp.StatusCode)
	}
}

// ── subnetPool validation ─────────────────────────────────────────────────────

func TestPutSettings_SubnetPool_Valid_Returns200(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"subnetPool": "10.0.0.0/8"})
	if resp.StatusCode != http.StatusOK {
		body := decodeBody(resp)
		t.Errorf("valid subnetPool: expected 200, got %d; body=%v", resp.StatusCode, body)
	}
	body := decodeBody(resp)
	if body["subnetPool"] != "10.0.0.0/8" {
		t.Errorf("subnetPool in response = %v, want '10.0.0.0/8'", body["subnetPool"])
	}
}

func TestPutSettings_SubnetPool_NotACIDR_Returns400(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"subnetPool": "not-a-cidr"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("subnetPool=not-a-cidr: expected 400, got %d", resp.StatusCode)
	}
}

func TestPutSettings_SubnetPool_HostBitsSet_Returns400(t *testing.T) {
	// 192.168.1.5/16 has host bits set — must be rejected.
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"subnetPool": "192.168.1.5/16"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("subnetPool with host bits: expected 400, got %d", resp.StatusCode)
	}
	body := decodeBody(resp)
	errMsg, _ := body["error"].(string)
	if errMsg == "" {
		t.Error("expected non-empty error message for host-bits-set subnet")
	}
}

// ── defaultFwPolicy validation ────────────────────────────────────────────────

func TestPutSettings_DefaultFwPolicy_Drop_Returns200(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"defaultFwPolicy": "drop"})
	if resp.StatusCode != http.StatusOK {
		body := decodeBody(resp)
		t.Errorf("defaultFwPolicy=drop: expected 200, got %d; body=%v", resp.StatusCode, body)
	}
	body := decodeBody(resp)
	if body["defaultFwPolicy"] != "drop" {
		t.Errorf("defaultFwPolicy in response = %v, want 'drop'", body["defaultFwPolicy"])
	}
}

func TestPutSettings_DefaultFwPolicy_Accept_Returns200(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"defaultFwPolicy": "accept"})
	if resp.StatusCode != http.StatusOK {
		body := decodeBody(resp)
		t.Errorf("defaultFwPolicy=accept: expected 200, got %d; body=%v", resp.StatusCode, body)
	}
}

func TestPutSettings_DefaultFwPolicy_Invalid_Returns400(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{"defaultFwPolicy": "reject"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("defaultFwPolicy=reject: expected 400, got %d", resp.StatusCode)
	}
	body := decodeBody(resp)
	errMsg, _ := body["error"].(string)
	if errMsg == "" {
		t.Error("expected non-empty error message for invalid defaultFwPolicy")
	}
}

// ── Other fields pass through without 400 ────────────────────────────────────

func TestPutSettings_OtherFields_Returns200(t *testing.T) {
	sta := newSettingsTestApp(t)

	resp := sta.put(t, map[string]any{
		"dns":                        "8.8.8.8",
		"defaultPersistentKeepalive": 30,
		"routerName":                 "Test-Router",
	})
	if resp.StatusCode != http.StatusOK {
		body := decodeBody(resp)
		t.Errorf("other fields: expected 200, got %d; body=%v", resp.StatusCode, body)
	}
}

// ── 401 without token ─────────────────────────────────────────────────────────

func TestPutSettings_NoAuth_Returns401(t *testing.T) {
	sta := newSettingsTestApp(t)

	ta := &testApp{app: sta.app}
	resp := ta.do("PUT", "/api/settings", "", map[string]any{"dns": "1.1.1.1"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth: expected 401, got %d", resp.StatusCode)
	}
}

func TestGetSettingsIncludesAWG31RuntimeCapability(t *testing.T) {
	t.Setenv("WG_QUICK_USERSPACE_IMPLEMENTATION", "amneziawg-go")
	sta := newSettingsTestApp(t)
	ta := &testApp{app: sta.app, adminToken: sta.token}
	resp := ta.do("GET", "/api/settings", sta.token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%v", resp.StatusCode, decodeBody(resp))
	}
	body := decodeBody(resp)
	if body["awg3Supported"] != true || body["awgMaxProtocol"] != "3.1" {
		t.Fatalf("unexpected capability: %v", body)
	}
	if body["awgEngineVersion"] != "3.1.20260814" || body["awgToolsVersion"] != "3.1.20260812" {
		t.Fatalf("unexpected versions: %v", body)
	}
}

func TestMetricsSettingsAdminUpdateAndSafeResponse(t *testing.T) {
	sta := newSettingsTestApp(t)
	ta := &testApp{app: sta.app, adminToken: sta.token}
	resp := ta.do("PUT", "/api/settings/metrics", sta.token, map[string]any{
		"enabled": true, "path": "/prometheus", "connectedPeerThresholdSeconds": 90,
		"historyEnabled": false, "token": "do-not-return-this-token",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d body=%v", resp.StatusCode, decodeBody(resp))
	}
	body := decodeBody(resp)
	if body["tokenConfigured"] != true || body["canManage"] != true {
		t.Fatalf("unexpected response: %v", body)
	}
	if _, exists := body["token"]; exists {
		t.Fatalf("write-only token leaked in response: %v", body)
	}
	encodedJSON, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(encodedJSON)
	if strings.Contains(encoded, "do-not-return-this-token") {
		t.Fatalf("plaintext token leaked in response: %s", encoded)
	}
	if got := sta.metrics.Current(); got.Path != "/prometheus" || got.ConnectedPeerThresholdSeconds != 90 || got.HistoryEnabled {
		t.Fatalf("runtime settings were not applied: %+v", got)
	}
}

func TestMetricsSettingsNonAdminReadOnly(t *testing.T) {
	sta := newSettingsTestApp(t)
	regular, err := users.Create("viewer", "viewerpass1")
	if err != nil {
		t.Fatal(err)
	}
	_, rawToken, err := tokens.Create(regular.ID, "viewer-token")
	if err != nil {
		t.Fatal(err)
	}
	ta := &testApp{app: sta.app}
	resp := ta.do("GET", "/api/settings/metrics", rawToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d body=%v", resp.StatusCode, decodeBody(resp))
	}
	if body := decodeBody(resp); body["canManage"] != false {
		t.Fatalf("regular user received manage permission: %v", body)
	}
	resp = ta.do("PUT", "/api/settings/metrics", rawToken, map[string]any{
		"enabled": true, "path": "/metrics", "connectedPeerThresholdSeconds": 60, "historyEnabled": true,
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT status=%d body=%v", resp.StatusCode, decodeBody(resp))
	}
}

func TestGenerateTemplateDefaultsToAWG31AndCanGenerateLegacyAWG2(t *testing.T) {
	sta := newSettingsTestApp(t)
	ta := &testApp{app: sta.app, adminToken: sta.token}
	resp := ta.do("POST", "/api/templates/generate", sta.token, map[string]any{"profile": "tls_client_hello"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AWG3 status=%d body=%v", resp.StatusCode, decodeBody(resp))
	}
	params, _ := decodeBody(resp)["params"].(map[string]any)
	if params["headerProtectionKey"] == nil || params["contentPaddingAddition"] != "10-100" {
		t.Fatalf("missing AWG3 defaults: %v", params)
	}
	resp = ta.do("POST", "/api/templates/generate", sta.token, map[string]any{"protocolVersion": "2.0"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AWG2 status=%d body=%v", resp.StatusCode, decodeBody(resp))
	}
	params, _ = decodeBody(resp)["params"].(map[string]any)
	if _, exists := params["headerProtectionKey"]; exists {
		t.Fatalf("legacy response leaked AWG3 field: %v", params)
	}
}
