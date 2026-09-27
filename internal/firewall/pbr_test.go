package firewall

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/db"
)

// pbrRecorder captures the kernel commands reconciliation would run and answers
// "ip rule show" from the policy rules it has seen, so the whole PBR lifecycle
// can be exercised through the existing exec seams without privileges.
type pbrRecorder struct {
	mu      sync.Mutex
	routes  []string
	rules   []string
	ipRules map[famObj]int    // fwmark+family → number of installed policy rules
	tables  map[famObj]string // table ID+family → the route currently installed
	// sysGW is the host default route per family. The IPv6 entry is empty by
	// default, which is the common real shape: a host with IPv6 only through a
	// tunnel has no IPv6 default route of its own.
	sysGW      map[bool]string
	v6Ifaces   map[string]bool // interfaces that carry a global IPv6 address
	failIfaces map[string]bool // interfaces whose address query fails (device gone)
	failNext   error
}

// famObj identifies a kernel object that exists once per address family.
type famObj struct {
	id int
	v6 bool
}

func newPBRRecorder(t *testing.T) *pbrRecorder {
	t.Helper()
	r := &pbrRecorder{
		ipRules:    map[famObj]int{},
		tables:     map[famObj]string{},
		sysGW:      map[bool]string{false: "default via 192.0.2.254 dev eth0"},
		v6Ifaces:   map[string]bool{},
		failIfaces: map[string]bool{},
	}

	oldRoute, oldRule, oldSys := pbrRouteExec, pbrRuleExec, sysRouteExec
	pbrRouteExec = func(cmd string, _ time.Duration, _ bool) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		v6 := isV6Command(cmd)
		body := stripFamily(cmd)
		table, hasTable := tableFromCommand(cmd)
		key := famObj{id: table, v6: v6}

		// Reads are answered from the recorded tables and are not writes.
		if strings.HasPrefix(body, "route show") {
			if hasTable {
				return r.tables[key], nil
			}
			return "", nil
		}
		if r.failNext != nil {
			err := r.failNext
			r.failNext = nil
			return "", err
		}
		r.routes = append(r.routes, cmd)
		if hasTable {
			if strings.HasPrefix(body, "route flush") {
				delete(r.tables, key)
			} else {
				r.tables[key] = cmd
			}
		}
		return "", nil
	}
	pbrRuleExec = func(cmd string, _ time.Duration, _ bool) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		v6 := isV6Command(cmd)
		body := stripFamily(cmd)
		switch {
		case body == "rule show":
			var sb strings.Builder
			for key, n := range r.ipRules {
				if key.v6 != v6 {
					continue
				}
				for i := 0; i < n; i++ {
					fmt.Fprintf(&sb, "1010:\tfrom all fwmark 0x%x lookup %d\n", key.id, key.id)
				}
			}
			return sb.String(), nil
		case strings.HasPrefix(body, "rule add"):
			r.rules = append(r.rules, cmd)
			if mark, ok := fwmarkFromCommand(cmd); ok {
				r.ipRules[famObj{id: mark, v6: v6}]++
			}
		case strings.HasPrefix(body, "rule del"):
			r.rules = append(r.rules, cmd)
			mark, ok := fwmarkFromCommand(cmd)
			key := famObj{id: mark, v6: v6}
			if !ok || r.ipRules[key] == 0 {
				return "", fmt.Errorf("RTNETLINK answers: No such file or directory")
			}
			r.ipRules[key]--
			if r.ipRules[key] == 0 {
				delete(r.ipRules, key)
			}
		}
		return "", nil
	}
	sysRouteExec = func(cmd string, _ time.Duration, _ bool) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		// "ip -6 addr show dev X scope global" — IPv6 capability of an interface.
		if strings.Contains(cmd, "addr show dev ") {
			iface := fieldAfter(cmd, "dev")
			if r.failIfaces[iface] {
				return "", fmt.Errorf(`Device "%s" does not exist.`, iface)
			}
			if r.v6Ifaces[iface] {
				return "    inet6 2001:db8::1/64 scope global", nil
			}
			return "", nil
		}
		return r.sysGW[isV6Command(cmd)], nil
	}
	t.Cleanup(func() { pbrRouteExec, pbrRuleExec, sysRouteExec = oldRoute, oldRule, oldSys })
	return r
}

