package api

import (
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/webaccess"
	"github.com/gofiber/fiber/v2"
)

func TestWebAccessGateRequiresVPNOrMachineToken(t *testing.T) {
	ta := newRemotesTestApp(t)
	_, err := db.DB().Exec(`INSERT INTO interfaces(id,name,address,enabled) VALUES('wg12','admin','10.8.2.1/24',1)`)
	if err != nil {
		t.Fatal(err)
	}
	m, err := webaccess.New(db.DB(), webaccess.Dependencies{Prepare: func(p webaccess.Policy) (webaccess.Prepared, error) { return webaccess.Prepare(db.DB(), p) }, Apply: func([]webaccess.Rule) error { return nil }, Trial: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	p := webaccess.Policy{Enabled: true, InterfaceID: "wg12", Clients: []webaccess.Client{{Name: "bot", IP: "172.18.0.2", Interface: "br-bot"}}}
	if _, err = m.Trial(p); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Confirm(netip.MustParseAddr("10.8.2.2")); err != nil {
		t.Fatal(err)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(WebAccessMiddleware(m))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("panel") })
	app.Get("/js/app.js", func(c *fiber.Ctx) error { return c.SendString("asset") })
	app.Get("/api/session", func(c *fiber.Ctx) error { return c.SendString("login") })
	app.Get("/api/health", func(c *fiber.Ctx) error { return c.SendString("ok") })
	g := app.Group("/api", AuthMiddleware)
	g.Get("/protected", func(c *fiber.Ctx) error { return c.SendString("protected") })
	RegisterWebAccess(g, m, "127.0.0.1")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go app.Listener(listener)
	t.Cleanup(func() { app.Shutdown() })
	client := &http.Client{Timeout: 3 * time.Second}
	for _, tc := range []struct {
		name, ip, path, auth string
		trusted              bool
		want                 int
	}{
		{"vpn panel", "10.8.2.2", "/", "", true, 200},
		{"vpn login", "10.8.2.2", "/api/session", "", true, 200},
		{"other vpn panel", "10.10.1.2", "/", "Bearer " + ta.token, true, 404},
		{"s2s panel", "10.255.255.1", "/", "Bearer " + ta.token, true, 404},
		{"public token panel", "198.51.100.2", "/", "Bearer " + ta.token, true, 404},
		{"machine token panel", "172.18.0.2", "/", "Bearer " + ta.token, true, 404},
		{"machine asset", "172.18.0.2", "/js/app.js", "Bearer " + ta.token, true, 404},
		{"machine login case", "172.18.0.2", "/api/Session/", "Bearer " + ta.token, true, 404},
		{"machine login", "172.18.0.2", "/api/session", "Bearer " + ta.token, true, 404},
		{"machine password", "172.18.0.2", "/api/protected", "owner:ownerpass1", true, 404},
		{"machine invalid token", "172.18.0.2", "/api/protected", "Bearer ws_invalid", true, 404},
		{"machine no token health", "172.18.0.2", "/api/health", "", true, 404},
		{"machine health", "172.18.0.2", "/api/health", "Bearer " + ta.token, true, 200},
		{"machine API", "172.18.0.2", "/api/protected", "Bearer " + ta.token, true, 200},
		{"untrusted metadata", "10.8.2.2", "/", "", false, 404},
		{"retired link", "10.8.2.2", "/cnf/0123456789abcdef0123456789abcdef", "", true, 404},
		{"retired generation", "10.8.2.2", "/api/peers/x/generateOneTimeLink", "", true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "http://"+listener.Addr().String()+tc.path, nil)
			req.Header.Set("X-Cascade-Client-IP", tc.ip)
			req.Header.Set("X-Forwarded-For", "10.8.2.2")
			if tc.trusted {
				req.Header.Set("X-Cascade-Proxy", webaccess.ProxyVersion)
				req.Header.Set("X-Cascade-HTTPS-Port", "443")
			}
			req.Header.Set("Authorization", tc.auth)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, tc.want, body)
			}
		})
	}
	// A machine token cannot change the web policy even when its owner is admin.
	req, _ := http.NewRequest("POST", "http://"+listener.Addr().String()+"/api/settings/web-access/cancel", strings.NewReader("{}"))
	req.Header.Set("X-Cascade-Proxy", "1")
	req.Header.Set("X-Cascade-HTTPS-Port", "443")
	req.Header.Set("X-Cascade-Client-IP", "172.18.0.2")
	req.Header.Set("Authorization", "Bearer "+ta.token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("machine modified policy: %d", resp.StatusCode)
	}
}
