package prometheusmetrics

import (
	"database/sql"

	"github.com/alexnikon/cascade/internal/gateway"
	"github.com/alexnikon/cascade/internal/tunnel"
)

// NewNativeCollector wires Prometheus to Cascade's process-local managers.
func NewNativeCollector(database *sql.DB, version, commit string, manager *Manager) *Collector {
	collector := NewCollector(nativeRuntimeProvider{}, nativeGatewayProvider{}, database, version, commit, manager.ConnectedPeerThreshold())
	collector.thresholdProvider = manager.ConnectedPeerThreshold
	return collector
}

type nativeRuntimeProvider struct{}

func (nativeRuntimeProvider) RuntimeSnapshots() []tunnel.RuntimeInterfaceSnapshot {
	manager := tunnel.Get()
	if manager == nil {
		return nil
	}
	return manager.RuntimeSnapshots()
}

type nativeGatewayProvider struct{}

func (nativeGatewayProvider) GetAllGatewaysWithStatus() ([]gateway.GatewayWithStatus, error) {
	manager := gateway.Get()
	if manager == nil {
		return nil, nil
	}
	return manager.GetAllGatewaysWithStatus()
}
