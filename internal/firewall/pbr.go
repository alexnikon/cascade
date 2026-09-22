package firewall

// PBR routing lifecycle.
//
// Ownership model
//
//	A PBR rule owns exactly one fwmark, and the routing table ID is the same
//	integer as the fwmark (table N ⇔ fwmark N). The fwmark is persisted on the
//	rule row, so a rule's kernel identity survives restarts and is still known
//	at delete time. The policy rule is "ip rule fwmark N lookup N" at priority
//	1000 + order*10.
//
//	The identity spans both address families: one rule, one fwmark, and table N
//	in each family. IPv4 and IPv6 routing tables are separate kernel namespaces
//	— "ip route show table 1000" and "ip -6 route show table 1000" are unrelated
//	objects — so reusing the number is safe and keeps one rule readable as one
//	number everywhere:
//
//	    ip    rule fwmark N lookup N   →  ip    route ... table N
//	    ip -6 rule fwmark N lookup N   →  ip -6 route ... table N
//
//	The packet mark is shared, which is what makes the two halves one rule: the
//	same MARK value is set by iptables-nft and by ip6tables-nft, and each family
//	then selects its own table N.
//
// Desired state
//
//	Kernel routing state is a pure function of persisted configuration plus
//	current gateway health:
//
//	    desiredRoutingState(rule) -> pbrDesired{gateway | fallback | blackhole}
//
//	reconcileRoutingForRule makes the kernel match that. Every path — initial
//	apply, chain rebuild, startup restore, gateway down, gateway recovery,
//	gateway-group failover — goes through it, so none of them can leave the
//	kernel in a state that only a future monitor transition would correct.
//
//	Reconciliation is idempotent: "ip route replace" overwrites in place and the
//	policy rule is only added when absent.

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/gateway"
	"github.com/alexnikon/cascade/internal/util"
)

// pbrKind is which of the three routing outcomes a rule currently wants.
type pbrKind string

const (
	pbrGateway   pbrKind = "gateway"   // gateway healthy → route via it
	pbrFallback  pbrKind = "fallback"  // gateway down, fallbackToDefault → system default
	pbrBlackhole pbrKind = "blackhole" // gateway down, no fallback → drop
	// pbrUnsupported: this family has no usable route for the rule — the
	// gateway cannot carry it, or the rule cannot match in it. No policy rule
	// and no table contents; matching traffic (if any) uses the main table.
	pbrUnsupported pbrKind = "unsupported"
)

// pbrDesired is the routing state a rule should currently have in one family.
type pbrDesired struct {
	kind pbrKind
	gw   resolvedGW // set for pbrGateway, and for pbrFallback when a default exists
}

// stateKey identifies one rule's routing state within one address family.
// Everything the manager remembers about installed routing is keyed by it, so
// the two families can be in different states — which is the normal case for a
// gateway that is healthy over IPv4 and broken over IPv6.
type stateKey struct {
	ruleID string
	v6     bool
}

func keyFor(ruleID string, f fam) stateKey { return stateKey{ruleID: ruleID, v6: f.v6} }

// sysRouteExec reads the system routing table. Separate from the write seams so
// tests can script "ip route show default" without privileges.
var sysRouteExec = util.Exec

// gatewayIsDown reports whether a directly-referenced gateway is currently
// unhealthy. Only "down" and "admin_down" count — "unknown" must NOT blackhole a
// rule, which matches the semantics already used by isGroupAllDown.
func (m *Manager) gatewayIsDown(gatewayID string, f fam) bool {
	return isDownStatus(m.gatewayFamilyStatus(gatewayID, f))
}

func isDownStatus(s string) bool { return s == "down" || s == "admin_down" }

