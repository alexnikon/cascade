package prometheusmetrics

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const (
	ModeVPN         = "vpn"
	ModePublicHTTPS = "public_https"
	InternalPath    = "/_internal/prometheus/metrics"
)

type Interface struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Enabled bool   `json:"enabled"`
}

func (m *Manager) Interfaces() ([]Interface, error) {
	rows, err := m.db.Query("SELECT id,name,address,enabled FROM interfaces ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Interface{}
	for rows.Next() {
		var i Interface
		if err := rows.Scan(&i.ID, &i.Name, &i.Address, &i.Enabled); err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	return result, rows.Err()
}

func (m *Manager) InterfaceAddress(id string) (string, error) {
	var address string
	var enabled bool
	if err := m.db.QueryRow("SELECT address,enabled FROM interfaces WHERE id = ?", id).Scan(&address, &enabled); err != nil {
		return "", fmt.Errorf("selected VPN interface is unavailable")
	}
	if !enabled {
		return "", fmt.Errorf("selected VPN interface is disabled")
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(strings.Split(address, ",")[0]))
	if err != nil || prefix.Addr().IsUnspecified() || prefix.Addr().IsLoopback() || prefix.Addr().IsMulticast() {
		return "", fmt.Errorf("selected VPN interface has no usable address")
	}
	return prefix.Addr().String(), nil
}

func (m *Manager) vpnAddress(id string) (string, error) {
	address, err := m.InterfaceAddress(id)
	if err != nil {
		return "", err
	}
	iface, err := net.InterfaceByName(id)
	if err != nil || iface.Flags&net.FlagUp == 0 {
		return "", fmt.Errorf("selected VPN interface is down")
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return "", fmt.Errorf("cannot read selected VPN interface addresses")
	}
	for _, a := range addresses {
		p, e := netip.ParsePrefix(a.String())
		if e == nil && p.Addr().String() == address {
			return address, nil
		}
	}
	return "", fmt.Errorf("selected VPN interface address is unavailable")
}

// Only the default gateway of the container's Ethernet interface can be the
// host-side proxy. Never trust a default route through a VPN or a client header.
func bridgeGateway(routes string) (netip.Addr, error) {
	for _, line := range strings.Split(routes, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[0] != "eth0" || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, e := strconv.ParseUint(f[3], 16, 16)
		if e != nil || flags&3 != 3 {
			continue
		}
		n, e := strconv.ParseUint(f[2], 16, 32)
		if e == nil && n != 0 {
			return netip.AddrFrom4([4]byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}), nil
		}
	}
	return netip.Addr{}, fmt.Errorf("cannot identify the local Caddy bridge gateway")
}

func publicProxyPeer(bind, network string) (netip.Addr, error) {
	if network == "bridge" {
		if runtime.GOOS != "linux" || bind != "0.0.0.0" {
			return netip.Addr{}, fmt.Errorf("Public HTTPS requires the standard bridge backend configuration")
		}
		routes, err := os.ReadFile("/proc/net/route")
		if err != nil {
			return netip.Addr{}, fmt.Errorf("cannot read the local proxy route")
		}
		return bridgeGateway(string(routes))
	}
	if network != "" && network != "host" {
		return netip.Addr{}, fmt.Errorf("Public HTTPS requires host or standard bridge networking")
	}
	ip, err := netip.ParseAddr(bind)
	if err != nil || ip.Unmap() != netip.MustParseAddr("127.0.0.1") {
		return netip.Addr{}, fmt.Errorf("Public HTTPS requires a loopback management bind")
	}
	return netip.MustParseAddr("127.0.0.1"), nil
}
