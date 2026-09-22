package firewall

// Dual-stack compilation and the IPv6 half of the PBR lifecycle.
//
// Everything here runs through the same exec seams as the IPv4 tests, so no
// privileges and no kernel are required. The Linux end-to-end proof — real
// ip6tables-nft, real "ip -6 rule", real packets — lives in
// pbr_netns_linux_test.go behind a build tag.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/gateway"
)

// scriptHealth makes a gateway report the given per-family statuses.
func scriptHealth(t *testing.T, v4, v6 string) {
	t.Helper()
	old := monitorStatusFor
	monitorStatusFor = func(_ *Manager, _ string) gateway.MonitorStatus {
		return gateway.MonitorStatus{Status: v4, IPv6Status: v6, IPv6Source: "probe"}
	}
	t.Cleanup(func() { monitorStatusFor = old })
}

// dualStackGateway returns a gateway whose interface carries a global IPv6
// address, which is what makes it IPv6-capable without any extra configuration.
func dualStackGateway(t *testing.T, m *Manager, rec *pbrRecorder, name, ip, iface string) *gateway.Gateway {
	t.Helper()
	gw := addPBRTestGateway(t, m.gm, name, ip, iface)
	rec.markIPv6Capable(iface)
	return gw
}

// ── Family filtering: only valid matches are compiled ─────────────────────────

func TestBuildMatchParts_FamilyFiltering(t *testing.T) {
	m, am := initTestDB(t)

	mustAlias := func(a aliases.Alias) *aliases.Alias {
		t.Helper()
		out, err := am.Create(a)
		if err != nil {
			t.Fatalf("Create(%s): %v", a.Name, err)
		}
		return out
	}

	v4Only := mustAlias(aliases.Alias{Name: "v4nets", Type: "network", Entries: []string{"10.0.0.0/8", "192.168.0.0/16"}})
	v6Only := mustAlias(aliases.Alias{Name: "v6nets", Type: "network", Entries: []string{"2001:db8::/32"}})
	mixed := mustAlias(aliases.Alias{Name: "mixednets", Type: "network", Entries: []string{"10.0.0.0/8", "2001:db8::/32"}})
	domain := mustAlias(aliases.Alias{Name: "domains", Type: "domain", Entries: []string{"example.com"}})

	for _, tc := range []struct {
		name       string
		ep         Endpoint
		wantV4     []string
		wantV6     []string
		v4OK, v6OK bool
	}{
		{
			name:   "any matches everywhere",
			ep:     Endpoint{Type: "any"},
			wantV4: []string{""}, wantV6: []string{""}, v4OK: true, v6OK: true,
		},
		{
			name:   "IPv4 CIDR never reaches ip6tables",
			ep:     Endpoint{Type: "cidr", Value: "203.0.113.0/24"},
			wantV4: []string{"-d 203.0.113.0/24"}, v4OK: true, v6OK: false,
		},
		{
			name:   "IPv6 CIDR never reaches iptables",
			ep:     Endpoint{Type: "cidr", Value: "2001:db8::/32"},
			wantV6: []string{"-d 2001:db8::/32"}, v4OK: false, v6OK: true,
		},
		{
			name:   "IPv4-only alias produces no IPv6 rule",
			ep:     Endpoint{Type: "alias", AliasID: v4Only.ID},
			wantV4: []string{"-d 10.0.0.0/8", "-d 192.168.0.0/16"}, v4OK: true, v6OK: false,
		},
		{
			name:   "IPv6-only alias produces no IPv4 rule",
			ep:     Endpoint{Type: "alias", AliasID: v6Only.ID},
			wantV6: []string{"-d 2001:db8::/32"}, v4OK: false, v6OK: true,
		},
		{
			name:   "mixed alias is split, each family getting only its own entries",
			ep:     Endpoint{Type: "alias", AliasID: mixed.ID},
			wantV4: []string{"-d 10.0.0.0/8"}, wantV6: []string{"-d 2001:db8::/32"}, v4OK: true, v6OK: true,
		},
		{
			name:   "domain alias matches its own set in each family",
			ep:     Endpoint{Type: "alias", AliasID: domain.ID},
			wantV4: []string{fmt.Sprintf("-m set --match-set %s dst", aliases.DomainSetV4(domain.ID))},
			wantV6: []string{fmt.Sprintf("-m set --match-set %s dst", aliases.DomainSetV6(domain.ID))},
			v4OK:   true, v6OK: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, c := range []struct {
				f    fam
				want []string
				ok   bool
			}{{famV4, tc.wantV4, tc.v4OK}, {famV6, tc.wantV6, tc.v6OK}} {
				ep := tc.ep
				got, ok, err := m.buildMatchParts("dst", &ep, c.f)
				if err != nil {
					t.Fatalf("%s: buildMatchParts: %v", c.f.tag, err)
				}
				if ok != c.ok {
					t.Fatalf("%s: representable = %v, want %v (got parts %q)", c.f.tag, ok, c.ok, got)
				}
				if !ok {
					continue
				}
				if strings.Join(got, "|") != strings.Join(c.want, "|") {
					t.Errorf("%s: parts = %q, want %q", c.f.tag, got, c.want)
				}
			}
		})
	}
}

