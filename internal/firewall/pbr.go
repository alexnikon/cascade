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
	"github.com/alexnikon/cascade/internal/util"
)

// pbrKind is which of the three routing outcomes a rule currently wants.
type pbrKind string

const (
	pbrGateway   pbrKind = "gateway"   // gateway healthy → route via it
	pbrFallback  pbrKind = "fallback"  // gateway down, fallbackToDefault → system default
	pbrBlackhole pbrKind = "blackhole" // gateway down, no fallback → drop
)

// pbrDesired is the routing state a rule should currently have.
type pbrDesired struct {
	kind pbrKind
	gw   resolvedGW // set for pbrGateway and pbrFallback
}

// sysRouteExec reads the system routing table. Separate from the write seams so
// tests can script "ip route show default" without privileges.
var sysRouteExec = util.Exec

// gatewayIsDown reports whether a directly-referenced gateway is currently
// unhealthy. Only "down" and "admin_down" count — "unknown" must NOT blackhole a
// rule, which matches the semantics already used by isGroupAllDown.
func (m *Manager) gatewayIsDown(gatewayID string) bool {
	if m.gm == nil || m.gm.Monitor() == nil {
		return false
	}
	st := m.gm.Monitor().GetStatus(gatewayID).Status
	return st == "down" || st == "admin_down"
}

// gatewayHealthUnknown reports that no probe has produced a result yet, so this
// rule's health genuinely cannot be evaluated. True only for a rule bound to a
// single gateway; a group is judged by its members and resolves on its own.
func (m *Manager) gatewayHealthUnknown(rule *Rule) bool {
	if rule.GatewayID == "" || m.gm == nil || m.gm.Monitor() == nil {
		return false
	}
	return m.gm.Monitor().GetStatus(rule.GatewayID).Status == "unknown"
}

// tableHasDefaultRoute reports whether a routing table already holds a default
// route — a gateway route, a fallback, or a blackhole.
func (m *Manager) tableHasDefaultRoute(table int) bool {
	out, err := pbrRouteExec(fmt.Sprintf("ip route show table %d", table), 5*time.Second, false)
	if err != nil {
		return false
	}
	return strings.Contains(out, "default")
}

// desiredRoutingState derives the routing state a rule should have right now
// from its configuration and the live health of its gateway or gateway group.
func (m *Manager) desiredRoutingState(rule *Rule) (pbrDesired, error) {
	unhealthy := func() (pbrDesired, error) {
		if !rule.FallbackToDefault {
			return pbrDesired{kind: pbrBlackhole}, nil
		}
		gw, err := m.getSystemDefaultGateway()
		if err != nil {
			return pbrDesired{}, fmt.Errorf("fallback: %w", err)
		}
		return pbrDesired{kind: pbrFallback, gw: gw}, nil
	}

	switch {
	case rule.GatewayGroupID != "":
		allDown, err := m.isGroupAllDown(rule.GatewayGroupID)
		if err != nil {
			return pbrDesired{}, err
		}
		if allDown {
			return unhealthy()
		}
	case rule.GatewayID != "":
		if m.gatewayIsDown(rule.GatewayID) {
			return unhealthy()
		}
	default:
		return pbrDesired{}, fmt.Errorf("rule has no gateway or gateway group")
	}

	// ResolveGroupGateway is already health-aware, so this picks the right tier.
	gw, err := m.resolveGateway(rule)
	if err != nil {
		return pbrDesired{}, err
	}
	return pbrDesired{kind: pbrGateway, gw: gw}, nil
}

