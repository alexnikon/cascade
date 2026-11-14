package api

import (
	"bytes"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/remotes"
	"github.com/alexnikon/cascade/internal/sessions"
	"github.com/alexnikon/cascade/internal/users"
	"github.com/gofiber/fiber/v2"
	totpLib "github.com/pquerna/otp/totp"
)

type browserClient struct {
	app     *fiber.App
	cookies map[string]*http.Cookie
	bearer  string
}

func sessionApp(t *testing.T) *testApp {
	t.Helper()
	ta := newTestApp(t)
	RegisterSessions(ta.app.Group("/api", AuthMiddleware))
	return ta
}

func browser(app *fiber.App) *browserClient {
	return &browserClient{app: app, cookies: map[string]*http.Cookie{}}
}

func (b *browserClient) request(t *testing.T, method, path string, body any, want int) (*http.Response, map[string]any) {
	t.Helper()
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Version/18 Safari/605")
	if b.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+b.bearer)
	}
	for _, cookie := range b.cookies {
		req.AddCookie(cookie)
	}
	resp, err := b.app.Test(req, 10000)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range resp.Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode: %v: %s", err, data)
	}
	return resp, result
}

func (b *browserClient) login(t *testing.T, username, password string) map[string]any {
	t.Helper()
	_, result := b.request(t, "POST", "/api/session", map[string]any{"username": username, "password": password, "remember": false}, 200)
	return result
}

func TestBrowserSessionLifetimePersistenceRenewalAndLogout(t *testing.T) {
	ta := sessionApp(t)
	b := browser(ta.app)
	b.login(t, "alice", "alicepass1")
	cookie := b.cookies["session_id"]
	if cookie == nil || cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge < int((sessions.Lifetime-time.Minute).Seconds()) {
		t.Fatalf("HTTP cookie: %+v", cookie)
	}
	b.request(t, "GET", "/api/users/me", nil, 200)
	_, state := b.request(t, "GET", "/api/session", nil, 200)
	if state["authenticated"] != true || state["username"] != "alice" {
		t.Fatalf("state %+v", state)
	}
	// Close and reopen the actual persistent database, then reinitialize auth.
	var dbFile string
	if err := db.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbFile); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := db.Init(filepath.Dir(dbFile)); err != nil {
		t.Fatal(err)
	}
	InitAuth()
	b.app = buildFiberApp()
	RegisterSessions(b.app.Group("/api", AuthMiddleware))
	b.request(t, "GET", "/api/users/me", nil, 200)
	// Backdate renewal while retaining an otherwise valid session.
	before := time.Now().Add(-48 * time.Hour).Unix()
	if _, err := db.DB().Exec(`UPDATE browser_sessions SET renewed_at=?,last_seen_at=?`, before, before); err != nil {
		t.Fatal(err)
	}
	resp, _ := b.request(t, "GET", "/api/users/me", nil, 200)
	if len(resp.Cookies()) != 1 || resp.Cookies()[0].MaxAge < int((sessions.Lifetime-time.Minute).Seconds()) {
		t.Fatalf("renewal cookie: %+v", resp.Cookies())
	}
	resp, _ = b.request(t, "GET", "/api/users/me", nil, 200)
	if len(resp.Cookies()) != 0 {
		t.Fatal("cookie renewed more than once per day")
	}
	stolen := *b.cookies["session_id"]
	b.request(t, "DELETE", "/api/session", nil, 200)
	b.cookies[stolen.Name] = &stolen
	resp, _ = b.request(t, "GET", "/api/users/me", nil, 401)
	if resp.Header.Get("X-Cascade-Session-Expired") != "1" {
		t.Fatal("missing browser expiry signal")
	}
}

