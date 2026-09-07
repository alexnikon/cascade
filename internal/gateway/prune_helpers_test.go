package gateway

import "testing"

func newTestManager(t *testing.T) *Manager {
	return newResolverTestManager(t)
}

func insertTestGateway(t *testing.T, _ *Manager, id, ip, iface string) {
	t.Helper()
	if err := insertGateway(Gateway{ID: id, Name: id, GatewayIP: ip, Interface: iface}); err != nil {
		t.Fatal(err)
	}
}