// isV6Command reports whether a kernel command targets the IPv6 family.
func isV6Command(cmd string) bool { return strings.HasPrefix(cmd, "ip -6 ") }

// stripFamily removes the "ip"/"ip -6" prefix so the rest can be matched once.
func stripFamily(cmd string) string {
	return strings.TrimPrefix(strings.TrimPrefix(cmd, "ip -6 "), "ip ")
}

func fieldAfter(cmd, keyword string) string {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f == keyword && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func (r *pbrRecorder) policyRuleCount(fwmark int) int { return r.policyRuleCountFam(fwmark, famV4) }

func (r *pbrRecorder) policyRuleCountFam(fwmark int, f fam) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ipRules[famObj{id: fwmark, v6: f.v6}]
}

func (r *pbrRecorder) tableRoute(fwmark int) string { return r.tableRouteFam(fwmark, famV4) }

func (r *pbrRecorder) tableRouteFam(fwmark int, f fam) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tables[famObj{id: fwmark, v6: f.v6}]
}

// failIfaceQuery makes an interface look absent, as it is mid-restart.
func (r *pbrRecorder) failIfaceQuery(iface string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failIfaces[iface] = true
}

// markIPv6Capable makes an interface look like it carries a global IPv6 address.
func (r *pbrRecorder) markIPv6Capable(iface string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.v6Ifaces[iface] = true
}

func (r *pbrRecorder) setSysGW6(route string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sysGW[true] = route
}

func (r *pbrRecorder) routeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.routes)
}

func (r *pbrRecorder) lastRoute() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.routes) == 0 {
		return ""
	}
	return r.routes[len(r.routes)-1]
}

func tableFromCommand(cmd string) (int, bool)  { return trailingInt(cmd, "table") }
func fwmarkFromCommand(cmd string) (int, bool) { return trailingInt(cmd, "fwmark") }

func trailingInt(cmd, keyword string) (int, bool) {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f == keyword && i+1 < len(fields) {
			var v int
			if _, err := fmt.Sscanf(fields[i+1], "%d", &v); err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

// ── Bug 2: initial state must reflect current gateway health ──────────────────

func TestReconcile_GatewayAlreadyDown_FallbackEnabled(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "dead-fallback", "198.51.100.9", "eth9")

	// The gateway is down before the rule ever exists — no transition follows.
	m.gm.Monitor().SetAdminDown(gw.ID, true)

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-down-fallback", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: true,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	got := rec.tableRoute(*rule.Fwmark)
	if !strings.Contains(got, "via 192.0.2.254 dev eth0") {
		t.Errorf("table route = %q, want the system default (immediate fallback)", got)
	}
	if strings.Contains(got, "eth9") {
		t.Errorf("table route = %q, must not point at the down gateway", got)
	}
}

func TestReconcile_GatewayAlreadyDown_FallbackDisabled(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "dead-blackhole", "198.51.100.10", "eth10")
	m.gm.Monitor().SetAdminDown(gw.ID, true)

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-down-blackhole", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: false,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}

	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "blackhole default") {
		t.Errorf("table route = %q, want an immediate blackhole", got)
	}
}

// A gateway with no probe result yet must not be treated as down, or every rule
// would blackhole itself for the first seconds after startup.
func TestReconcile_UnknownGatewayHealthUsesGatewayRoute(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "fresh", "198.51.100.11", "eth11")

	rule, err := m.AddRule(RuleInput{Name: "TEST-unknown", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "dev eth11") {
		t.Errorf("table route = %q, want the gateway route while health is unknown", got)
	}
}

