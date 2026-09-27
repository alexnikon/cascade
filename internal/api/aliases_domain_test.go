package api

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/dnsalias"
	"github.com/alexnikon/cascade/internal/ipset"
)

// newAliasApp wires the alias routes against the package test database, with a
// domain resolver pointed at a resolver address that never answers — these tests
// are about the HTTP surface, not about DNS.
func newAliasApp(t *testing.T) (*fiber.App, *aliases.Manager) {
	t.Helper()

	im, err := ipset.New(t.TempDir())
	if err != nil {
		t.Fatalf("ipset.New: %v", err)
	}
	am := aliases.New(im)
	aliases.SetInstance(am)
	t.Cleanup(func() { aliases.SetInstance(nil) })

	r := dnsalias.New(am, im)
	dnsalias.SetInstance(r)
	r.Start()
	t.Cleanup(func() {
		r.Stop()
		dnsalias.SetInstance(nil)
	})

	app := fiber.New()
	RegisterAliases(app.Group("/api"))
	return app, am
}

func doJSON(t *testing.T, app *fiber.App, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, 10000)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, raw
}

func TestAPI_CreateDomainAlias(t *testing.T) {
	app, am := newAliasApp(t)

	status, body := doJSON(t, app, "POST", "/api/aliases",
		`{"name":"API-YouTube","type":"domain","entries":["youtube.com","www.youtube.com","youtube.com"]}`)
	if status != fiber.StatusCreated {
		t.Fatalf("status = %d, body = %s", status, body)
	}

	var a aliases.Alias
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	t.Cleanup(func() { am.Delete(a.ID) }) //nolint:errcheck

	if a.Type != "domain" {
		t.Errorf("type = %q, want domain", a.Type)
	}
	if len(a.Entries) != 2 {
		t.Errorf("entries = %v, want the duplicate collapsed", a.Entries)
	}
	if want := aliases.DomainSetV4(a.ID); a.IPSetName != want {
		t.Errorf("ipsetName = %q, want %q", a.IPSetName, want)
	}
}

func TestAPI_CreateDomainAliasRejectsWildcard(t *testing.T) {
	app, _ := newAliasApp(t)

	status, body := doJSON(t, app, "POST", "/api/aliases",
		`{"name":"API-Wildcard","type":"domain","entries":["*.googlevideo.com"]}`)
	if status != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", status, body)
	}
	if !strings.Contains(string(body), "wildcard") {
		t.Errorf("error message should explain wildcards are unsupported, got %s", body)
	}
}

func TestAPI_GetDomainAliasExposesRuntimeStatus(t *testing.T) {
	app, am := newAliasApp(t)

	a, err := am.Create(aliases.Alias{Name: "API-Status", Type: "domain", Entries: []string{"example.com"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { am.Delete(a.ID) }) //nolint:errcheck
	dnsalias.Get().Sync(a.ID)

	status, body := doJSON(t, app, "GET", "/api/aliases/"+a.ID, "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}

	var got aliases.Alias
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DomainStatus == nil {
		t.Fatal("want domainStatus on a domain alias, got none")
	}
}

// Runtime state must never leak onto the other alias types.
func TestAPI_NonDomainAliasHasNoDomainStatus(t *testing.T) {
	app, am := newAliasApp(t)

	a, err := am.Create(aliases.Alias{Name: "API-Hosts", Type: "host", Entries: []string{"10.0.0.1"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { am.Delete(a.ID) }) //nolint:errcheck

	status, body := doJSON(t, app, "GET", "/api/aliases/"+a.ID, "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if strings.Contains(string(body), "domainStatus") {
		t.Errorf("host alias exposed domainStatus: %s", body)
	}
}

func TestAPI_RefreshDomainAlias(t *testing.T) {
	app, am := newAliasApp(t)

	a, err := am.Create(aliases.Alias{Name: "API-Refresh", Type: "domain", Entries: []string{"example.com"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { am.Delete(a.ID) }) //nolint:errcheck
	dnsalias.Get().Sync(a.ID)

	status, body := doJSON(t, app, "POST", "/api/aliases/"+a.ID+"/refresh", "")
	if status != fiber.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", status, body)
	}
}

func TestAPI_RefreshRejectsNonDomainAlias(t *testing.T) {
	app, am := newAliasApp(t)

	a, err := am.Create(aliases.Alias{Name: "API-NotDomain", Type: "host", Entries: []string{"10.0.0.2"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { am.Delete(a.ID) }) //nolint:errcheck

	status, _ := doJSON(t, app, "POST", "/api/aliases/"+a.ID+"/refresh", "")
	if status != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestAPI_RefreshUnknownAliasIs404(t *testing.T) {
	app, _ := newAliasApp(t)

	status, _ := doJSON(t, app, "POST", "/api/aliases/00000000-0000-0000-0000-000000000000/refresh", "")
	if status != fiber.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}