func TestBrowserSessionAccessControlAndBearerIdentity(t *testing.T) {
	ta := sessionApp(t)
	alice, bob, admin := browser(ta.app), browser(ta.app), browser(ta.app)
	alice.login(t, "alice", "alicepass1")
	bob.login(t, "bob", "bobpass1")
	admin.login(t, "admin", "adminpass1")
	_, list := alice.request(t, "GET", "/api/sessions", nil, 200)
	own := list["sessions"].([]any)
	if len(own) != 1 || own[0].(map[string]any)["current"] != true {
		t.Fatalf("own list %+v", own)
	}
	aliceID := own[0].(map[string]any)["id"].(string)
	bob.request(t, "DELETE", "/api/sessions/"+aliceID, nil, 404)
	anon := browser(ta.app)
	anon.request(t, "GET", "/api/sessions", nil, 401)
	// The admin Bearer identity must win even with another user's browser cookie.
	alice.bearer = ta.adminToken
	_, list = alice.request(t, "GET", "/api/sessions", nil, 200)
	all := list["sessions"].([]any)
	if len(all) != 3 {
		t.Fatalf("admin list %+v", all)
	}
	for _, item := range all {
		if item.(map[string]any)["current"] != false {
			t.Fatal("Bearer request claimed a current browser session")
		}
	}
	_, result := alice.request(t, "DELETE", "/api/sessions/"+aliceID, nil, 200)
	if result["current"] != false {
		t.Fatal("Bearer revoked the browser as current")
	}
	alice.bearer = ""
	alice.request(t, "GET", "/api/users/me", nil, 401)
	_, list = bob.request(t, "GET", "/api/sessions", nil, 200)
	bobID := list["sessions"].([]any)[0].(map[string]any)["id"].(string)
	_, result = bob.request(t, "DELETE", "/api/sessions/"+bobID, nil, 200)
	if result["current"] != true || bob.cookies["session_id"] != nil {
		t.Fatalf("self revoke %+v %+v", result, bob.cookies)
	}
	bob.request(t, "GET", "/api/users/me", nil, 401)
	admin.request(t, "DELETE", "/api/sessions/missing", nil, 404)
}

