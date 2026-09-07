package api

import (
	"github.com/alexnikon/cascade/internal/gateway"
	"github.com/gofiber/fiber/v2"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayAdminPatchPreservesTarget(t *testing.T) {
	// Use the package TestMain database without replacing shared peer fixtures.
	manager := gateway.NewManager()
	gateway.SetInstance(manager)
	defer gateway.SetInstance(nil)
	disabled := false
	gw, err := manager.CreateGateway(gateway.GatewayInput{Name: "test", Interface: "eth0", GatewayIP: "192.0.2.1", MonitorAddress: "1.1.1.1", Monitor: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DeleteGateway(gw.ID) })
	app := fiber.New()
	RegisterGateways(app.Group("/api"))
	req := httptest.NewRequest("PATCH", "/api/gateways/"+gw.ID, strings.NewReader(`{"adminDown":true}`))
	req.Header.Set("Content-Type", "application/json")
	response, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("status = %d", response.StatusCode)
	}
	got, err := manager.GetGateway(gw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MonitorAddress != "1.1.1.1" || !got.AdminDown {
		t.Fatalf("unexpected gateway: %+v", got)
	}
}
