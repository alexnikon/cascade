package gateway

import (
	"github.com/alexnikon/cascade/internal/settings"
	"testing"
)

func TestCreatedGatewayInheritsCurrentWindow(t *testing.T) {
	m := newResolverTestManager(t)
	set := func(n int) {
		t.Helper()
		if _, err := settings.UpdateSettings(map[string]any{"gatewayWindowSeconds": n}); err != nil {
			t.Fatal(err)
		}
	}
	set(120)
	disabled := false
	gw, err := m.CreateGateway(GatewayInput{Name: "window-test", Interface: "eth0", GatewayIP: "192.0.2.1", Monitor: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		saved, err := m.GetGateway(gw.ID)
		if err != nil {
			t.Fatal(err)
		}
		window, _, _ := globalThresholds(saved.WindowSeconds)
		if window != want {
			t.Fatalf("window=%d, want %d", window, want)
		}
		probes := []probe{{ts: nowMs() - 100000, success: true}}
		addToWindow(&probes, probe{ts: nowMs(), success: true}, window)
		expected := 1
		if want >= 120 {
			expected = 2
		}
		if len(probes) != expected {
			t.Fatalf("retained %d probes, want %d", len(probes), expected)
		}
	}
	if gw.WindowSeconds != 0 {
		t.Fatal("global default was copied into override")
	}
	check(120)
	set(90)
	check(90)
	input := GatewayInput{Name: gw.Name, Interface: gw.Interface, GatewayIP: gw.GatewayIP, Monitor: &disabled, WindowSeconds: 45}
	if _, err = m.UpdateGateway(gw.ID, input); err != nil {
		t.Fatal(err)
	}
	check(45)
	set(150)
	check(45)
	input.WindowSeconds = 0
	if _, err = m.UpdateGateway(gw.ID, input); err != nil {
		t.Fatal(err)
	}
	check(150)
}
