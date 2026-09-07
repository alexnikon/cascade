package gateway

import "testing"

func TestAdminOnlyUpdatePreservesMonitorConfiguration(t *testing.T) {
	m := newResolverTestManager(t)
	gw := Gateway{ID: "test", Name: "test", Interface: "wg13", GatewayIP: "10.255.255.6", MonitorAddress: "1.1.1.1", MonitorRule: "icmp_only", Enabled: true}
	if err := insertGateway(gw); err != nil {
		t.Fatal(err)
	}
	for _, down := range []bool{true, false} {
		got, err := m.SetAdminDown(gw.ID, down)
		if err != nil {
			t.Fatal(err)
		}
		if got.MonitorAddress != gw.MonitorAddress || got.GatewayIP != gw.GatewayIP || got.Interface != gw.Interface || got.AdminDown != down {
			t.Fatalf("admin update changed configuration: %+v", got)
		}
	}
}