// Recovery still works, and it does so through the same reconciliation.
func TestReconcile_RecoveryRestoresGatewayRoute(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "flapper", "198.51.100.12", "eth12")
	m.gm.Monitor().SetAdminDown(gw.ID, true)

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-recovery", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: false,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "blackhole") {
		t.Fatalf("initial route = %q, want blackhole", got)
	}

	m.gm.Monitor().SetAdminDown(gw.ID, false)
	if err := m.restoreRoute(rule); err != nil {
		t.Fatalf("restoreRoute: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "dev eth12") {
		t.Errorf("recovered route = %q, want the gateway route", got)
	}

	// ...and back down again, without any special-casing.
	m.gm.Monitor().SetAdminDown(gw.ID, true)
	if err := m.reconcileRoutingForRule(rule); err != nil {
		t.Fatalf("reconcile after second outage: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "blackhole") {
		t.Errorf("route after second outage = %q, want blackhole again", got)
	}
}

// Startup restore: rebuildChains runs against the applied snapshot while the
// gateway is already down, and the very first resulting state must be correct.
func TestReconcile_RebuildWhileGatewayDownIsImmediatelyCorrect(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "down-at-boot", "198.51.100.13", "eth13")

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-restart", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: true,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}

	// Gateway goes down, then Cascade "restarts": fresh in-memory state, kernel
	// rebuilt from the applied snapshot, no monitor transition delivered.
	m.gm.Monitor().SetAdminDown(gw.ID, true)
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
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "via 192.0.2.254") {
		t.Errorf("post-restart route = %q, want fallback without waiting for a transition", got)
	}
}

// ── Idempotency ───────────────────────────────────────────────────────────────

func TestReconcile_IsIdempotent(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "steady", "198.51.100.14", "eth14")

	rule, err := m.AddRule(RuleInput{Name: "TEST-idempotent", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := m.reconcileRoutingForRule(rule); err != nil {
			t.Fatalf("reconcile #%d: %v", i, err)
		}
	}
	if n := rec.policyRuleCount(*rule.Fwmark); n != 1 {
		t.Errorf("policy rules for fwmark %d = %d, want exactly 1", *rule.Fwmark, n)
	}
	if n := rec.routeCount(); n != 1 {
		t.Errorf("route writes = %d, want the steady state written once", n)
	}
}

// ── Bug 1: delete must release the kernel state the rule owned ────────────────

func assertReleased(t *testing.T, rec *pbrRecorder, fwmark int) {
	t.Helper()
	if n := rec.policyRuleCount(fwmark); n != 0 {
		t.Errorf("policy rules for fwmark %d = %d, want none after delete", fwmark, n)
	}
	deleted, flushed := false, false
	rec.mu.Lock()
	for _, cmd := range rec.rules {
		if cmd == fmt.Sprintf("ip rule del fwmark %d lookup %d", fwmark, fwmark) {
			deleted = true
		}
	}
	for _, cmd := range rec.routes {
		if cmd == fmt.Sprintf("ip route flush table %d", fwmark) {
			flushed = true
		}
	}
	rec.mu.Unlock()
	if !deleted {
		t.Errorf("no policy rule deletion was issued for fwmark %d", fwmark)
	}
	if !flushed {
		t.Errorf("table %d was never flushed", fwmark)
	}
}