// routeCommand renders the "ip route replace" for a desired state. WireGuard
// interfaces need a device-only route; everything else gets an on-link next hop.
func routeCommand(d pbrDesired, table int) string {
	if d.kind == pbrBlackhole {
		return fmt.Sprintf("ip route replace blackhole default table %d", table)
	}
	isWG := strings.HasPrefix(d.gw.iface, "wg") || strings.HasPrefix(d.gw.iface, "awg")
	if isWG || d.gw.gatewayIP == "" {
		return fmt.Sprintf("ip route replace default dev %s table %d", d.gw.iface, table)
	}
	return fmt.Sprintf("ip route replace default via %s dev %s onlink table %d", d.gw.gatewayIP, d.gw.iface, table)
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
		desired, err := m.desiredRoutingState(rule)
		if err != nil {
			return err
		}
		return m.applyDesiredLocked(rule, desired)
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
func (m *Manager) applyDesiredLocked(rule *Rule, desired pbrDesired) error {
	fwmark := *rule.Fwmark
	ruleInstalled := m.ipRuleExists(fwmark)

	m.routeStateMu.Lock()
	last, known := m.appliedState[rule.ID]
	m.routeStateMu.Unlock()

	// Startup, before the first probe: health is genuinely unknown, so a route
	// written now would be a guess. Whatever the kernel already holds came from
	// the last time health *was* known, which is a strictly better answer — keep
	// it and let the first probe reconcile. Only a table with nothing in it gets
	// the provisional gateway route, since there is no knowledge to preserve.
	if !known && m.gatewayHealthUnknown(rule) && m.tableHasDefaultRoute(fwmark) {
		if !ruleInstalled {
			priority := 1000 + rule.Order*10
			cmd := fmt.Sprintf("ip rule add fwmark %d lookup %d priority %d", fwmark, fwmark, priority)
			if _, err := pbrRuleExec(cmd, 10*time.Second, true); err != nil {
				return fmt.Errorf("ip rule add: %w", err)
			}
		}
		log.Printf("firewall: rule %q: gateway health not yet known — keeping the existing table %d route",
			rule.Name, fwmark)
		return nil
	}

	if !known || last != desired {
		if _, err := pbrRouteExec(routeCommand(desired, fwmark), 10*time.Second, true); err != nil {
			return fmt.Errorf("ip route replace: %w", err)
		}
	}

	// Exactly one policy rule per fwmark.
	if !ruleInstalled {
		priority := 1000 + rule.Order*10
		cmd := fmt.Sprintf("ip rule add fwmark %d lookup %d priority %d", fwmark, fwmark, priority)
		if _, err := pbrRuleExec(cmd, 10*time.Second, true); err != nil {
			return fmt.Errorf("ip rule add: %w", err)
		}
	}

	m.routeStateMu.Lock()
	m.appliedState[rule.ID] = desired
	if desired.kind == pbrGateway {
		m.activeGateway[rule.ID] = desired.gw
	} else {
		delete(m.activeGateway, rule.ID)
	}
	m.routeStateMu.Unlock()

	m.fallbackMu.Lock()
	was := m.fallbackActive[rule.ID]
	if desired.kind == pbrGateway {
		delete(m.fallbackActive, rule.ID)
	} else {
		m.fallbackActive[rule.ID] = true
	}
	m.fallbackMu.Unlock()

	// Log transitions only, so steady-state reconciliation stays quiet.
	switch {
	case desired.kind == pbrFallback && !was:
		log.Printf("firewall: rule %q: fallback → default via %s dev %s (gateway unavailable)",
			rule.Name, desired.gw.gatewayIP, desired.gw.iface)
	case desired.kind == pbrBlackhole && !was:
		log.Printf("firewall: rule %q: blackhole ACTIVE (gateway unavailable)", rule.Name)
	case desired.kind == pbrGateway && was:
		log.Printf("firewall: rule %q: gateway route restored via %s", rule.Name, desired.gw.iface)
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
	for i := 0; i < maxDuplicatePolicyRules; i++ {
		if !m.ipRuleExists(fwmark) {
			break
		}
		cmd := fmt.Sprintf("ip rule del fwmark %d lookup %d", fwmark, fwmark)
		if _, err := pbrRuleExec(cmd, 5*time.Second, true); err != nil {
			break
		}
	}
	// Removes the gateway route, a fallback default, or a blackhole alike.
	pbrRouteExec(fmt.Sprintf("ip route flush table %d", fwmark), 5*time.Second, true) //nolint:errcheck
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
	delete(m.activeGateway, rule.ID)
	delete(m.appliedState, rule.ID)
	m.routeStateMu.Unlock()
	m.fallbackMu.Lock()
	delete(m.fallbackActive, rule.ID)
	m.fallbackMu.Unlock()

	log.Printf("firewall: rule %q: released PBR state (ip rule + table %d)", rule.Name, fwmark)
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
