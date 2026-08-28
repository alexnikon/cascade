package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestInterfaceImportRoutesExcludeLegacyBackupImporter(t *testing.T) {
	app := fiber.New()
	RegisterInterfaces(app.Group("/api"))

	for _, routes := range app.Stack() {
		for _, route := range routes {
			if route.Method == fiber.MethodPost && route.Path == "/api/tunnel-interfaces/import-backup" {
				t.Fatal("legacy import route is still registered")
			}
		}
	}

	legacyReq := httptest.NewRequest("POST", "/api/tunnel-interfaces/import-backup", nil)
	legacyResp, err := app.Test(legacyReq)
	if err != nil {
		t.Fatalf("legacy route request: %v", err)
	}
	if legacyResp.StatusCode != fiber.StatusMethodNotAllowed {
		t.Fatalf("legacy import route status = %d, want %d", legacyResp.StatusCode, fiber.StatusMethodNotAllowed)
	}

	nativeReq := httptest.NewRequest("POST", "/api/tunnel-interfaces/import-interface", nil)
	nativeReq.Header.Set("Content-Type", "application/json")
	nativeResp, err := app.Test(nativeReq)
	if err != nil {
		t.Fatalf("native route request: %v", err)
	}
	if nativeResp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("native import route status = %d, want %d", nativeResp.StatusCode, fiber.StatusBadRequest)
	}
}