func TestDeleteRule_ReleasesPBRState(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "delete-me", "198.51.100.15", "eth15")

	rule, err := m.AddRule(RuleInput{Name: "TEST-stage3-delete", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	fwmark := *rule.Fwmark
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if rec.policyRuleCount(fwmark) != 1 {
		t.Fatalf("expected a policy rule for fwmark %d before delete", fwmark)
	}

	if err := m.DeleteRule(rule.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	assertReleased(t, rec, fwmark)
}

// applyToLocal changes which iptables hook carries the MARK, not who owns the
// routing state, so teardown must be identical.
func TestDeleteRule_ReleasesPBRStateWithApplyToLocal(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "delete-local", "198.51.100.16", "eth16")

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-stage3-delete-local", Action: "accept",
		GatewayID: gw.ID, ApplyToLocal: true,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if !rule.ApplyToLocal {
		t.Fatal("applyToLocal was not persisted — test would not prove anything")
	}
	fwmark := *rule.Fwmark
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if err := m.DeleteRule(rule.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	assertReleased(t, rec, fwmark)
}

// A blackholed rule must not leave its blackhole behind either.
func TestDeleteRule_ReleasesBlackholedRule(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "delete-blackhole", "198.51.100.17", "eth17")
	m.gm.Monitor().SetAdminDown(gw.ID, true)

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-stage3-delete-blackhole", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: false,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	fwmark := *rule.Fwmark
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if got := rec.tableRoute(fwmark); !strings.Contains(got, "blackhole") {
		t.Fatalf("route before delete = %q, want blackhole", got)
	}
	if err := m.DeleteRule(rule.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	assertReleased(t, rec, fwmark)
}

// Deleting a rule must not disturb a fwmark another rule still owns.
func TestDeleteRule_LeavesOtherRulesAlone(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "shared", "198.51.100.18", "eth18")

	keep, err := m.AddRule(RuleInput{Name: "TEST-keep", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule(keep): %v", err)
	}
	drop, err := m.AddRule(RuleInput{Name: "TEST-drop", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule(drop): %v", err)
	}
	if *keep.Fwmark == *drop.Fwmark {
		t.Fatalf("rules share fwmark %d — allocation is broken", *keep.Fwmark)
	}
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if err := m.DeleteRule(drop.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if n := rec.policyRuleCount(*keep.Fwmark); n != 1 {
		t.Errorf("surviving rule lost its policy rule (count = %d)", n)
	}
	assertReleased(t, rec, *drop.Fwmark)
}

// ── fwmark ownership ──────────────────────────────────────────────────────────

// A mark must not be handed out again while the applied snapshot still backs its
// routing table, or the new rule would inherit the old rule's routes.
func TestNextFwmark_DoesNotReuseMarkStillInAppliedSnapshot(t *testing.T) {
	m, _ := initTestDB(t)
	newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "reuse", "198.51.100.19", "eth19")

	first, err := m.AddRule(RuleInput{Name: "TEST-first", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}

	// Deleted from the draft, still present in the applied snapshot.
	if _, err := db.DB().Exec(`DELETE FROM firewall_rules WHERE id = ?`, first.ID); err != nil {
		t.Fatalf("delete draft row: %v", err)
	}

	next, err := m.nextFwmark()
	if err != nil {
		t.Fatalf("nextFwmark: %v", err)
	}
	if next == *first.Fwmark {
		t.Errorf("nextFwmark returned %d, which the applied snapshot still owns", next)
	}
}

func TestPBRMarksInUse_SpansBothTables(t *testing.T) {
	m, _ := initTestDB(t)
	newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "spans", "198.51.100.20", "eth20")

	rule, err := m.AddRule(RuleInput{Name: "TEST-span", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}

	used, err := pbrMarksInUse("")
	if err != nil {
		t.Fatalf("pbrMarksInUse: %v", err)
	}
	if !used[*rule.Fwmark] {
		t.Errorf("fwmark %d missing from the in-use set", *rule.Fwmark)
	}
	// Excluding the owner frees the mark — that is what makes delete safe.
	used, err = pbrMarksInUse(rule.ID)
	if err != nil {
		t.Fatalf("pbrMarksInUse(exclude): %v", err)
	}
	if used[*rule.Fwmark] {
		t.Errorf("fwmark %d still reported in use after excluding its only owner", *rule.Fwmark)
	}
}

// ── Desired-state derivation ──────────────────────────────────────────────────

func TestDesiredRoutingState_Table(t *testing.T) {
	m, _ := initTestDB(t)
	newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "matrix", "198.51.100.21", "eth21")

	mark := 1500
	base := func(fallback bool) *Rule {
		return &Rule{ID: "matrix", Name: "matrix", Fwmark: &mark, GatewayID: gw.ID, FallbackToDefault: fallback}
	}

	for _, tc := range []struct {
		name     string
		down     bool
		fallback bool
		want     pbrKind
	}{
		{"healthy", false, false, pbrGateway},
		{"healthy with fallback configured", false, true, pbrGateway},
		{"down without fallback", true, false, pbrBlackhole},
		{"down with fallback", true, true, pbrFallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.gm.Monitor().SetAdminDown(gw.ID, tc.down)
			got, err := m.desiredRoutingState(base(tc.fallback), famV4)
			if err != nil {
				t.Fatalf("desiredRoutingState: %v", err)
			}
			if got.kind != tc.want {
				t.Errorf("kind = %q, want %q", got.kind, tc.want)
			}
		})
	}
}

func TestRouteCommand_Shapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		desired pbrDesired
		want    string
	}{
		{"blackhole", pbrDesired{kind: pbrBlackhole}, "ip route replace blackhole default table 1001"},
		{"wireguard", pbrDesired{kind: pbrGateway, gw: resolvedGW{iface: "wg11", gatewayIP: "172.16.0.1"}},
			"ip route replace default dev wg11 table 1001"},
		{"amnezia", pbrDesired{kind: pbrGateway, gw: resolvedGW{iface: "awg0", gatewayIP: "192.0.2.1"}},
			"ip route replace default dev awg0 table 1001"},
		{"ethernet", pbrDesired{kind: pbrFallback, gw: resolvedGW{iface: "eth0", gatewayIP: "192.0.2.254"}},
			"ip route replace default via 192.0.2.254 dev eth0 onlink table 1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeCommand(tc.desired, 1001, famV4); got != tc.want {
				t.Errorf("routeCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

// ── Recovery that arrives without a down→up edge ──────────────────────────────

// Editing a gateway stops its monitor and starts a new one, so the first status
// it reports after an outage is "unknown" → "healthy". That is not a down→up
// edge, and under the old callback it left every affected rule stuck in
// fallback with no further event coming.
func TestHandleGatewayStatusChange_RecoversWithoutDownToUpEdge(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	m.restoreDelay = 10 * time.Millisecond
	gw := addPBRTestGateway(t, m.gm, "remonitored", "198.51.100.22", "eth22")

	m.gm.Monitor().SetAdminDown(gw.ID, true)
	rule, err := m.AddRule(RuleInput{
		Name: "TEST-remonitored", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: false,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "blackhole") {
		t.Fatalf("route = %q, want blackhole while down", got)
	}

	// The gateway is healthy again, but the monitor restarted, so the callback
	// reports unknown → healthy rather than down → healthy.
	m.gm.Monitor().SetAdminDown(gw.ID, false)
	if err := m.handleGatewayStatusChange(gw.ID, "healthy", "unknown"); err != nil {
		t.Fatalf("handleGatewayStatusChange: %v", err)
	}
	t.Cleanup(m.StopPendingRouteRestores)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.tableRoute(*rule.Fwmark), "dev eth22") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("route = %q, want the gateway route restored without a down→up edge",
		rec.tableRoute(*rule.Fwmark))
}

// A healthy probe every few seconds must not keep resetting the anti-flap timer,
// or the rule would never actually leave fallback.
func TestScheduleRouteRestore_RepeatedProbesDoNotStarveTheTimer(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	m.restoreDelay = 80 * time.Millisecond
	gw := addPBRTestGateway(t, m.gm, "chatty", "198.51.100.23", "eth23")

	m.gm.Monitor().SetAdminDown(gw.ID, true)
	rule, err := m.AddRule(RuleInput{
		Name: "TEST-chatty", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: false,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}
	m.gm.Monitor().SetAdminDown(gw.ID, false)
	t.Cleanup(m.StopPendingRouteRestores)

	// Probe far more often than the restore delay.
	stop := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(stop) {
		if err := m.handleGatewayStatusChange(gw.ID, "healthy", "healthy"); err != nil {
			t.Fatalf("handleGatewayStatusChange: %v", err)
		}
		if strings.Contains(rec.tableRoute(*rule.Fwmark), "dev eth23") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("route = %q, want restore to fire despite repeated healthy probes",
		rec.tableRoute(*rule.Fwmark))
}

// ── Rebuild must not empty the tables it is about to reinstall ────────────────

// A rebuild used to flush every owned table first, so matched traffic leaked to
// the main routing table until the route was rewritten — and if the write could
// not succeed yet (an interface still coming up after a restart) the table was
// simply left empty.
func TestCleanupRoutingRules_KeepsActiveRulesAndReleasesTheRest(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "keeper", "198.51.100.24", "eth24")

	active, err := m.AddRule(RuleInput{Name: "TEST-active", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule(active): %v", err)
	}
	disabled, err := m.AddRule(RuleInput{Name: "TEST-disabled", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule(disabled): %v", err)
	}
	// Apply it while it is still enabled, so its table really holds a route and
	// the cleanup below has something to release rather than a table that was
	// never populated.
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	if rec.tableRoute(*disabled.Fwmark) == "" {
		t.Fatalf("setup: the rule being disabled should have a route installed first")
	}
	if _, err := m.ToggleRule(disabled.ID, false); err != nil {
		t.Fatalf("ToggleRule: %v", err)
	}

	rec.mu.Lock()
	rec.routes = nil
	rec.mu.Unlock()

	// The rebuild inside ApplyRules is what runs cleanupRoutingRules.
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}

	rec.mu.Lock()
	var flushed []string
	for _, cmd := range rec.routes {
		if strings.HasPrefix(cmd, "ip route flush") {
			flushed = append(flushed, cmd)
		}
	}
	rec.mu.Unlock()

	wantFlush := fmt.Sprintf("ip route flush table %d", *disabled.Fwmark)
	dontFlush := fmt.Sprintf("ip route flush table %d", *active.Fwmark)
	if !contains(flushed, wantFlush) {
		t.Errorf("flushes = %v, want the disabled rule's table released", flushed)
	}
	if contains(flushed, dontFlush) {
		t.Errorf("flushes = %v, must not empty the active rule's table", flushed)
	}

	// The kernel state is the point: one table emptied, the other still routing.
	if got := rec.tableRoute(*disabled.Fwmark); got != "" {
		t.Errorf("disabled rule's table = %q, want it empty", got)
	}
	if got := rec.tableRoute(*active.Fwmark); !strings.Contains(got, "eth24") {
		t.Errorf("active rule's table = %q, want it still routing via the gateway", got)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// After a restart the applied snapshot is replayed. The rule's table must never
// be left empty, even when its gateway interface is not up yet and the route
// write fails.
func TestRebuild_FailedRouteWriteDoesNotEmptyTheTable(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "late-iface", "198.51.100.25", "eth25")

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-late", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: true,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.ApplyRules(); err != nil {
		t.Fatalf("ApplyRules: %v", err)
	}
	before := rec.tableRoute(*rule.Fwmark)
	if before == "" {
		t.Fatal("expected a route to be installed before the simulated restart")
	}

	// Simulated restart: the interface is not up, so the write fails.
	rec.mu.Lock()
	rec.failNext = fmt.Errorf(`exit status 1: Cannot find device "eth25"`)
	rec.mu.Unlock()
	m.routeStateMu.Lock()
	m.appliedState = map[stateKey]pbrDesired{}
	m.routeStateMu.Unlock()

	if err := m.rebuildChains(); err != nil {
		t.Fatalf("rebuildChains: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); got != before {
		t.Errorf("table route = %q, want the previous route %q preserved through a failed write", got, before)
	}
}

// Startup before the first probe: the table already holds the state that was
// correct the last time health was known, and a speculative gateway route must
// not overwrite it.
func TestReconcile_UnknownHealthKeepsExistingTableContents(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "boot", "198.51.100.26", "eth26")

	rule, err := m.AddRule(RuleInput{
		Name: "TEST-boot", Action: "accept",
		GatewayID: gw.ID, FallbackToDefault: false,
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	m.gm.Monitor().SetAdminDown(gw.ID, true)
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "blackhole") {
		t.Fatalf("route = %q, want blackhole", got)
	}

	// Restart: in-memory state is gone and the monitor has not reported yet.
	m.routeStateMu.Lock()
	m.appliedState = map[stateKey]pbrDesired{}
	m.routeStateMu.Unlock()
	m.gm.Monitor().Stop(gw.ID)

	if m.gm.Monitor().GetStatus(gw.ID).Status != "unknown" {
		t.Skip("monitor does not report unknown after Stop — nothing to assert")
	}
	if err := m.reconcileRoutingForRule(rule); err != nil {
		t.Fatalf("reconcile at boot: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "blackhole") {
		t.Errorf("route = %q, want the blackhole preserved until the first probe", got)
	}
}

// With nothing in the table there is no knowledge to preserve, so a brand-new
// rule still gets its gateway route straight away.
func TestReconcile_UnknownHealthStillInstallsIntoAnEmptyTable(t *testing.T) {
	m, _ := initTestDB(t)
	rec := newPBRRecorder(t)
	gw := addPBRTestGateway(t, m.gm, "brand-new", "198.51.100.27", "eth27")
	m.gm.Monitor().Stop(gw.ID)

	rule, err := m.AddRule(RuleInput{Name: "TEST-brand-new", Action: "accept", GatewayID: gw.ID})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := m.applyRoutingForRule(rule); err != nil {
		t.Fatalf("applyRoutingForRule: %v", err)
	}
	if got := rec.tableRoute(*rule.Fwmark); !strings.Contains(got, "dev eth27") {
		t.Errorf("route = %q, want the gateway route installed into an empty table", got)
	}
}
