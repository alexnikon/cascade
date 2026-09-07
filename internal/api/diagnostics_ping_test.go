package api

import (
	"encoding/json"
	"github.com/gofiber/fiber/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticsPingBindsInterface(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$*\" = '-c 3 -W 2 -I wg13 1.1.1.1' ] || [ \"$*\" = '-c 3 -W 2 1.1.1.1' ]; then\n echo '3 packets transmitted, 3 received, 0% packet loss'\nelse\n echo '3 packets transmitted, 0 received, 100% packet loss'\nfi\n"
	if err := os.WriteFile(filepath.Join(dir, "ping"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	app := fiber.New()
	app.Post("/ping", diagnosticsPing)
	for _, body := range []string{`{"host":"1.1.1.1","interface":"wg13","count":3}`, `{"host":"1.1.1.1","count":3}`} {
		req := httptest.NewRequest("POST", "/ping", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var result PingResult
		err = json.NewDecoder(res.Body).Decode(&result)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 || !result.Reachable {
			t.Fatalf("response: %+v, %v", result, err)
		}
	}
	for _, body := range []string{`{"host":"-V"}`, `{"host":"1.1.1.1","interface":"wg13;id"}`} {
		req := httptest.NewRequest("POST", "/ping", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("unsafe input accepted: %s", body)
		}
	}
}