// gatewayFamilyStatus returns the health of a gateway in one address family.
//
// IPv4 is the gateway's combined status, exactly as before. IPv6 is whatever
// MonitorStatus reports for IPv6, which is either a real ICMPv6 probe result or
// the IPv4 result explicitly labelled as inherited — the monitor decides, and
// the distinction is visible in the API rather than buried here.
func (m *Manager) gatewayFamilyStatus(gatewayID string, f fam) string {
	st := monitorStatusFor(m, gatewayID)
	if f.v6 {
		return st.IPv6Status
	}
	return st.Status
}

// monitorStatusFor reads a gateway's live health.
//
// A package variable for the same reason as the exec seams above: the two
// families holding different statuses is the behaviour worth testing, and the
// only way to produce it for real is a live kernel with a reachable ICMPv6
// target. Tests script it; nothing else replaces it.
var monitorStatusFor = func(m *Manager, gatewayID string) gateway.MonitorStatus {
	if m.gm == nil || m.gm.Monitor() == nil {
		return gateway.MonitorStatus{Status: "unknown", IPv6Status: "unknown"}
	}
	return m.gm.Monitor().GetStatus(gatewayID)
}

// gatewayHealthUnknown reports that no probe has produced a result yet, so this
// rule's health genuinely cannot be evaluated. True only for a rule bound to a
// single gateway; a group is judged by its members and resolves on its own.
func (m *Manager) gatewayHealthUnknown(rule *Rule, f fam) bool {
	if rule.GatewayID == "" || m.gm == nil || m.gm.Monitor() == nil {
		return false
	}
	return m.gatewayFamilyStatus(rule.GatewayID, f) == "unknown"
}

// tableHasDefaultRoute reports whether a routing table already holds a default
// route — a gateway route, a fallback, or a blackhole.
func (m *Manager) tableHasDefaultRoute(table int, f fam) bool {
	return strings.Contains(m.tableContents(table, f), "default")
}

// tableContents reads one routing table in one family.
func (m *Manager) tableContents(table int, f fam) string {
	out, err := pbrRouteExec(fmt.Sprintf("%s route show table %d", f.ipr, table), 5*time.Second, false)
	if err != nil {
		return ""
	}
	return out
}

// desiredRoutingState derives the routing state a rule should have right now in
// one address family, from its configuration and the live health of its gateway
// or gateway group.
//
// The four outcomes:
//
//	gateway     — healthy, and the gateway can carry this family
//	fallback    — unhealthy with fallbackToDefault, route via the system default
//	blackhole   — unhealthy without fallbackToDefault, drop
//	unsupported — the gateway has no route in this family at all
//
// "unsupported" is what keeps a bogus IPv6 route out of the kernel. An IPv4-only
// gateway never gets "ip -6 route ... dev <iface>" pointed at it just because the
// interface exists, and the rule is left with no IPv6 policy rule, so matching
// IPv6 traffic keeps using the main table instead of vanishing into a dead one.
func (m *Manager) desiredRoutingState(rule *Rule, f fam) (pbrDesired, error) {
	unhealthy := func() (pbrDesired, error) {
		if !rule.FallbackToDefault {
			return pbrDesired{kind: pbrBlackhole}, nil
		}
		gw, ok, err := m.getSystemDefaultGatewayFor(f)
		if err != nil {
			return pbrDesired{}, fmt.Errorf("fallback: %w", err)
		}
		if !ok {
			// No default route in this family. A host can have working IPv6
			// only through a tunnel; "use the system default" then means an
			// empty table, which the kernel falls through to main on.
			return pbrDesired{kind: pbrFallback}, nil
		}
		return pbrDesired{kind: pbrFallback, gw: gw}, nil
	}

	// Capability first: an unhealthy gateway that could never carry this family
	// has nothing to fall back from.
	gw, err := m.resolveGatewayObj(rule)
	if err != nil {
		return pbrDesired{}, err
	}
	route, capable := m.gatewayRouteFor(gw, f)
	if !capable {
		return pbrDesired{kind: pbrUnsupported}, nil
	}

	switch {
	case rule.GatewayGroupID != "":
		allDown, err := m.isGroupAllDown(rule.GatewayGroupID, f)
		if err != nil {
			return pbrDesired{}, err
		}
		if allDown {
			return unhealthy()
		}
	case rule.GatewayID != "":
		if m.gatewayIsDown(rule.GatewayID, f) {
			return unhealthy()
		}
	default:
		return pbrDesired{}, fmt.Errorf("rule has no gateway or gateway group")
	}

	return pbrDesired{kind: pbrGateway, gw: route}, nil
}

