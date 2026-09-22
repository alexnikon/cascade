//go:build linux && netns

package firewall

// Dual-stack end-to-end proof against a real kernel.
//
// Excluded from the normal build by the "netns" tag, because it needs root: run
// it on its own with
//
//	sudo -E go test -tags netns -run TestNetns ./internal/firewall/ -v
//
// Topology — three network namespaces joined by veth pairs, dual-stacked:
//
//	client                 router (Cascade)                  gateway
//	10.77.1.2/24   ──cl──  10.77.1.1/24 | 10.77.2.1/24  ──gw──  10.77.2.2/24
//	fd00:77:1::2/64        fd00:77:1::1 | fd00:77:2::1          fd00:77:2::2
//	                                    |
//	                                    └── 10.77.3.1/24 | fd00:77:3::1 ──dd── decoy
//	                                                                     10.77.3.2 | fd00:77:3::2
//
// The client's default route points at the router in both families and normally
// leaves through the decoy link. A Cascade firewall rule matching a domain alias
// must pull exactly the alias's addresses onto the gateway link instead — in
// both families — and leave everything else on the normal path.
//
// No seams are used at all. The outer test builds the topology and then re-runs
// itself inside the router namespace, so the firewall manager, the ipset manager
// and every command they issue execute exactly as they do in production — the
// router namespace simply is the host as far as Cascade can tell.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/gateway"
)

const netnsInnerEnv = "CASCADE_NETNS_INNER"

const (
	nsClient  = "csc-t-client"
	nsRouter  = "csc-t-router"
	nsGateway = "csc-t-gw"
	nsDecoy   = "csc-t-decoy"
)

// Addresses the alias resolves to — the destinations that must be policy-routed.
const (
	targetV4 = "10.77.9.9"
	targetV6 = "fd00:77:9::9"
	// A control destination that must stay on the normal path.
	controlV4 = "10.77.8.8"
	controlV6 = "fd00:77:8::8"
)

func sh(t *testing.T, format string, args ...any) string {
	t.Helper()
	cmd := fmt.Sprintf(format, args...)
	out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
	return string(out)
}

func shSoft(format string, args ...any) string {
	out, _ := exec.Command("bash", "-c", fmt.Sprintf(format, args...)).CombinedOutput()
	return string(out)
}

func inNS(t *testing.T, ns, format string, args ...any) string {
	t.Helper()
	return sh(t, "ip netns exec %s %s", ns, fmt.Sprintf(format, args...))
}

func requireNetns(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("network namespace test needs root")
	}
	for _, bin := range []string{"ip", "iptables-nft", "ip6tables-nft", "ipset"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
}