// An ipset alias is a hash:net family inet set: it has no IPv6 counterpart, so
// no IPv6 rule may reference it.
func TestBuildMatchParts_PlainIPSetAliasIsIPv4Only(t *testing.T) {
	m, am := initTestDB(t)
	a, err := am.Create(aliases.Alias{Name: "bignets", Type: "ipset"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ep := Endpoint{Type: "alias", AliasID: a.ID}
	if _, ok, err := m.buildMatchParts("dst", &ep, famV4); err != nil || !ok {
		t.Fatalf("IPv4: ok=%v err=%v, want it compiled", ok, err)
	}
	if parts, ok, err := m.buildMatchParts("dst", &ep, famV6); err != nil || ok {
		t.Fatalf("IPv6: ok=%v parts=%q err=%v, want it skipped", ok, parts, err)
	}
}

// A deleted alias must never widen a rule to "match everything".
func TestBuildMatchParts_MissingAliasIsRefused(t *testing.T) {
	m, _ := initTestDB(t)
	ep := Endpoint{Type: "alias", AliasID: "00000000-0000-0000-0000-000000000000"}
	for _, f := range families() {
		if _, _, err := m.buildMatchParts("dst", &ep, f); err == nil {
			t.Errorf("%s: want an error for a missing alias, not a match-anything rule", f.tag)
		}
	}
}

// "icmp" means a different protocol number in each family.
func TestCombosFor_ICMPBecomesICMPv6(t *testing.T) {
	in := []portCombo{{proto: "icmp"}, {proto: "tcp", dstPort: "443"}}

	v4 := combosFor(in, famV4)
	if v4[0].proto != "icmp" {
		t.Errorf("IPv4 proto = %q, want icmp", v4[0].proto)
	}
	v6 := combosFor(in, famV6)
	if v6[0].proto != "icmpv6" {
		t.Errorf("IPv6 proto = %q, want icmpv6", v6[0].proto)
	}
	if len(v6) != 2 || v6[1].proto != "tcp" || v6[1].dstPort != "443" {
		t.Errorf("IPv6 combos = %+v, want tcp/443 carried through unchanged", v6)
	}
}

// ── Route command shapes ──────────────────────────────────────────────────────

func TestRouteCommand_IPv6Shapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		desired pbrDesired
		want    string
	}{
		{"blackhole", pbrDesired{kind: pbrBlackhole}, "ip -6 route replace blackhole default table 1001"},
		{"wireguard device route", pbrDesired{kind: pbrGateway, gw: resolvedGW{iface: "wg11"}},
			"ip -6 route replace default dev wg11 table 1001"},
		{"explicit next hop", pbrDesired{kind: pbrGateway, gw: resolvedGW{iface: "eth0", gatewayIP: "2001:db8::1"}},
			"ip -6 route replace default via 2001:db8::1 dev eth0 onlink table 1001"},
		{"fallback with no IPv6 default falls through", pbrDesired{kind: pbrFallback},
			"ip -6 route flush table 1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeCommand(tc.desired, 1001, famV6); got != tc.want {
				t.Errorf("routeCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

// ── Gateway IPv6 capability ───────────────────────────────────────────────────

func TestGatewayRouteFor_Capability(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	rec.markIPv6Capable("wg11")

	v4Only := &gateway.Gateway{Interface: "eth0", GatewayIP: "192.0.2.1"}
	if _, c := m.gatewayRouteFor(v4Only, famV4); c != capYes {
		t.Error("every gateway must be IPv4-capable")
	}
	if got, c := m.gatewayRouteFor(v4Only, famV6); c != capNo {
		t.Errorf("IPv4-only gateway reported capability %v (%+v), want capNo", c, got)
	}

	tunnel := &gateway.Gateway{Interface: "wg11"}
	got, c := m.gatewayRouteFor(tunnel, famV6)
	if c != capYes {
		t.Fatalf("capability = %v, want capYes: a global IPv6 address makes the gateway IPv6-capable", c)
	}
	if got.iface != "wg11" || got.gatewayIP != "" {
		t.Errorf("route = %+v, want a device route on wg11", got)
	}

	explicit := &gateway.Gateway{Interface: "eth0", GatewayIP: "192.0.2.1", GatewayIPv6: "2001:db8::1"}
	got, c = m.gatewayRouteFor(explicit, famV6)
	if c != capYes || got.gatewayIP != "2001:db8::1" {
		t.Errorf("route = %+v cap=%v, want the configured IPv6 next hop", got, c)
	}

	// An interface that is not there right now is unknowable, not "no IPv6".
	rec.failIfaceQuery("wg11")
	if _, c := m.gatewayRouteFor(tunnel, famV6); c != capUnknown {
		t.Errorf("capability = %v while the interface is missing, want capUnknown", c)
	}
}

// ── Lifecycle: the Stage-3 guarantees, in IPv6 ────────────────────────────────

func TestReconcile_DualStackInstallsBothFamilies(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "dual", "198.51.100.30", "wg30")
	scriptHealth(t, "healthy", "healthy")

	rule := mustPBRRule(t, m, "TEST-dual", gw.ID, false)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	if got := rec.tableRouteFam(*rule.Fwmark, famV4); !strings.Contains(got, "ip route replace default dev wg30") {
		t.Errorf("IPv4 table = %q, want the gateway route", got)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); !strings.Contains(got, "ip -6 route replace default dev wg30") {
		t.Errorf("IPv6 table = %q, want the gateway route", got)
	}
	for _, f := range families() {
		if n := rec.policyRuleCountFam(*rule.Fwmark, f); n != 1 {
			t.Errorf("%s policy rules = %d, want exactly one", f.tag, n)
		}
	}
}

// An IPv4-only gateway must not acquire an IPv6 policy rule or a table full of
// routes pointing somewhere IPv6 cannot go.
func TestReconcile_IPv4OnlyGatewayInstallsNoIPv6State(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "v4only", "198.51.100.31", "eth31")
	scriptHealth(t, "healthy", "healthy")

	rule := mustPBRRule(t, m, "TEST-v4only", gw.ID, false)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	if got := rec.tableRouteFam(*rule.Fwmark, famV4); got == "" {
		t.Error("IPv4 table is empty, want the gateway route")
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); got != "" {
		t.Errorf("IPv6 table = %q, want nothing installed for an IPv4-only gateway", got)
	}
	if n := rec.policyRuleCountFam(*rule.Fwmark, famV6); n != 0 {
		t.Errorf("IPv6 policy rules = %d, want none", n)
	}
}

// Health that differs per family is the whole reason the model exists.
func TestReconcile_PerFamilyHealthDiverges(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	rec.setSysGW6("default via 2001:db8::fe dev eth0")
	gw := dualStackGateway(t, m, rec, "split", "198.51.100.32", "wg32")

	// IPv4 fine, IPv6 broken: only IPv6 falls back.
	scriptHealth(t, "healthy", "down")
	rule := mustPBRRule(t, m, "TEST-split", gw.ID, true)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	if got := rec.tableRouteFam(*rule.Fwmark, famV4); !strings.Contains(got, "dev wg32") {
		t.Errorf("IPv4 table = %q, want the gateway route — IPv4 is healthy", got)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); !strings.Contains(got, "via 2001:db8::fe") {
		t.Errorf("IPv6 table = %q, want the IPv6 fallback", got)
	}
}

func TestReconcile_IPv6AlreadyDown_Fallback(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	rec.setSysGW6("default via 2001:db8::fe dev eth0")
	gw := dualStackGateway(t, m, rec, "down6-fb", "198.51.100.33", "wg33")
	// Down before the rule ever exists — no transition will follow.
	scriptHealth(t, "down", "down")

	rule := mustPBRRule(t, m, "TEST-down6-fb", gw.ID, true)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	got := rec.tableRouteFam(*rule.Fwmark, famV6)
	if !strings.Contains(got, "ip -6 route replace default via 2001:db8::fe") {
		t.Errorf("IPv6 table = %q, want the system IPv6 default immediately", got)
	}
	if strings.Contains(got, "wg33") {
		t.Errorf("IPv6 table = %q, must not point at the down gateway", got)
	}
}

func TestReconcile_IPv6AlreadyDown_Blackhole(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "down6-bh", "198.51.100.34", "wg34")
	scriptHealth(t, "down", "down")

	rule := mustPBRRule(t, m, "TEST-down6-bh", gw.ID, false)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	if got := rec.tableRouteFam(*rule.Fwmark, famV6); got != "ip -6 route replace blackhole default table "+fmt.Sprint(*rule.Fwmark) {
		t.Errorf("IPv6 table = %q, want an IPv6 blackhole", got)
	}
	if n := rec.policyRuleCountFam(*rule.Fwmark, famV6); n != 1 {
		t.Errorf("IPv6 policy rules = %d, want one — the blackhole needs the rule to select it", n)
	}
}

// With no IPv6 default route on the host, "fall back to the default" means
// leaving the table empty so the kernel continues to the main table.
func TestReconcile_IPv6FallbackWithoutDefaultFallsThrough(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "nofb6", "198.51.100.35", "wg35")
	scriptHealth(t, "down", "down")

	rule := mustPBRRule(t, m, "TEST-nofb6", gw.ID, true)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	if got := rec.tableRouteFam(*rule.Fwmark, famV6); got != "" {
		t.Errorf("IPv6 table = %q, want it empty so lookup continues to main", got)
	}
}

