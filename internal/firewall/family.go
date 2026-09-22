package firewall

// Address families.
//
// Cascade compiles one firewall rule into up to two kernel rule sets — one per
// address family — from a single configuration object. There is no separate
// "IPv6 rule" and no separate IPv6 engine: the same rule, the same fwmark and
// the same reconciliation run twice with a different toolchain.
//
// What differs between the two passes is the binary (iptables-nft vs
// ip6tables-nft), the routing command prefix (ip vs ip -6) and which of an
// endpoint's data can be expressed at all. A rule is only emitted into a family
// whose matches it can validly produce — an IPv4 CIDR never reaches
// ip6tables-nft, and an IPv4-only ipset alias never produces an IPv6 rule.

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/alexnikon/cascade/internal/gateway"
)

// fam is one address family and the tools that operate on it.
type fam struct {
	v6  bool
	ipt string // iptables binary
	ipr string // "ip route"/"ip rule" command prefix
	tag string // for log messages
}

var (
	famV4 = fam{v6: false, ipt: "iptables-nft", ipr: "ip", tag: "IPv4"}
	famV6 = fam{v6: true, ipt: "ip6tables-nft", ipr: "ip -6", tag: "IPv6"}
)

// families returns the address families a rule is compiled into, in order.
func families() []fam { return []fam{famV4, famV6} }

// ── Family filtering ──────────────────────────────────────────────────────────

// addrIsV6 reports whether a plain address or CIDR is IPv6.
func addrIsV6(s string) bool {
	s = strings.TrimSpace(s)
	if ip, _, err := net.ParseCIDR(s); err == nil {
		return ip.To4() == nil
	}
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() == nil
}

// matchesFamily reports whether an address or CIDR belongs to f.
func (f fam) matchesFamily(s string) bool { return addrIsV6(s) == f.v6 }

// protocolFor adapts a rule protocol to this family, reporting whether the
// combination can be expressed at all.
//
// ICMP is the one protocol whose name is family-specific: "icmp" only exists in
// iptables and "icmpv6" only in ip6tables. A rule that matches ICMP is
// therefore compiled as ICMPv6 in the v6 pass rather than being dropped — the
// user asked to match "ping", and ping means a different protocol number in
// each family.
func (f fam) protocolFor(proto string) (string, bool) {
	if proto == "icmp" && f.v6 {
		return "icmpv6", true
	}
	return proto, true
}

// ── IPv6 capability of a gateway ──────────────────────────────────────────────

// gatewayRouteFor returns the next hop to use for a gateway in one address
// family, and whether the gateway can carry that family at all.
//
// IPv4 is unconditional: every gateway has an IPv4 next hop, which is a required
// field. IPv6 is capability-gated, in this order:
//
//  1. an explicit gatewayIPv6 — used as an on-link next hop;
//  2. otherwise, a global IPv6 address on the gateway's interface — used as a
//     device route, which is the shape a WireGuard tunnel needs anyway;
//  3. otherwise the gateway cannot carry IPv6 at all.
//
// Step 2 is what makes existing configurations dual-stack with no migration: a
// WARP or S2S tunnel that already holds a global IPv6 address is usable
// immediately, and one that does not is never given a bogus IPv6 route.
func (m *Manager) gatewayRouteFor(gw *gateway.Gateway, f fam) (resolvedGW, capability) {
	if gw == nil {
		return resolvedGW{}, capNo
	}
	if !f.v6 {
		return resolvedGW{gatewayIP: gw.GatewayIP, iface: gw.Interface}, capYes
	}
	if gw.GatewayIPv6 != "" {
		return resolvedGW{gatewayIP: gw.GatewayIPv6, iface: gw.Interface}, capYes
	}
	switch hasV6, known := m.ifaceGlobalIPv6(gw.Interface); {
	case !known:
		return resolvedGW{}, capUnknown
	case hasV6:
		return resolvedGW{iface: gw.Interface}, capYes
	}
	return resolvedGW{}, capNo
}

// capability is the answer to "can this gateway carry this address family".
type capability int

const (
	capNo capability = iota
	capYes
	// capUnknown: the question cannot be answered right now, because the
	// gateway's interface does not currently exist. A tunnel is torn down and
	// recreated on restart, and treating that window as "no IPv6" would release
	// the rule's IPv6 state — emptying a live table, exactly the leak Stage 3
	// removed for IPv4.
	capUnknown
)

// ifaceGlobalIPv6 reports whether an interface currently holds a global
// (non-link-local) IPv6 address, and whether that could be determined at all.
//
// Read live rather than cached: an interface that comes up, or a tunnel that
// gains an address, changes the answer, and reconciliation must see the change
// on its next pass instead of on the next restart.
//
// The second return value is the important one. A query that fails means the
// device is not there — during a restart every tunnel is briefly gone — which
// is not the same as a device that exists and has no IPv6 address. Only the
// latter is grounds for tearing the rule's IPv6 routing down.
func (m *Manager) ifaceGlobalIPv6(iface string) (hasV6, known bool) {
	if iface == "" {
		return false, true
	}
	out, err := sysRouteExec(fmt.Sprintf("ip -6 addr show dev %s scope global", iface), 5*time.Second, false)
	if err != nil {
		return false, false
	}
	return strings.Contains(out, "inet6"), true
}

// getSystemDefaultGatewayFor parses the host's default route for one family.
//
// The IPv6 answer is allowed to be "there isn't one": a host can have working
// IPv6 only through a tunnel and no default route of its own, which is not an
// error — it simply means a fallback has nothing to point at. The caller turns
// that into a fall-through instead of a route.
func (m *Manager) getSystemDefaultGatewayFor(f fam) (resolvedGW, bool, error) {
	if !f.v6 {
		gw, err := m.getSystemDefaultGateway()
		return gw, err == nil, err
	}
	out, err := sysRouteExec("ip -6 route show default", 5*time.Second, false)
	if err != nil {
		return resolvedGW{}, false, err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[0] == "default" && fields[1] == "via" && fields[3] == "dev" {
			return resolvedGW{gatewayIP: fields[2], iface: fields[4]}, true, nil
		}
		if len(fields) >= 3 && fields[0] == "default" && fields[1] == "dev" {
			return resolvedGW{iface: fields[2]}, true, nil
		}
	}
	return resolvedGW{}, false, nil
}

// ruleMatchesFamily reports whether a rule compiles into any match in one
// address family — the same question applyRuleKernelFamily asks before emitting
// chain entries, asked again so routing state is installed only where traffic
// can actually be marked.
//
// A rule that cannot be compiled at all (a deleted alias) matches nothing
// anywhere, which is the safe answer: it is not installed either.
func (m *Manager) ruleMatchesFamily(rule *Rule, f fam) bool {
	for dir, ep := range map[string]*Endpoint{"src": &rule.Source, "dst": &rule.Destination} {
		_, ok, err := m.buildMatchParts(dir, ep, f)
		if err != nil || !ok {
			return false
		}
	}
	return true
}