// buildTopology creates the namespaces and returns a cleanup function.
func buildTopology(t *testing.T) {
	t.Helper()
	teardown := func() {
		for _, ns := range []string{nsClient, nsRouter, nsGateway, nsDecoy} {
			shSoft("ip netns del %s 2>/dev/null", ns)
		}
	}
	teardown() // in case a previous run died mid-way
	t.Cleanup(teardown)

	for _, ns := range []string{nsClient, nsRouter, nsGateway, nsDecoy} {
		sh(t, "ip netns add %s", ns)
		inNS(t, ns, "ip link set lo up")
	}

	link := func(peerNS, ifRouter, ifPeer, v4Router, v4Peer, v6Router, v6Peer string) {
		sh(t, "ip link add %s netns %s type veth peer name %s netns %s", ifRouter, nsRouter, ifPeer, peerNS)
		inNS(t, nsRouter, "ip addr add %s dev %s", v4Router, ifRouter)
		inNS(t, nsRouter, "ip -6 addr add %s dev %s nodad", v6Router, ifRouter)
		inNS(t, nsRouter, "ip link set %s up", ifRouter)
		inNS(t, peerNS, "ip addr add %s dev %s", v4Peer, ifPeer)
		inNS(t, peerNS, "ip -6 addr add %s dev %s nodad", v6Peer, ifPeer)
		inNS(t, peerNS, "ip link set %s up", ifPeer)
	}

	link(nsClient, "cl-r", "cl-c", "10.77.1.1/24", "10.77.1.2/24", "fd00:77:1::1/64", "fd00:77:1::2/64")
	link(nsGateway, "gw-r", "gw-g", "10.77.2.1/24", "10.77.2.2/24", "fd00:77:2::1/64", "fd00:77:2::2/64")
	link(nsDecoy, "dd-r", "dd-d", "10.77.3.1/24", "10.77.3.2/24", "fd00:77:3::1/64", "fd00:77:3::2/64")

	// Router forwards.
	for _, s := range []string{
		"net.ipv4.ip_forward=1", "net.ipv6.conf.all.forwarding=1",
	} {
		inNS(t, nsRouter, "sysctl -qw %s", s)
	}

	// Router's normal (main-table) default goes out the decoy link.
	inNS(t, nsRouter, "ip route add default via 10.77.3.2 dev dd-r")
	inNS(t, nsRouter, "ip -6 route add default via fd00:77:3::2 dev dd-r")

	// Client defaults at the router.
	inNS(t, nsClient, "ip route add default via 10.77.1.1 dev cl-c")
	inNS(t, nsClient, "ip -6 route add default via fd00:77:1::1 dev cl-c")

	// Both the gateway and the decoy answer for the target and control
	// addresses, so which one replies tells us which path was taken.
	for _, ns := range []string{nsGateway, nsDecoy} {
		iface := "gw-g"
		if ns == nsDecoy {
			iface = "dd-d"
		}
		inNS(t, ns, "ip addr add %s/32 dev %s", targetV4, iface)
		inNS(t, ns, "ip -6 addr add %s/128 dev %s nodad", targetV6, iface)
		inNS(t, ns, "ip addr add %s/32 dev %s", controlV4, iface)
		inNS(t, ns, "ip -6 addr add %s/128 dev %s nodad", controlV6, iface)
		inNS(t, ns, "ip route add default via 10.77.%s.1", map[string]string{nsGateway: "2", nsDecoy: "3"}[ns])
		inNS(t, ns, "ip -6 route add default via fd00:77:%s::1", map[string]string{nsGateway: "2", nsDecoy: "3"}[ns])
	}

	// Per-destination counting rules in the gateway namespace. A rule with no
	// target only counts; it neither accepts nor drops.
	for _, c := range []struct {
		bin  string
		dsts []string
	}{
		{"iptables-nft", []string{targetV4, controlV4}},
		{"ip6tables-nft", []string{targetV6, controlV6}},
	} {
		inNS(t, nsGateway, "%s -t filter -N CSCCOUNT", c.bin)
		inNS(t, nsGateway, "%s -t filter -I INPUT 1 -j CSCCOUNT", c.bin)
		for _, d := range c.dsts {
			inNS(t, nsGateway, "%s -t filter -A CSCCOUNT -d %s", c.bin, d)
		}
	}

	// Let IPv6 DAD settle so the addresses are usable.
	time.Sleep(300 * time.Millisecond)
}

// arrivedAtGateway reports whether pings from the client reached the gateway
// namespace, read from a counting rule installed for that exact destination.
//
// Counting the destination rather than the interface matters: the gateway link
// carries neighbour discovery and replies of its own, so an interface counter
// would move whatever path the traffic took.
func arrivedAtGateway(t *testing.T, v6 bool, dst string) bool {
	t.Helper()
	before := gatewayCounter(t, v6, dst)
	ping := "ping"
	if v6 {
		ping = "ping -6"
	}
	shSoft("ip netns exec %s %s -c 2 -W 1 %s", nsClient, ping, dst)
	return gatewayCounter(t, v6, dst) > before
}

