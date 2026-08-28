package tunnel

// import_interface.go — import a native Cascade interface export.
//
// The supported format is produced by GET /:id/export:
// { "interface": { privateKey, publicKey, address, ... }, "peers": [...] }
//
// Keys (server + client) are used as-is — no regeneration.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alexnikon/cascade/internal/peer"
	"github.com/alexnikon/cascade/internal/validate"
)

// ── Backup JSON types ─────────────────────────────────────────────────────────

// cascadeBackup is the top-level structure of a Cascade backup file.
type cascadeBackup struct {
	Interface cascadeBackupIface  `json:"interface"`
	Peers     []cascadeBackupPeer `json:"peers"`
}

// cascadeBackupIface mirrors the ifaceJSON output plus privateKey.
type cascadeBackupIface struct {
	PrivateKey    string             `json:"privateKey"`
	PublicKey     string             `json:"publicKey"`
	Address       string             `json:"address"`
	Protocol      string             `json:"protocol"`
	DisableRoutes bool               `json:"disableRoutes"`
	NatDisabled   bool               `json:"natDisabled"`
	DNS           string             `json:"dns"`
	PublicHost    string             `json:"publicHost"`
	MTU           int                `json:"mtu"`
	MSS           int                `json:"mss"`
	AWG2          *peer.AWG2Settings `json:"settings"`
}

// cascadeBackupPeer mirrors the peer.Peer JSON fields we need.
type cascadeBackupPeer struct {
	Name                string      `json:"name"`
	PublicKey           string      `json:"publicKey"`
	PrivateKey          string      `json:"privateKey"`
	PresharedKey        string      `json:"presharedKey"`
	AllowedIPs          string      `json:"allowedIPs"`
	Address             string      `json:"address"`
	ClientAllowedIPs    string      `json:"clientAllowedIPs"`
	PeerType            string      `json:"peerType"`
	Endpoint            string      `json:"endpoint"`
	PersistentKeepalive int         `json:"persistentKeepalive"`
	GroupID             string      `json:"groupId"`
	ExpiredAt           interface{} `json:"expiredAt"`
	Enabled             bool        `json:"enabled"`
	CreatedAt           string      `json:"createdAt"`
}

// ── Input / Result types ──────────────────────────────────────────────────────

// ImportInterfaceInput is the payload for Manager.ImportInterface (Cascade export format).
type ImportInterfaceInput struct {
	RawJSON    string // raw content of a Cascade export JSON (GET /:id/export)
	ListenPort int    // UDP port to assign to the restored interface
}

// ImportInterfaceResult is returned by Manager.ImportInterface.
type ImportInterfaceResult struct {
	Interface    *TunnelInterface
	PeersCreated int
	PeersFailed  []string // names of clients that could not be imported
	Started      bool
	StartError   error
}

// ImportInterface restores a Cascade interface export (produced by GET /:id/export).
// It creates a new interface with the original keys and optionally recreates peers.
func (m *Manager) ImportInterface(inp ImportInterfaceInput) (*ImportInterfaceResult, error) {
	if inp.ListenPort <= 0 || inp.ListenPort > 65535 {
		return nil, fmt.Errorf("invalid listen port %d", inp.ListenPort)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inp.RawJSON), &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, ok := raw["interface"]; !ok {
		return nil, fmt.Errorf("not a Cascade export: missing \"interface\" key")
	}
	return m.importCascadeBackup(inp)
}