func TestBrowserTOTPRequiresShortLivedChallengeAndRotatesToken(t *testing.T) {
	ta := sessionApp(t)
	secret := "JBSWY3DPEHPK3PXP"
	if err := users.SetTOTP(ta.alice.ID, secret); err != nil {
		t.Fatal(err)
	}
	b := browser(ta.app)
	result := b.login(t, "alice", "alicepass1")
	if result["totp_required"] != true || b.cookies["session_id"] != nil || b.cookies["login_challenge"] == nil {
		t.Fatalf("pending %+v %+v", result, b.cookies)
	}
	challenge := *b.cookies["login_challenge"]
	if challenge.MaxAge > 600 || challenge.MaxAge < 590 {
		t.Fatal("challenge expiry not ten minutes")
	}
	b.request(t, "GET", "/api/users/me", nil, 401)
	_, state := b.request(t, "GET", "/api/session", nil, 200)
	if state["authenticated"] != false || state["totp_pending"] != true {
		t.Fatalf("pending state %+v", state)
	}
	b.request(t, "POST", "/api/auth/totp/verify", map[string]string{"code": "invalid"}, 401)
	code, _ := totpLib.GenerateCode(secret, time.Now())
	b.request(t, "POST", "/api/auth/totp/verify", map[string]string{"code": code}, 200)
	if b.cookies["session_id"] == nil || b.cookies["session_id"].Value == challenge.Value || b.cookies["login_challenge"] != nil {
		t.Fatal("challenge was not rotated")
	}
	replay := browser(ta.app)
	replay.cookies[challenge.Name] = &challenge
	replay.request(t, "POST", "/api/auth/totp/verify", map[string]string{"code": code}, 401)
	b.request(t, "GET", "/api/users/me", nil, 200)
	expired := browser(ta.app)
	expired.login(t, "alice", "alicepass1")
	if _, err := db.DB().Exec(`UPDATE auth_challenges SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	expired.request(t, "POST", "/api/auth/totp/verify", map[string]string{"code": code}, 401)
}

func TestCredentialChangesInvalidateOtherSessionsAndChallenges(t *testing.T) {
	ta := sessionApp(t)
	first, second := browser(ta.app), browser(ta.app)
	first.login(t, "alice", "alicepass1")
	second.login(t, "alice", "alicepass1")
	first.request(t, "PATCH", "/api/users/me", map[string]string{"currentPassword": "alicepass1", "password": "newpassword1"}, 200)
	first.request(t, "GET", "/api/users/me", nil, 200)
	second.request(t, "GET", "/api/users/me", nil, 401)
	second.login(t, "alice", "newpassword1")
	_, setup := first.request(t, "GET", "/api/users/me/totp/setup", nil, 200)
	raw := *first.cookies["totp_setup"]
	// Copying a setup cookie to a different session must not authorize setup.
	second.cookies[raw.Name] = &raw
	code, _ := totpLib.GenerateCode(setup["secret"].(string), time.Now())
	second.request(t, "POST", "/api/users/me/totp/enable", map[string]string{"code": code}, 400)
	first.request(t, "POST", "/api/users/me/totp/enable", map[string]string{"code": code}, 200)
	first.request(t, "GET", "/api/users/me", nil, 200)
	second.request(t, "GET", "/api/users/me", nil, 401)
	pending := browser(ta.app)
	pending.login(t, "alice", "newpassword1")
	// Disabling 2FA preserves this session and cancels the pending login.
	first.request(t, "POST", "/api/users/me/totp/disable", map[string]string{"code": code}, 200)
	first.request(t, "GET", "/api/users/me", nil, 200)
	pending.request(t, "POST", "/api/auth/totp/verify", map[string]string{"code": code}, 401)
	admin := browser(ta.app)
	admin.bearer = ta.adminToken
	admin.request(t, "PATCH", "/api/users/"+ta.alice.ID, map[string]string{"currentPassword": "adminpass1", "password": "resetpassword1"}, 200)
	first.request(t, "GET", "/api/users/me", nil, 401)
	bob := browser(ta.app)
	bob.login(t, "bob", "bobpass1")
	admin.request(t, "DELETE", "/api/users/"+ta.bob.ID, nil, 200)
	bob.request(t, "GET", "/api/users/me", nil, 401)
}

func TestSetupChallengeExpiryAndBearerBinding(t *testing.T) {
	ta := sessionApp(t)
	b := browser(ta.app)
	b.bearer = ta.aliceToken
	_, setup := b.request(t, "GET", "/api/users/me/totp/setup", nil, 200)
	code, _ := totpLib.GenerateCode(setup["secret"].(string), time.Now())
	if _, err := db.DB().Exec(`UPDATE auth_challenges SET expires_at=0`); err != nil {
		t.Fatal(err)
	}
	b.request(t, "POST", "/api/users/me/totp/enable", map[string]string{"code": code}, 400)
	_, setup = b.request(t, "GET", "/api/users/me/totp/setup", nil, 200)
	code, _ = totpLib.GenerateCode(setup["secret"].(string), time.Now())
	b.bearer = ta.bobToken
	b.request(t, "POST", "/api/users/me/totp/enable", map[string]string{"code": code}, 400)
	b.bearer = ta.aliceToken
	b.request(t, "POST", "/api/users/me/totp/enable", map[string]string{"code": code}, 200)
	b.request(t, "POST", "/api/users/me/totp/enable", map[string]string{"code": code}, 400)
}

func TestAuthTransportTrustAndCookieIsolation(t *testing.T) {
	ta := sessionApp(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ta.app.Listener(listener)
	t.Cleanup(func() { ta.app.Shutdown() })
	base := "http://" + listener.Addr().String()
	login := func(trusted bool) []*http.Cookie {
		t.Helper()
		req, _ := http.NewRequest("POST", base+"/api/session", strings.NewReader(`{"username":"alice","password":"alicepass1"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Forwarded", "proto=https")
		req.Header.Set("X-Cascade-Client-IP", "10.8.0.2")
		req.Header.Set("X-Cascade-HTTPS-Port", "443")
		if trusted {
			req.Header.Set("X-Cascade-Proxy", "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("login %d", resp.StatusCode)
		}
		return resp.Cookies()
	}
	var plain, secure *http.Cookie
	for _, cookie := range login(false) {
		if cookie.Name == "session_id" && cookie.Value != "" {
			plain = cookie
		}
	}
	for _, cookie := range login(true) {
		if cookie.Name == "session_id_https" && cookie.Value != "" {
			secure = cookie
		}
	}
	if plain == nil || plain.Secure || secure == nil || !secure.Secure || plain.Value == secure.Value {
		t.Fatalf("cookies plain=%+v secure=%+v", plain, secure)
	}
	// Both cookies can coexist on one host, with the transport selecting identity.
	for _, trusted := range []bool{false, true} {
		req, _ := http.NewRequest("GET", base+"/api/sessions", nil)
		req.AddCookie(plain)
		req.AddCookie(secure)
		if trusted {
			req.Header.Set("X-Cascade-Proxy", "1")
			req.Header.Set("X-Cascade-Client-IP", "10.8.0.2")
			req.Header.Set("X-Cascade-HTTPS-Port", "443")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Sessions []sessions.Session `json:"sessions"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(result.Sessions) != 2 || !result.Sessions[0].Current {
			t.Fatalf("transport list %+v status=%d", result, resp.StatusCode)
		}
		if trusted && result.Sessions[0].LoginIP != "10.8.0.2" {
			t.Fatal("trusted client IP not recorded")
		}
		if !trusted && result.Sessions[0].LoginIP != "127.0.0.1" {
			t.Fatal("spoofed client IP accepted")
		}
	}
}

func TestRestoreArchiveClearsSessionsAndChallenges(t *testing.T) {
	ta := sessionApp(t)
	b := browser(ta.app)
	b.login(t, "alice", "alicepass1")
	b.request(t, "GET", "/api/users/me/totp/setup", nil, 200)
	var path string
	if err := db.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := makeTarGz(t, "cascade.db", content)
	clean, err := sanitizeRestoreSessions(archive)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := extractDBFromTarGz(clean)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(file, restored, 0600); err != nil {
		t.Fatal(err)
	}
	// Reopen only the sanitized backup; the live database must still be valid.
	d, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, table := range []string{"browser_sessions", "auth_challenges"} {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("restored %s=%d: %v", table, n, err)
		}
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n != 3 {
		t.Fatal("restore changed user data")
	}
	b.request(t, "GET", "/api/users/me", nil, 200)
}

func TestDirectTLSUsesSecurePersistentCookie(t *testing.T) {
	ta := sessionApp(t)
	certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := certificateServer.TLS.Certificates[0]
	client := certificateServer.Client()
	certificateServer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	go ta.app.Listener(tlsListener)
	t.Cleanup(func() { ta.app.Shutdown() })
	req, _ := http.NewRequest("POST", "https://"+listener.Addr().String()+"/api/session", strings.NewReader(`{"username":"alice","password":"alicepass1"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	found := false
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "session_id_https" && cookie.Value != "" {
			found = true
			if !cookie.Secure || !cookie.HttpOnly || cookie.MaxAge < int((sessions.Lifetime-time.Minute).Seconds()) {
				t.Fatalf("TLS cookie %+v", cookie)
			}
		}
	}
	if !found {
		t.Fatal("direct TLS not recognized")
	}
}

func TestRemoteSessionProxyUsesBearerAndStripsBrowserExpirySignal(t *testing.T) {
	ta := sessionApp(t)
	b := browser(ta.app)
	b.login(t, "alice", "alicepass1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer remote-token" || r.Header.Get("Cookie") != "" {
			t.Error("remote identity or cookie isolation failed")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/sessions":
			w.Write([]byte(`{"sessions":[{"id":"remote-session","current":false}]}`))
		case "DELETE /api/sessions/remote-session":
			w.Write([]byte(`{"ok":true,"current":false}`))
		default:
			w.Header().Set("X-Cascade-Session-Expired", "1")
			http.SetCookie(w, &http.Cookie{Name: "session_id", Value: "remote-credential", Path: "/"})
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"Unauthorized"}`))
		}
	}))
	defer upstream.Close()
	previous := proxyClient
	proxyClient = upstream.Client()
	defer func() { proxyClient = previous }()
	remote, err := remotes.Add("Remote", upstream.URL, "remote-token", false)
	if err != nil {
		t.Fatal(err)
	}
	RegisterRemotes(ta.app.Group("/api", AuthMiddleware))
	base := "/api/remotes/" + remote.ID + "/proxy"
	_, result := b.request(t, "GET", base+"/sessions", nil, 200)
	if result["sessions"].([]any)[0].(map[string]any)["current"] != false {
		t.Fatal("remote claimed current browser")
	}
	b.request(t, "DELETE", base+"/sessions/remote-session", nil, 200)
	resp, _ := b.request(t, "GET", base+"/expired", nil, 401)
	if resp.Header.Get("X-Cascade-Session-Expired") != "" || len(resp.Cookies()) != 0 {
		t.Fatal("upstream altered local browser authentication")
	}
	b.request(t, "GET", "/api/users/me", nil, 200)
	// An expired local browser is rejected by the local gate before proxying.
	sessions.Logout(b.cookies["session_id"].Value)
	resp, _ = b.request(t, "GET", base+"/sessions", nil, 401)
	if resp.Header.Get("X-Cascade-Session-Expired") != "1" {
		t.Fatal("local expiry not distinguished from remote expiry")
	}
}
