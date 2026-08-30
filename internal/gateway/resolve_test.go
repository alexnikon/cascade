package gateway

import (
	"testing"

	"github.com/alexnikon/cascade/internal/db"
)

func newResolverTestManager(t *testing.T) *Manager {
	t.Helper()
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(db.Close)
	return NewManager()
}

func addResolverGateway(t *testing.T, manager *Manager, id, ip, iface, status string) {
	t.Helper()
	if err := insertGateway(Gateway{
		ID:              id,
		Name:            id,
		Interface:       iface,
		GatewayIP:       ip,
		Enabled:         true,
		Monitor:         true,
		MonitorInterval: 5,
		WindowSeconds:   30,
		MonitorRule:     "icmp_only",
		CreatedAt:       id,
	}); err != nil {
		t.Fatalf("insertGateway(%s): %v", id, err)
	}
	manager.monitor.mu.Lock()
	manager.monitor.states[id] = &monitorState{status: MonitorStatus{Status: status}}
	manager.monitor.mu.Unlock()
}

func setResolverStatus(manager *Manager, id, status string) {
	manager.monitor.mu.RLock()
	state := manager.monitor.states[id]
	manager.monitor.mu.RUnlock()
	state.mu.Lock()
	state.status.Status = status
	state.mu.Unlock()
}

func TestResolveGroupGateway_UsesHealthyHighestPriorityTier(t *testing.T) {
	manager := newResolverTestManager(t)
	addResolverGateway(t, manager, "g1", "192.0.2.1", "eth1", "healthy")
	addResolverGateway(t, manager, "g2", "192.0.2.2", "eth2", "healthy")
	group, err := manager.CreateGroup(GatewayGroupInput{Name: "test", Gateways: []GatewayGroupMember{
		{GatewayID: "g1", Tier: 1}, {GatewayID: "g2", Tier: 2},
	}})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	resolved, err := manager.ResolveGroupGateway(group.ID)
	if err != nil {
		t.Fatalf("ResolveGroupGateway: %v", err)
	}
	if resolved.ID != "g1" {
		t.Fatalf("resolved gateway = %s, want g1", resolved.ID)
	}

	setResolverStatus(manager, "g1", "down")
	resolved, err = manager.ResolveGroupGateway(group.ID)
	if err != nil {
		t.Fatalf("ResolveGroupGateway after primary down: %v", err)
	}
	if resolved.ID != "g2" {
		t.Fatalf("resolved gateway after primary down = %s, want g2", resolved.ID)
	}

	setResolverStatus(manager, "g1", "healthy")
	resolved, err = manager.ResolveGroupGateway(group.ID)
	if err != nil {
		t.Fatalf("ResolveGroupGateway after primary recovery: %v", err)
	}
	if resolved.ID != "g1" {
		t.Fatalf("resolved gateway after primary recovery = %s, want g1", resolved.ID)
	}
}

func TestResolveGroupGateway_AllDownUsesTierOneLastResort(t *testing.T) {
	manager := newResolverTestManager(t)
	addResolverGateway(t, manager, "g1", "192.0.2.1", "eth1", "down")
	addResolverGateway(t, manager, "g2", "192.0.2.2", "eth2", "admin_down")
	group, err := manager.CreateGroup(GatewayGroupInput{Name: "test", Gateways: []GatewayGroupMember{
		{GatewayID: "g1", Tier: 1}, {GatewayID: "g2", Tier: 2},
	}})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	resolved, err := manager.ResolveGroupGateway(group.ID)
	if err != nil {
		t.Fatalf("ResolveGroupGateway: %v", err)
	}
	if resolved.ID != "g1" {
		t.Fatalf("all-down gateway = %s, want tier-one g1", resolved.ID)
	}

	contains, err := manager.GroupContainsGateway(group.ID, "g2")
	if err != nil || !contains {
		t.Fatalf("GroupContainsGateway(g2) = %t, %v; want true", contains, err)
	}
}