// gatewayCounter reads the packet count of the gateway namespace's counting
// rule for one destination.
func gatewayCounter(t *testing.T, v6 bool, dst string) int {
	t.Helper()
	bin := "iptables-nft"
	if v6 {
		bin = "ip6tables-nft"
	}
	out := inNS(t, nsGateway, "%s -t filter -L CSCCOUNT -n -v -x", bin)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// "<pkts> <bytes> [target] <prot> <opt> <in> <out> <source> <destination>"
		// A rule with no target leaves that column empty, so match on the tail.
		if len(fields) >= 8 && fields[len(fields)-1] == dst {
			var n int
			fmt.Sscanf(fields[0], "%d", &n)
			return n
		}
	}
	t.Fatalf("no counting rule for %s in:\n%s", dst, out)
	return 0
}

// TestNetnsDualStackDomainAliasPBR is the end-to-end proof: one Cascade rule,
// one domain alias, both address families policy-routed onto the gateway link
// while control traffic stays on the normal path.
func TestNetnsDualStackDomainAliasPBR(t *testing.T) {
	requireNetns(t)

	// Outer pass: build the topology, then re-run this same test inside the
	// router namespace so Cascade runs unmodified, with no exec seams at all.
	if os.Getenv(netnsInnerEnv) == "" {
		buildTopology(t)
		cmd := exec.Command("ip", "netns", "exec", nsRouter, os.Args[0],
			"-test.run", "^"+t.Name()+"$", "-test.v")
		cmd.Env = append(os.Environ(), netnsInnerEnv+"=1")
		out, err := cmd.CombinedOutput()
		t.Logf("inside %s:\n%s", nsRouter, out)
		if err != nil {
			t.Fatalf("test body failed inside the router namespace: %v", err)
		}
		return
	}

	m, am := initTestDB(t)

	// A domain alias, with its two sets populated directly — DNS is not what is
	// under test here, the ipset → MARK → ip rule → table path is.
	alias, err := am.Create(aliases.Alias{Name: "TESTnetns", Type: "domain", Entries: []string{"example.test"}})
	if err != nil {
		t.Fatalf("Create alias: %v", err)
	}
	v4Set, v6Set := aliases.DomainSetV4(alias.ID), aliases.DomainSetV6(alias.ID)
	sh(t, "ipset create %s hash:ip family inet  timeout 3600 -exist", v4Set)
	sh(t, "ipset create %s hash:ip family inet6 timeout 3600 -exist", v6Set)
	sh(t, "ipset add %s %s timeout 3600 -exist", v4Set, targetV4)
	sh(t, "ipset add %s %s timeout 3600 -exist", v6Set, targetV6)

	// A dual-stack gateway on the gateway link.
	gw, err := m.gm.CreateGateway(gateway.GatewayInput{
		Name: "TESTnetns-gw", Interface: "gw-r",
		GatewayIP: "10.77.2.2", MonitorAddress: "10.77.2.2",
		GatewayIPv6: "fd00:77:2::2", MonitorAddressV6: "fd00:77:2::2",
		Enabled: boolPtr(true), Monitor: boolPtr(false), MonitorInterval: 60,
		WindowSeconds: 30, MonitorRule: "icmp_only",
	})
	if err != nil {
		t.Fatalf("CreateGateway: %v", err)
	}
	t.Cleanup(func() { m.gm.Monitor().Stop(gw.ID) })

	// Monitoring is off, so health reads "unknown" — which must route normally,
	// not blackhole. Scripted here so the test does not depend on probe timing.
	old := monitorStatusFor
	monitorStatusFor = func(_ *Manager, _ string) gateway.MonitorStatus {
		return gateway.MonitorStatus{Status: "healthy", IPv6Status: "healthy", IPv6Source: "probe"}
	}
	t.Cleanup(func() { monitorStatusFor = old })

	rule, err := m.AddRule(RuleInput{
		Name: "TESTnetns-pbr", Action: "accept", GatewayID: gw.ID,
		Destination: Endpoint{Type: "alias", AliasID: alias.ID},
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.initChains(); err != nil {
		t.Fatalf("initChains: %v", err)
	}
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	mark := *rule.Fwmark

	// ── The compiled kernel state ────────────────────────────────────────────
	v4Mangle := sh(t, "iptables-nft -t mangle -S FIREWALL_MANGLE")
	v6Mangle := sh(t, "ip6tables-nft -t mangle -S FIREWALL_MANGLE")
	if !strings.Contains(v4Mangle, v4Set) {
		t.Errorf("IPv4 mangle does not match the v4 set:\n%s", v4Mangle)
	}
	for _, c := range []struct{ name, out string }{{"IPv4", v4Mangle}, {"IPv6", v6Mangle}} {
		if !strings.Contains(c.out, fmt.Sprintf("0x%x", mark)) {
			t.Errorf("%s mangle does not set fwmark %d:\n%s", c.name, mark, c.out)
		}
	}
	if !strings.Contains(v6Mangle, v6Set) {
		t.Errorf("IPv6 mangle does not match the v6 set:\n%s", v6Mangle)
	}
	if strings.Contains(v6Mangle, v4Set) {
		t.Errorf("IPv6 mangle references the IPv4 set:\n%s", v6Mangle)
	}

	v4Rules := sh(t, "ip rule show")
	v6Rules := sh(t, "ip -6 rule show")
	for _, c := range []struct{ name, out string }{{"ip rule", v4Rules}, {"ip -6 rule", v6Rules}} {
		if !strings.Contains(c.out, fmt.Sprintf("fwmark 0x%x lookup %d", mark, mark)) {
			t.Errorf("%s has no policy rule for fwmark %d:\n%s", c.name, mark, c.out)
		}
	}
	v4Table := sh(t, "ip route show table %d", mark)
	v6Table := sh(t, "ip -6 route show table %d", mark)
	if !strings.Contains(v4Table, "gw-r") {
		t.Errorf("IPv4 table %d = %q, want the gateway link", mark, v4Table)
	}
	if !strings.Contains(v6Table, "gw-r") {
		t.Errorf("IPv6 table %d = %q, want the gateway link", mark, v6Table)
	}

	// ── Real packets ─────────────────────────────────────────────────────────
	if !arrivedAtGateway(t, false, targetV4) {
		t.Error("IPv4 target was not policy-routed onto the gateway link")
	}
	if !arrivedAtGateway(t, true, targetV6) {
		t.Error("IPv6 target was not policy-routed onto the gateway link")
	}
	if arrivedAtGateway(t, false, controlV4) {
		t.Error("IPv4 control traffic was diverted; only alias destinations should be")
	}
	if arrivedAtGateway(t, true, controlV6) {
		t.Error("IPv6 control traffic was diverted; only alias destinations should be")
	}

	// ── Delete releases both families ────────────────────────────────────────
	if err := m.DeleteRule(rule.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if got := sh(t, "ip rule show"); strings.Contains(got, fmt.Sprintf("lookup %d", mark)) {
		t.Errorf("IPv4 policy rule survived delete:\n%s", got)
	}
	if got := sh(t, "ip -6 rule show"); strings.Contains(got, fmt.Sprintf("lookup %d", mark)) {
		t.Errorf("IPv6 policy rule survived delete:\n%s", got)
	}
	if got := strings.TrimSpace(sh(t, "ip route show table %d", mark)); got != "" {
		t.Errorf("IPv4 table %d survived delete: %q", mark, got)
	}
	if got := strings.TrimSpace(sh(t, "ip -6 route show table %d", mark)); got != "" {
		t.Errorf("IPv6 table %d survived delete: %q", mark, got)
	}

	// ── Deleting the alias destroys both sets without a restart ──────────────
	am.SetKernelRefsRebuilder(m.RebuildChains)
	if err := am.Delete(alias.ID); err != nil {
		t.Fatalf("Delete alias: %v", err)
	}
	sets := sh(t, "ipset list -n")
	if strings.Contains(sets, v4Set) || strings.Contains(sets, v6Set) {
		t.Errorf("alias sets survived deletion (the orphan bug):\n%s", sets)
	}
}