// routeCommand renders the routing command for a desired state in one family.
// WireGuard interfaces need a device-only route; everything else gets an on-link
// next hop. The only difference between the families is the "ip -6" prefix —
// "blackhole default" and "via ... dev ... onlink" are spelled the same way in
// both.
//
// A fallback with no next hop (no system default in this family) and an
// unsupported family both flush the table: the policy rule then finds nothing
// and the kernel continues to the main table.
func routeCommand(d pbrDesired, table int, f fam) string {
	switch {
	case d.kind == pbrBlackhole:
		return fmt.Sprintf("%s route replace blackhole default table %d", f.ipr, table)
	case d.gw.iface == "":
		return fmt.Sprintf("%s route flush table %d", f.ipr, table)
	}
	isWG := strings.HasPrefix(d.gw.iface, "wg") || strings.HasPrefix(d.gw.iface, "awg")
	if isWG || d.gw.gatewayIP == "" {
		return fmt.Sprintf("%s route replace default dev %s table %d", f.ipr, d.gw.iface, table)
	}
	return fmt.Sprintf("%s route replace default via %s dev %s onlink table %d",
		f.ipr, d.gw.gatewayIP, d.gw.iface, table)
}

// reconcileRoutingForRule makes the kernel match the rule's desired state.
//
// This is the single place that writes PBR routing state. It is idempotent, so
// callers may invoke it as often as they like — on create, on apply, on chain
// rebuild, on startup restore, and from the gateway monitor callbacks.
func (m *Manager) reconcileRoutingForRule(rule *Rule) error {
	if rule.Fwmark == nil {
		return nil
	}
	return m.withRuleApply(rule.ID, func() error {
		var firstErr error
		for _, f := range families() {
			desired, err := m.desiredRoutingState(rule, f)
			if err == nil {
				err = m.applyDesiredLocked(rule, desired, f)
			}
			// One family failing must not leave the other unreconciled: a host
			// with no IPv6 at all would otherwise stop IPv4 from converging.
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", f.tag, err)
			}
		}
		return firstErr
	})
}