// importCascadeBackup handles the native Cascade backup format:
// { "interface": { privateKey, publicKey, address, ... }, "peers": [...] }
func (m *Manager) importCascadeBackup(inp ImportInterfaceInput) (*ImportInterfaceResult, error) {
	var backup cascadeBackup
	if err := json.Unmarshal([]byte(inp.RawJSON), &backup); err != nil {
		return nil, fmt.Errorf("invalid Cascade backup JSON: %w", err)
	}

	ifc := backup.Interface
	if ifc.PrivateKey == "" {
		return nil, fmt.Errorf("backup missing interface.privateKey")
	}
	if ifc.PublicKey == "" {
		return nil, fmt.Errorf("backup missing interface.publicKey")
	}
	if ifc.Address == "" {
		return nil, fmt.Errorf("backup missing interface.address")
	}

	if err := validate.WGKey(ifc.PrivateKey); err != nil {
		return nil, fmt.Errorf("invalid server private key: %w", err)
	}
	if err := validate.WGKey(ifc.PublicKey); err != nil {
		return nil, fmt.Errorf("invalid server public key: %w", err)
	}

	address := ifc.Address
	if !strings.Contains(address, "/") {
		address += "/24"
	}

	m.mu.RLock()
	for _, t := range m.interfaces {
		if t.ListenPort == inp.ListenPort {
			m.mu.RUnlock()
			return nil, fmt.Errorf("port %d is already used by interface %s", inp.ListenPort, t.ID)
		}
		if subnetsOverlap(t.Address, address) {
			m.mu.RUnlock()
			return nil, fmt.Errorf("address %s overlaps with existing interface %s (%s)", address, t.ID, t.Address)
		}
	}
	m.mu.RUnlock()

	protocol := ifc.Protocol
	if protocol == "" {
		protocol = "wireguard-1.0"
	}

	iface, err := m.CreateInterface(CreateInput{
		Protocol:      protocol,
		Address:       address,
		ListenPort:    inp.ListenPort,
		DisableRoutes: ifc.DisableRoutes,
		DNS:           ifc.DNS,
		AWG2:          ifc.AWG2,
	})
	if err != nil {
		return nil, fmt.Errorf("create interface: %w", err)
	}

	iface.PrivateKey = ifc.PrivateKey
	iface.PublicKey = ifc.PublicKey
	iface.NatDisabled = ifc.NatDisabled
	iface.PublicHost = ifc.PublicHost
	iface.MTU = ifc.MTU
	iface.MSS = ifc.MSS
	if err := iface.save(); err != nil {
		_ = m.DeleteInterface(iface.ID)
		return nil, fmt.Errorf("save interface keys: %w", err)
	}
	if err := iface.RegenerateConfig(); err != nil {
		_ = m.DeleteInterface(iface.ID)
		return nil, fmt.Errorf("regenerate config: %w", err)
	}

	var peersCreated int
	var peersFailed []string

	for _, bp := range backup.Peers {
		inp := peer.PeerInput{
			Name:                bp.Name,
			PublicKey:           bp.PublicKey,
			PrivateKey:          bp.PrivateKey,
			PresharedKey:        bp.PresharedKey,
			AllowedIPs:          bp.AllowedIPs,
			Address:             bp.Address,
			ClientAllowedIPs:    bp.ClientAllowedIPs,
			PeerType:            bp.PeerType,
			Endpoint:            bp.Endpoint,
			PersistentKeepalive: bp.PersistentKeepalive,
			GroupID:             bp.GroupID,
			GenerateKeys:        false,
			CreatedAt:           bp.CreatedAt,
		}
		if bp.ExpiredAt != nil {
			if s, ok := bp.ExpiredAt.(string); ok && s != "" {
				inp.ExpiredAt = s
			}
		}

		p, err := iface.AddPeer(inp)
		if err != nil {
			peersFailed = append(peersFailed, bp.Name)
			continue
		}
		if !bp.Enabled {
			f := false
			_, _ = iface.UpdatePeer(p.ID, peer.PeerUpdate{Enabled: &f})
		}
		peersCreated++
	}

	startErr := iface.Start()
	return &ImportInterfaceResult{
		Interface:    iface,
		PeersCreated: peersCreated,
		PeersFailed:  peersFailed,
		Started:      startErr == nil,
		StartError:   startErr,
	}, nil
}