func TestReconcile_IPv6RecoveryAndReFailure(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "recover6", "198.51.100.36", "wg36")

	scriptHealth(t, "healthy", "down")
	rule := mustPBRRule(t, m, "TEST-recover6", gw.ID, false)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); !strings.Contains(got, "blackhole") {
		t.Fatalf("setup: IPv6 table = %q, want blackhole", got)
	}

	scriptHealth(t, "healthy", "healthy")
	if err := m.reconcileRoutingForRule(rule); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); !strings.Contains(got, "default dev wg36") {
		t.Errorf("IPv6 table = %q, want the gateway route restored", got)
	}

	scriptHealth(t, "healthy", "down")
	if err := m.reconcileRoutingForRule(rule); err != nil {
		t.Fatalf("re-failure: %v", err)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); !strings.Contains(got, "blackhole") {
		t.Errorf("IPv6 table = %q, want the blackhole back", got)
	}
}

// A restart reconciles from the applied snapshot and live health; the first
// state it produces must already be right in both families.
func TestReconcile_IPv6RebuildWhileDownIsImmediatelyCorrect(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	rec.setSysGW6("default via 2001:db8::fe dev eth0")
	gw := dualStackGateway(t, m, rec, "restart6", "198.51.100.37", "wg37")
	scriptHealth(t, "healthy", "healthy")

	rule := mustPBRRule(t, m, "TEST-restart6", gw.ID, true)
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}

	// The gateway loses IPv6, then Cascade "restarts": in-memory state gone,
	// kernel rebuilt from the snapshot, no monitor transition delivered.
	scriptHealth(t, "healthy", "down")
	m.routeStateMu.Lock()
	m.activeGateway = map[stateKey]resolvedGW{}
	m.appliedState = map[stateKey]pbrDesired{}
	m.routeStateMu.Unlock()
	m.fallbackMu.Lock()
	m.fallbackActive = map[stateKey]bool{}
	m.fallbackMu.Unlock()

	if err := m.rebuildChains(); err != nil {
		t.Fatalf("rebuildChains: %v", err)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); !strings.Contains(got, "via 2001:db8::fe") {
		t.Errorf("post-restart IPv6 route = %q, want fallback without waiting for a transition", got)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV4); !strings.Contains(got, "dev wg37") {
		t.Errorf("post-restart IPv4 route = %q, want it untouched — IPv4 is still healthy", got)
	}
}