// applyDesiredLocked writes one desired state to the kernel and syncs the
// in-memory bookkeeping. The caller must already hold the rule's apply lock.
//
// The route and the policy rule are tracked independently. The route write is
// skipped when this manager already put the rule in exactly that state, so a
// burst of monitor callbacks for one transition does not churn the kernel;
// anything that invalidates that knowledge — a chain rebuild, a release, a
// restart — clears the record and the next reconciliation writes again. The
// policy rule is added whenever it is missing, regardless.
func (m *Manager) applyDesiredLocked(rule *Rule, desired pbrDesired, f fam) error {
	fwmark := *rule.Fwmark
	key := keyFor(rule.ID, f)

	// A family the gateway cannot carry gets nothing at all: no policy rule, no
	// table contents. Releasing rather than skipping matters on a change — a
	// gateway that loses its IPv6 address must not keep routing IPv6 into a
	// tunnel that can no longer carry it.
	if desired.kind == pbrUnsupported {
		m.releasePBRStateFamily(fwmark, f)
		m.routeStateMu.Lock()
		m.appliedState[key] = desired
		delete(m.activeGateway, key)
		m.routeStateMu.Unlock()
		m.fallbackMu.Lock()
		delete(m.fallbackActive, key)
		m.fallbackMu.Unlock()
		return nil
	}

	ruleInstalled := m.ipRuleExists(fwmark, f)

	m.routeStateMu.Lock()
	last, known := m.appliedState[key]
	m.routeStateMu.Unlock()

	// Startup, before the first probe: health is genuinely unknown, so a route
	// written now would be a guess. Whatever the kernel already holds came from
	// the last time health *was* known, which is a strictly better answer — keep
	// it and let the first probe reconcile. Only a table with nothing in it gets
	// the provisional gateway route, since there is no knowledge to preserve.
	addPolicyRule := func() error {
		if ruleInstalled {
			return nil
		}
		priority := 1000 + rule.Order*10
		cmd := fmt.Sprintf("%s rule add fwmark %d lookup %d priority %d", f.ipr, fwmark, fwmark, priority)
		if _, err := pbrRuleExec(cmd, 10*time.Second, true); err != nil {
			return fmt.Errorf("%s rule add: %w", f.ipr, err)
		}
		return nil
	}

	if !known && m.gatewayHealthUnknown(rule, f) && m.tableHasDefaultRoute(fwmark, f) {
		if err := addPolicyRule(); err != nil {
			return err
		}
		log.Printf("firewall: rule %q: %s gateway health not yet known — keeping the existing table %d route",
			rule.Name, f.tag, fwmark)
		return nil
	}

	if !known || last != desired {
		if _, err := pbrRouteExec(routeCommand(desired, fwmark, f), 10*time.Second, true); err != nil {
			return fmt.Errorf("%s route replace: %w", f.ipr, err)
		}
	}

	// Exactly one policy rule per fwmark, per family.
	if err := addPolicyRule(); err != nil {
		return err
	}

	m.routeStateMu.Lock()
	m.appliedState[key] = desired
	if desired.kind == pbrGateway {
		m.activeGateway[key] = desired.gw
	} else {
		delete(m.activeGateway, key)
	}
	m.routeStateMu.Unlock()

	m.fallbackMu.Lock()
	was := m.fallbackActive[key]
	if desired.kind == pbrGateway {
		delete(m.fallbackActive, key)
	} else {
		m.fallbackActive[key] = true
	}
	m.fallbackMu.Unlock()

	// Log transitions only, so steady-state reconciliation stays quiet.
	switch {
	case desired.kind == pbrFallback && !was && desired.gw.iface == "":
		log.Printf("firewall: rule %q: %s fallback → main table (no %s default route)",
			rule.Name, f.tag, f.tag)
	case desired.kind == pbrFallback && !was:
		log.Printf("firewall: rule %q: %s fallback → default via %s dev %s (gateway unavailable)",
			rule.Name, f.tag, desired.gw.gatewayIP, desired.gw.iface)
	case desired.kind == pbrBlackhole && !was:
		log.Printf("firewall: rule %q: %s blackhole ACTIVE (gateway unavailable)", rule.Name, f.tag)
	case desired.kind == pbrGateway && was:
		log.Printf("firewall: rule %q: %s gateway route restored via %s", rule.Name, f.tag, desired.gw.iface)
	}
	return nil
}

// ── Ownership and teardown ────────────────────────────────────────────────────