func TestReconcile_DualStackIsIdempotent(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "idem6", "198.51.100.38", "wg38")
	scriptHealth(t, "healthy", "healthy")

	rule := mustPBRRule(t, m, "TEST-idem6", gw.ID, false)
	for i := 0; i < 4; i++ {
		if err := m.reconcileRoutingForRule(rule); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	for _, f := range families() {
		if n := rec.policyRuleCountFam(*rule.Fwmark, f); n != 1 {
			t.Errorf("%s policy rules = %d after four reconciliations, want exactly one", f.tag, n)
		}
	}
	// Two writes total: one per family, on the first pass.
	if n := rec.routeCount(); n != 2 {
		t.Errorf("route writes = %d, want one per family and none after", n)
	}
}

// ── Deletion releases both families ───────────────────────────────────────────

func TestDeleteRule_ReleasesBothFamilies(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "del6", "198.51.100.39", "wg39")
	scriptHealth(t, "healthy", "healthy")

	rule := mustPBRRule(t, m, "TEST-del6", gw.ID, false)
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	for _, f := range families() {
		if rec.tableRouteFam(*rule.Fwmark, f) == "" || rec.policyRuleCountFam(*rule.Fwmark, f) != 1 {
			t.Fatalf("setup: %s state missing before delete", f.tag)
		}
	}

	if err := m.DeleteRule(rule.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}

	// No apply, no restart: the kernel state goes with the rule.
	for _, f := range families() {
		if got := rec.tableRouteFam(*rule.Fwmark, f); got != "" {
			t.Errorf("%s table = %q after delete, want it empty", f.tag, got)
		}
		if n := rec.policyRuleCountFam(*rule.Fwmark, f); n != 0 {
			t.Errorf("%s policy rules = %d after delete, want none", f.tag, n)
		}
	}
}

func TestDeleteRule_LeavesTheOtherRulesIPv6StateAlone(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "neighbour6", "198.51.100.40", "wg40")
	scriptHealth(t, "healthy", "healthy")

	doomed := mustPBRRule(t, m, "TEST-doomed6", gw.ID, false)
	keeper := mustPBRRule(t, m, "TEST-keeper6", gw.ID, false)
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if *doomed.Fwmark == *keeper.Fwmark {
		t.Fatal("setup: the two rules must own different fwmarks")
	}

	if err := m.DeleteRule(doomed.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}

	if got := rec.tableRouteFam(*keeper.Fwmark, famV6); !strings.Contains(got, "dev wg40") {
		t.Errorf("surviving rule's IPv6 table = %q, want it untouched", got)
	}
	if n := rec.policyRuleCountFam(*keeper.Fwmark, famV6); n != 1 {
		t.Errorf("surviving rule's IPv6 policy rules = %d, want it untouched", n)
	}
}

// A gateway that loses its IPv6 address must not keep routing IPv6 into it.
func TestReconcile_LosingIPv6CapabilityReleasesIPv6State(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "lose6", "198.51.100.41", "wg41")
	scriptHealth(t, "healthy", "healthy")

	rule := mustPBRRule(t, m, "TEST-lose6", gw.ID, false)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}
	if rec.tableRouteFam(*rule.Fwmark, famV6) == "" {
		t.Fatal("setup: IPv6 route should be installed first")
	}

	rec.mu.Lock()
	delete(rec.v6Ifaces, "wg41")
	rec.mu.Unlock()

	if err := m.reconcileRoutingForRule(rule); err != nil {
		t.Fatalf("reconcile after losing IPv6: %v", err)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); got != "" {
		t.Errorf("IPv6 table = %q, want it released", got)
	}
	if n := rec.policyRuleCountFam(*rule.Fwmark, famV6); n != 0 {
		t.Errorf("IPv6 policy rules = %d, want them removed", n)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV4); !strings.Contains(got, "dev wg41") {
		t.Errorf("IPv4 table = %q, want it unaffected", got)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func mustPBRRule(t *testing.T, m *Manager, name, gatewayID string, fallback bool) *Rule {
	t.Helper()
	r, err := m.AddRule(RuleInput{
		Name: name, Action: "accept", GatewayID: gatewayID, FallbackToDefault: fallback,
	})
	if err != nil {
		t.Fatalf("AddRule(%s): %v", name, err)
	}
	return r
}

// Routing state is installed only where the rule can actually mark traffic. A
// rule whose endpoints are IPv4-only never marks an IPv6 packet, so an IPv6
// policy rule and table for it would sit there inert.
func TestReconcile_IPv4OnlyRuleGetsNoIPv6RoutingState(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "v4rule", "198.51.100.42", "wg42")
	scriptHealth(t, "healthy", "healthy")

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-v4rule", Action: "accept", GatewayID: gw.ID,
		Source: Endpoint{Type: "cidr", Value: "10.99.0.0/24"},
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	if got := rec.tableRouteFam(*rule.Fwmark, famV4); !strings.Contains(got, "dev wg42") {
		t.Errorf("IPv4 table = %q, want the gateway route", got)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV6); got != "" {
		t.Errorf("IPv6 table = %q, want nothing — the rule cannot match IPv6", got)
	}
	if n := rec.policyRuleCountFam(*rule.Fwmark, famV6); n != 0 {
		t.Errorf("IPv6 policy rules = %d, want none", n)
	}
}

// The mirror image: a rule whose endpoints are IPv6-only routes IPv6 and leaves
// IPv4 alone.
func TestReconcile_IPv6OnlyRuleGetsNoIPv4RoutingState(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := dualStackGateway(t, m, rec, "v6rule", "198.51.100.43", "wg43")
	scriptHealth(t, "healthy", "healthy")

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-v6rule", Action: "accept", GatewayID: gw.ID,
		Destination: Endpoint{Type: "cidr", Value: "2001:db8:1::/48"},
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	if got := rec.tableRouteFam(*rule.Fwmark, famV6); !strings.Contains(got, "dev wg43") {
		t.Errorf("IPv6 table = %q, want the gateway route", got)
	}
	if got := rec.tableRouteFam(*rule.Fwmark, famV4); got != "" {
		t.Errorf("IPv4 table = %q, want nothing — the rule cannot match IPv4", got)
	}
	if n := rec.policyRuleCountFam(*rule.Fwmark, famV4); n != 0 {
		t.Errorf("IPv4 policy rules = %d, want none", n)
	}
}