// pbrMarksInUse returns every fwmark claimed by a rule in the draft table or in
// the applied snapshot, optionally ignoring one rule ID.
//
// Both tables matter. The kernel is built from the applied snapshot, while new
// allocations and deletions happen in the draft, so a mark is only free when
// neither table claims it. Reading just one of them is what previously let a
// deleted rule's routing state survive, and what let a fresh rule reuse a mark
// whose table still held the old rule's route.
func pbrMarksInUse(excludeRuleID string) (map[int]bool, error) {
	used := make(map[int]bool)
	for _, table := range []string{"firewall_rules", "firewall_rules_applied"} {
		rows, err := db.DB().Query(
			fmt.Sprintf(`SELECT fwmark FROM %s WHERE fwmark IS NOT NULL AND id != ?`, table), excludeRuleID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v int
			if rows.Scan(&v) == nil {
				used[v] = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return used, nil
}

// maxDuplicatePolicyRules bounds the delete loop in releasePBRState. A fwmark
// should only ever have one policy rule; the loop exists so that duplicates left
// behind by an older version or an interrupted rebuild are cleaned up too.
const maxDuplicatePolicyRules = 8

// releasePBRState removes every kernel object Cascade owns for one fwmark: all
// policy rules selecting its table, and the contents of the table itself.
//
// Only Cascade-owned state is touched — the commands are scoped to the exact
// "fwmark N lookup N" pair this manager allocates, and to table N, which is
// Cascade's by construction (see the ownership model above). Unrelated policy
// rules and system tables are never enumerated or deleted.
func (m *Manager) releasePBRState(fwmark int) {
	for _, f := range families() {
		m.releasePBRStateFamily(fwmark, f)
	}
}

// releasePBRStateFamily releases one fwmark's kernel objects in one family.
//
// Reads before writing: with two families, most marks only ever exist in one of
// them, and flushing a table that was never populated would be a pointless write
// on every rebuild.
func (m *Manager) releasePBRStateFamily(fwmark int, f fam) {
	if !m.ipRuleExists(fwmark, f) && strings.TrimSpace(m.tableContents(fwmark, f)) == "" {
		return
	}
	for i := 0; i < maxDuplicatePolicyRules; i++ {
		if !m.ipRuleExists(fwmark, f) {
			break
		}
		cmd := fmt.Sprintf("%s rule del fwmark %d lookup %d", f.ipr, fwmark, fwmark)
		if _, err := pbrRuleExec(cmd, 5*time.Second, true); err != nil {
			break
		}
	}
	// Removes the gateway route, a fallback default, or a blackhole alike.
	pbrRouteExec(fmt.Sprintf("%s route flush table %d", f.ipr, fwmark), 5*time.Second, true) //nolint:errcheck
}

// releaseRoutingForRule tears down the kernel routing state owned by a rule that
// is going away. It is a no-op when the rule never had PBR state, and it refuses
// to touch a fwmark that another rule still claims.
func (m *Manager) releaseRoutingForRule(rule *Rule) {
	if rule == nil || rule.Fwmark == nil {
		return
	}
	fwmark := *rule.Fwmark

	used, err := pbrMarksInUse(rule.ID)
	if err != nil {
		log.Printf("firewall: releaseRoutingForRule %q: %v — leaving fwmark %d in place", rule.Name, err, fwmark)
		return
	}
	if used[fwmark] {
		log.Printf("firewall: rule %q deleted, but fwmark %d is still claimed by another rule — routing state kept",
			rule.Name, fwmark)
		return
	}

	m.cancelRestoreTimer(rule.ID)
	m.releasePBRState(fwmark)

	m.routeStateMu.Lock()
	m.fallbackMu.Lock()
	for _, f := range families() {
		key := keyFor(rule.ID, f)
		delete(m.activeGateway, key)
		delete(m.appliedState, key)
		delete(m.fallbackActive, key)
	}
	m.fallbackMu.Unlock()
	m.routeStateMu.Unlock()

	log.Printf("firewall: rule %q: released PBR state (ip rule + ip -6 rule + table %d in both families)",
		rule.Name, fwmark)
}

// releaseOrphanedMarks releases every fwmark that was owned before a change and
// is no longer claimed afterwards. Used by ApplyRules, where the applied
// snapshot is replaced wholesale and rules can disappear from it.
func (m *Manager) releaseOrphanedMarks(before map[int]bool) {
	after, err := pbrMarksInUse("")
	if err != nil {
		log.Printf("firewall: releaseOrphanedMarks: %v", err)
		return
	}
	for mark := range before {
		if after[mark] {
			continue
		}
		m.releasePBRState(mark)
		log.Printf("firewall: released orphaned PBR state for fwmark %d", mark)
	}
}
