// Package firewall manages Firewall Rules that combine packet filtering and
// Policy-Based Routing (PBR) via iptables-nft custom chains.
//
// Architecture (mirrors FirewallManager.js):
//
//	FIREWALL_FORWARD (filter table) — ACCEPT/DROP/REJECT for every rule
//	FIREWALL_MANGLE  (mangle table, PREROUTING) — MARK (PBR rules) or RETURN
//	FIREWALL_MANGLE_OUT (mangle table, OUTPUT)  — MARK for rules with ApplyToLocal
//
// FIREWALL_MANGLE marks forwarded traffic. Rules that opt in via ApplyToLocal get
// the same MARK installed in FIREWALL_MANGLE_OUT as well, so traffic generated on
// this host uses the same fwmark, ip rule and routing table. That chain opens with
// guards for already-marked packets and for WireGuard transport (see
// installLocalGuards), which is what keeps a tunnel from being routed into itself.
//
// Rules are processed in order; the first match wins.
// PBR rules: packets get fwmark → ip rule lookup table N → table N has
//
//	"default via <gatewayIP> dev <iface>" → routed through that gateway.
//
// Non-PBR rules: RETURN in mangle prevents subsequent PBR rules from marking.
//
// Gateway fallback (FIX-15b):
//
//	When a gateway goes down, the routing table entry is replaced with either
//	a blackhole route (drop) or the system default gateway (failover),
//	depending on the rule's fallbackToDefault flag.
//	Recovery is delayed 30 s (anti-flap) and triggered by the GatewayMonitor
//	StatusChange callback registered in Init().
//
// Storage: SQLite `firewall_rules` table.
// Source/destination endpoint objects are stored as JSON columns.
package firewall

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/gateway"
	"github.com/alexnikon/cascade/internal/settings"
	"github.com/alexnikon/cascade/internal/util"
	"github.com/alexnikon/cascade/internal/validate"
)

// ── Types ─────────────────────────────────────────────────────────────────────

// Endpoint describes a traffic match for source or destination.
type Endpoint struct {
	Type        string `json:"type"`                  // any | cidr | alias
	Value       string `json:"value,omitempty"`       // CIDR for type=cidr
	AliasID     string `json:"aliasId,omitempty"`     // alias ID for type=alias
	Invert      bool   `json:"invert,omitempty"`      // negate the match (!= )
	Port        string `json:"port,omitempty"`        // legacy plain port string
	PortAliasID string `json:"portAliasId,omitempty"` // port/port-group alias ID
}

// Rule is a firewall/PBR rule persisted in SQLite.
// RuleType is "rule" (default) or "separator" (visual divider, ignored by kernel).
type Rule struct {
	ID                string   `json:"id"`
	RuleType          string   `json:"ruleType"` // "rule" | "separator"
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	Order             int      `json:"order"`
	Interface         string   `json:"interface"` // any | wg10 | eth0 ...
	Protocol          string   `json:"protocol"`  // any | tcp | udp | tcp/udp | icmp
	Source            Endpoint `json:"source"`
	Destination       Endpoint `json:"destination"`
	Action            string   `json:"action"`            // accept | drop | reject
	GatewayID         string   `json:"gatewayId"`         // PBR: direct gateway
	GatewayGroupID    string   `json:"gatewayGroupId"`    // PBR: gateway group
	Fwmark            *int     `json:"fwmark"`            // auto-assigned for PBR rules
	FallbackToDefault bool     `json:"fallbackToDefault"` // fallback to default gw (vs blackhole)
	ApplyToLocal      bool     `json:"applyToLocal"`      // PBR also applies to traffic originating on this host
	Log               bool     `json:"log"`
	Comment           string   `json:"comment"`
	SeparatorColor    string   `json:"separatorColor"` // separator tint: ""=gray | red|orange|yellow|green|cyan|blue|purple
	CreatedAt         string   `json:"createdAt"`
}

// RuleInput is the create/update request payload from the API.
type RuleInput struct {
	Name              string   `json:"name"`
	Interface         string   `json:"interface"`
	Protocol          string   `json:"protocol"`
	Source            Endpoint `json:"source"`
	Destination       Endpoint `json:"destination"`
	Action            string   `json:"action"`
	GatewayID         string   `json:"gatewayId"`
	GatewayGroupID    string   `json:"gatewayGroupId"`
	Fwmark            *int     `json:"fwmark"`
	FallbackToDefault bool     `json:"fallbackToDefault"`
	ApplyToLocal      bool     `json:"applyToLocal"`
	Log               bool     `json:"log"`
	Comment           string   `json:"comment"`
}

// portCombo represents one iptables protocol+port combination.
type portCombo struct {
	proto        string // tcp | udp | nil-like ""
	srcPort      string
	srcMultiport bool
	dstPort      string
	dstMultiport bool
}

// TraceStep is one rule evaluated during SimulateTrace.
type TraceStep struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Fwmark       *int   `json:"fwmark"`
	SrcMatch     bool   `json:"srcMatch"`
	DstMatch     bool   `json:"dstMatch"`
	Matched      bool   `json:"matched"`
	ProtoSkipped bool   `json:"protoSkipped,omitempty"` // true when rule has protocol/port condition that was not evaluated
}

// TraceResult is the output of SimulateTrace.
type TraceResult struct {
	MatchedRule *MatchedRule `json:"matchedRule"` // nil if no rule matched
	Steps       []TraceStep  `json:"steps"`
}

// MatchedRule is the summary of the winning rule in a trace.
type MatchedRule struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Fwmark *int   `json:"fwmark"`
}

// HostInterface is one network interface returned by GetNetworkInterfaces.
type HostInterface struct {
	Name string `json:"name"`
}

// ── Manager ───────────────────────────────────────────────────────────────────

// Manager manages Firewall Rules and PBR routing tables.
type Manager struct {
	am *aliases.Manager
	gm *gateway.Manager

	rebuildMu     sync.Mutex // serialises rebuildChains calls
	ruleApplyMu   sync.Map   // rule ID → *sync.Mutex; serialises route updates per rule
	routeStateMu  sync.Mutex
	activeGateway map[stateKey]resolvedGW // rule+family → last successfully applied gateway
	appliedState  map[stateKey]pbrDesired // rule+family → last desired state written to the kernel
	restoreDelay  time.Duration

	fallbackMu     sync.Mutex
	fallbackActive map[stateKey]bool      // rule+family → currently in fallback/blackhole
	restoreTimers  map[string]*time.Timer // rule ID → 30 s anti-flap restore timer
	restoreWG      sync.WaitGroup         // in-flight restore callbacks
}

// New creates a Manager. Call Init() after db.Init().
func New(am *aliases.Manager, gm *gateway.Manager) *Manager {
	return &Manager{
		am:             am,
		gm:             gm,
		activeGateway:  make(map[stateKey]resolvedGW),
		appliedState:   make(map[stateKey]pbrDesired),
		restoreDelay:   30 * time.Second,
		fallbackActive: make(map[stateKey]bool),
		restoreTimers:  make(map[string]*time.Timer),
	}
}

// Init initialises iptables chains, loads rules from SQLite, rebuilds chains,
// and registers the GatewayMonitor callback for fallback logic.
func (m *Manager) Init() error {
	if err := m.initChains(); err != nil {
		log.Printf("firewall: initChains warning: %v", err)
		// Non-fatal: container may not have iptables on dev machine.
	}

	// Ensure the applied snapshot exists (first run after upgrade: copy draft → applied).
	if err := m.ensureAppliedSnapshot(); err != nil {
		log.Printf("firewall: ensureAppliedSnapshot warning: %v", err)
	}

	if err := m.rebuildChains(); err != nil {
		log.Printf("firewall: initial rebuildChains warning: %v", err)
	}

	// Register gateway status change callback for PBR fallback (FIX-15b).
	m.gm.Monitor().OnStatusChange(func(gwID, newStatus, oldStatus string) {
		if err := m.handleGatewayStatusChange(gwID, newStatus, oldStatus); err != nil {
			log.Printf("firewall: handleGatewayStatusChange(%s): %v", gwID, err)
		}
	})

	count, _ := m.countRules()
	log.Printf("firewall: init complete (%d rules)", count)
	return nil
}

// ── Public CRUD ───────────────────────────────────────────────────────────────

// GetRules returns all rules sorted by order ascending (includes separators).
func (m *Manager) GetRules() ([]Rule, error) {
	rows, err := db.DB().Query(`
		SELECT id, rule_type, name, enabled, order_idx, interface, protocol,
		       source, destination, action,
		       gateway_id, gateway_group_id, fwmark, fallback_to_default,
		       apply_to_local, log, comment, separator_color, created_at
		FROM firewall_rules ORDER BY order_idx
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRule returns a single rule by ID, or nil if not found.
func (m *Manager) GetRule(id string) (*Rule, error) {
	row := db.DB().QueryRow(`
		SELECT id, rule_type, name, enabled, order_idx, interface, protocol,
		       source, destination, action,
		       gateway_id, gateway_group_id, fwmark, fallback_to_default,
		       apply_to_local, log, comment, separator_color, created_at
		FROM firewall_rules WHERE id = ?
	`, id)
	r, err := scanRuleRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// ── Staged apply ──────────────────────────────────────────────────────────────

// getAppliedRules reads rules from the applied snapshot table (used by rebuildChains).
func (m *Manager) getAppliedRules() ([]Rule, error) {
	rows, err := db.DB().Query(`
		SELECT id, rule_type, name, enabled, order_idx, interface, protocol,
		       source, destination, action,
		       gateway_id, gateway_group_id, fwmark, fallback_to_default,
		       apply_to_local, log, comment, separator_color, created_at
		FROM firewall_rules_applied ORDER BY order_idx
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ensureAppliedSnapshot copies firewall_rules → firewall_rules_applied when the
// applied table is empty. Called on Init() to handle first-run and upgrades from
// pre-staged versions where no snapshot exists yet.
func (m *Manager) ensureAppliedSnapshot() error {
	var count int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM firewall_rules_applied`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil // snapshot already exists
	}
	// First run or upgrade: seed applied from draft so existing rules stay active.
	_, err := db.DB().Exec(`
		INSERT INTO firewall_rules_applied
		    (id, rule_type, name, interface, protocol, source, destination, src_port, dst_port,
		     action, gateway_id, gateway_group_id, fwmark, fallback_to_default,
		     apply_to_local, enabled, log, comment, separator_color, order_idx, created_at)
		SELECT id, rule_type, name, interface, protocol, source, destination, src_port, dst_port,
		       action, gateway_id, gateway_group_id, fwmark, fallback_to_default,
		       apply_to_local, enabled, log, comment, separator_color, order_idx, created_at
		FROM firewall_rules
	`)
	if err != nil {
		return fmt.Errorf("ensureAppliedSnapshot: %w", err)
	}
	log.Printf("firewall: applied snapshot seeded from draft (%d rules)", count)
	return nil
}

// ApplyRules copies the current draft (firewall_rules) → applied snapshot, then
// rebuilds iptables chains from the snapshot.
func (m *Manager) ApplyRules() error {
	// Marks owned before the snapshot is replaced. Any that no longer appear
	// afterwards belonged to rules this apply removed, and their routing state
	// has to go with them.
	before, err := pbrMarksInUse("")
	if err != nil {
		return err
	}

	tx, err := db.DB().Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM firewall_rules_applied`); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO firewall_rules_applied
		    (id, rule_type, name, interface, protocol, source, destination, src_port, dst_port,
		     action, gateway_id, gateway_group_id, fwmark, fallback_to_default,
		     apply_to_local, enabled, log, comment, separator_color, order_idx, created_at)
		SELECT id, rule_type, name, interface, protocol, source, destination, src_port, dst_port,
		       action, gateway_id, gateway_group_id, fwmark, fallback_to_default,
		       apply_to_local, enabled, log, comment, separator_color, order_idx, created_at
		FROM firewall_rules
	`); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	m.releaseOrphanedMarks(before)
	log.Printf("firewall: rules applied (snapshot updated)")
	return m.rebuildChains()
}

// DiscardChanges overwrites the draft (firewall_rules) with the applied snapshot,
// reverting all unapplied edits. No kernel change is needed (kernel already matches applied).
func (m *Manager) DiscardChanges() error {
	tx, err := db.DB().Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM firewall_rules`); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO firewall_rules
		    (id, rule_type, name, interface, protocol, source, destination, src_port, dst_port,
		     action, gateway_id, gateway_group_id, fwmark, fallback_to_default,
		     apply_to_local, enabled, log, comment, separator_color, order_idx, created_at)
		SELECT id, rule_type, name, interface, protocol, source, destination, src_port, dst_port,
		       action, gateway_id, gateway_group_id, fwmark, fallback_to_default,
		       apply_to_local, enabled, log, comment, separator_color, order_idx, created_at
		FROM firewall_rules_applied
	`); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("firewall: draft discarded — reverted to applied snapshot")
	return nil
}

// HasPendingChanges reports whether the draft differs from the applied snapshot.
// NOTE: SQLite compound operators (EXCEPT, UNION ALL) are left-associative and
// have equal precedence, so we must wrap each EXCEPT in a subquery to avoid the
// mis-parse: A EXCEPT B UNION ALL C EXCEPT D → ((A EXCEPT B) UNION ALL C) EXCEPT D.
func (m *Manager) HasPendingChanges() (bool, error) {
	var diff int
	err := db.DB().QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT * FROM (
				SELECT id, rule_type, name, enabled, order_idx, interface, protocol,
				       source, destination, action, gateway_id, gateway_group_id,
				       fwmark, fallback_to_default, apply_to_local, log, comment, separator_color
				FROM firewall_rules
				WHERE rule_type != 'separator'
				EXCEPT
				SELECT id, rule_type, name, enabled, order_idx, interface, protocol,
				       source, destination, action, gateway_id, gateway_group_id,
				       fwmark, fallback_to_default, apply_to_local, log, comment, separator_color
				FROM firewall_rules_applied
				WHERE rule_type != 'separator'
			) AS draft_minus_applied
			UNION ALL
			SELECT * FROM (
				SELECT id, rule_type, name, enabled, order_idx, interface, protocol,
				       source, destination, action, gateway_id, gateway_group_id,
				       fwmark, fallback_to_default, apply_to_local, log, comment, separator_color
				FROM firewall_rules_applied
				WHERE rule_type != 'separator'
				EXCEPT
				SELECT id, rule_type, name, enabled, order_idx, interface, protocol,
				       source, destination, action, gateway_id, gateway_group_id,
				       fwmark, fallback_to_default, apply_to_local, log, comment, separator_color
				FROM firewall_rules
				WHERE rule_type != 'separator'
			) AS applied_minus_draft
		)
	`).Scan(&diff)
	if err != nil {
		return false, err
	}
	return diff > 0, nil
}

// AddRule creates a new rule, returns the created rule.
func (m *Manager) AddRule(inp RuleInput) (*Rule, error) {
	if err := validateInput(inp); err != nil {
		return nil, err
	}

	order, err := m.nextOrder()
	if err != nil {
		return nil, err
	}

	hasPBR := inp.GatewayID != "" || inp.GatewayGroupID != ""
	fwmark := inp.Fwmark
	if hasPBR && fwmark == nil {
		next, err := m.nextFwmark()
		if err != nil {
			return nil, err
		}
		fwmark = &next
	}
	if !hasPBR {
		fwmark = nil
	}

	rule := Rule{
		ID:                uuid.New().String(),
		Name:              strings.TrimSpace(inp.Name),
		Enabled:           true,
		Order:             order,
		Interface:         strOr(inp.Interface, "any"),
		Protocol:          strOr(inp.Protocol, "any"),
		Source:            normalizeEndpoint(inp.Source),
		Destination:       normalizeEndpoint(inp.Destination),
		Action:            strOr(inp.Action, "accept"),
		GatewayID:         inp.GatewayID,
		GatewayGroupID:    inp.GatewayGroupID,
		Fwmark:            fwmark,
		FallbackToDefault: hasPBR && inp.FallbackToDefault,
		ApplyToLocal:      hasPBR && inp.ApplyToLocal,
		Log:               inp.Log,
		Comment:           strings.TrimSpace(inp.Comment),
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
	}

	if err := insertRule(rule); err != nil {
		return nil, err
	}

	log.Printf("firewall: rule added %q (action=%s order=%d) — pending apply", rule.Name, rule.Action, rule.Order)
	return &rule, nil
}

// allowedSeparatorColors is the set of valid separator color values.
// Empty string means "default" (gray). Must match the frontend palette.
var allowedSeparatorColors = map[string]bool{
	"": true, "red": true, "orange": true, "yellow": true,
	"green": true, "cyan": true, "blue": true, "purple": true,
}

// sanitizeSeparatorColor returns color if it is in the allowed palette, "" otherwise.
func sanitizeSeparatorColor(color string) string {
	if allowedSeparatorColors[color] {
		return color
	}
	return ""
}

// Separate INSERT constants to avoid fmt.Sprintf table-name interpolation antipattern.
const insertSepDraft = `
	INSERT INTO firewall_rules (id, rule_type, name, enabled, order_idx, interface, protocol,
	    source, destination, action, gateway_id, gateway_group_id, fwmark,
	    fallback_to_default, log, comment, separator_color, created_at)
	VALUES (?, 'separator', ?, 1, ?, 'any', 'any', '{}', '{}', 'accept', '', '', NULL, 0, 0, ?, ?, ?)
`
const insertSepApplied = `
	INSERT INTO firewall_rules_applied (id, rule_type, name, enabled, order_idx, interface, protocol,
	    source, destination, action, gateway_id, gateway_group_id, fwmark,
	    fallback_to_default, log, comment, separator_color, created_at)
	VALUES (?, 'separator', ?, 1, ?, 'any', 'any', '{}', '{}', 'accept', '', '', NULL, 0, 0, ?, ?, ?)
`

// AddSeparator creates a visual separator (no iptables effect) at the end of the list.
// Writes to BOTH firewall_rules and firewall_rules_applied so separators are never
// part of the pending-apply cycle.
func (m *Manager) AddSeparator(name, color string) (*Rule, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Separator"
	}
	color = sanitizeSeparatorColor(color)
	order, err := m.nextOrder()
	if err != nil {
		return nil, err
	}
	sep := Rule{
		ID:             uuid.New().String(),
		RuleType:       "separator",
		Name:           name,
		SeparatorColor: color,
		Enabled:        true,
		Order:          order,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	tx, err := db.DB().Begin()
	if err != nil {
		return nil, err
	}
	args := []any{sep.ID, sep.Name, sep.Order, "", sep.SeparatorColor, sep.CreatedAt}
	if _, err := tx.Exec(insertSepDraft, args...); err != nil {
		tx.Rollback()
		return nil, err
	}
	if _, err := tx.Exec(insertSepApplied, args...); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	log.Printf("firewall: separator added %q color=%q (order=%d)", sep.Name, sep.Color(), sep.Order)
	return &sep, nil
}

// UpdateSeparator updates name and color of a separator, syncing both tables.
// Returns an error if the rule does not exist or is not a separator.
func (m *Manager) UpdateSeparator(id, name, color string) (*Rule, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Separator"
	}
	color = sanitizeSeparatorColor(color)
	tx, err := db.DB().Begin()
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(
		`UPDATE firewall_rules SET name = ?, separator_color = ? WHERE id = ? AND rule_type = 'separator'`,
		name, color, id,
	)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		tx.Rollback()
		return nil, fmt.Errorf("firewall rule not found")
	}
	if _, err := tx.Exec(
		`UPDATE firewall_rules_applied SET name = ?, separator_color = ? WHERE id = ? AND rule_type = 'separator'`,
		name, color, id,
	); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return m.GetRule(id)
}

// Color returns SeparatorColor for logging convenience.
func (r *Rule) Color() string { return r.SeparatorColor }

// UpdateRule replaces a rule's fields, rebuilds chains, returns the updated rule.
func (m *Manager) UpdateRule(id string, inp RuleInput) (*Rule, error) {
	old, err := m.GetRule(id)
	if err != nil {
		return nil, err
	}
	if old == nil {
		return nil, fmt.Errorf("firewall rule not found")
	}
	if err := validateInput(inp); err != nil {
		return nil, err
	}

	hasPBR := inp.GatewayID != "" || inp.GatewayGroupID != ""
	fwmark := inp.Fwmark
	if hasPBR && fwmark == nil {
		if old.Fwmark != nil {
			fwmark = old.Fwmark
		} else {
			next, err := m.nextFwmark()
			if err != nil {
				return nil, err
			}
			fwmark = &next
		}
	}
	if !hasPBR {
		fwmark = nil
	}

	rule := Rule{
		ID:                old.ID,
		Name:              strings.TrimSpace(inp.Name),
		Enabled:           old.Enabled,
		Order:             old.Order,
		Interface:         strOr(inp.Interface, "any"),
		Protocol:          strOr(inp.Protocol, "any"),
		Source:            normalizeEndpoint(inp.Source),
		Destination:       normalizeEndpoint(inp.Destination),
		Action:            strOr(inp.Action, "accept"),
		GatewayID:         inp.GatewayID,
		GatewayGroupID:    inp.GatewayGroupID,
		Fwmark:            fwmark,
		FallbackToDefault: hasPBR && inp.FallbackToDefault,
		ApplyToLocal:      hasPBR && inp.ApplyToLocal,
		Log:               inp.Log,
		Comment:           strings.TrimSpace(inp.Comment),
		CreatedAt:         old.CreatedAt,
	}

	if err := updateRule(rule); err != nil {
		return nil, err
	}

	log.Printf("firewall: rule updated %q — pending apply", rule.Name)
	return &rule, nil
}

// DeleteRule removes a rule from the draft (pending apply).
// For separators: also removes from firewall_rules_applied (separators bypass the apply cycle).
func (m *Manager) DeleteRule(id string) error {
	r, err := m.GetRule(id)
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("firewall rule not found")
	}

	if r.RuleType == "separator" {
		tx, err := db.DB().Begin()
		if err != nil {
			return err
		}
		for _, tbl := range []string{"firewall_rules", "firewall_rules_applied"} {
			if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, tbl), id); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		log.Printf("firewall: separator deleted %q", r.Name)
		return nil
	}

	if _, err := db.DB().Exec(`DELETE FROM firewall_rules WHERE id = ?`, id); err != nil {
		return err
	}

	// Tear the rule's routing state down now, while its fwmark is still known.
	// Rediscovering it after the row is gone is impossible, which is exactly how
	// stale ip rules and populated tables used to survive a delete.
	m.releaseRoutingForRule(r)

	log.Printf("firewall: rule deleted %q — pending apply", r.Name)
	return nil
}

// ToggleRule enables or disables a rule (pending apply).
func (m *Manager) ToggleRule(id string, enabled bool) (*Rule, error) {
	r, err := m.GetRule(id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("firewall rule not found")
	}

	if _, err := db.DB().Exec(`UPDATE firewall_rules SET enabled = ? WHERE id = ?`, boolInt(enabled), id); err != nil {
		return nil, err
	}

	r.Enabled = enabled
	return r, nil
}

// MoveRule swaps the order of a rule with its neighbour ("up" or "down") — pending apply.
func (m *Manager) MoveRule(id, direction string) (*Rule, error) {
	rules, err := m.GetRules()
	if err != nil {
		return nil, err
	}

	idx := -1
	for i, r := range rules {
		if r.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, fmt.Errorf("firewall rule not found")
	}

	swapIdx := idx - 1
	if direction == "down" {
		swapIdx = idx + 1
	}
	if swapIdx < 0 || swapIdx >= len(rules) {
		return &rules[idx], nil // already at edge
	}

	// Swap order values in DB.
	a, b := rules[idx], rules[swapIdx]
	tx, err := db.DB().Begin()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE firewall_rules SET order_idx = ? WHERE id = ?`, b.Order, a.ID); err != nil {
		tx.Rollback()
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE firewall_rules SET order_idx = ? WHERE id = ?`, a.Order, b.ID); err != nil {
		tx.Rollback()
		return nil, err
	}
	// If either swapped item is a separator, sync BOTH order_idx values to applied.
	// Rationale: moving a separator doesn't change the relative order of non-separator
	// rules, so it must not create a "pending changes" diff. Because HasPendingChanges
	// compares order_idx for non-separators, the neighbor's order_idx must also be
	// kept in sync when it was displaced only by a separator move.
	if a.RuleType == "separator" || b.RuleType == "separator" {
		if _, err := tx.Exec(`UPDATE firewall_rules_applied SET order_idx = ? WHERE id = ?`, b.Order, a.ID); err != nil {
			tx.Rollback()
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE firewall_rules_applied SET order_idx = ? WHERE id = ?`, a.Order, b.ID); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	a.Order, b.Order = b.Order, a.Order
	return &a, nil
}

// ReorderRules sets the order_idx of rules to match the given slice of IDs.
// The slice must contain exactly all existing rule IDs (no extras, no missing).
// If only separators changed position (non-separator relative order is unchanged),
// the reorder is synced to applied immediately and creates no pending diff.
// If non-separators changed position, only separator order_idx is synced to applied
// (normal pending apply for the non-separator changes).
func (m *Manager) ReorderRules(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("ids must not be empty")
	}

	// Fetch current rules to validate and detect separator positions.
	rules, err := m.GetRules()
	if err != nil {
		return err
	}

	// Validate: incoming list must be exactly the same set of IDs as in the DB.
	if len(ids) != len(rules) {
		return fmt.Errorf("reorder list length %d does not match rule count %d", len(ids), len(rules))
	}
	knownIDs := make(map[string]bool, len(rules))
	for _, r := range rules {
		knownIDs[r.ID] = true
	}
	for _, id := range ids {
		if !knownIDs[id] {
			return fmt.Errorf("unknown rule id %q in reorder list", id)
		}
	}

	// Build separator set and old non-separator sequence (by ID, in current order).
	sepIDs := make(map[string]bool)
	oldNonSepSeq := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.RuleType == "separator" {
			sepIDs[r.ID] = true
		} else {
			oldNonSepSeq = append(oldNonSepSeq, r.ID)
		}
	}
	// New non-separator sequence from the incoming ids list.
	newNonSepSeq := make([]string, 0, len(oldNonSepSeq))
	for _, id := range ids {
		if !sepIDs[id] {
			newNonSepSeq = append(newNonSepSeq, id)
		}
	}
	// If relative order of non-separators is unchanged → only separators moved.
	onlySepsMoved := len(oldNonSepSeq) == len(newNonSepSeq)
	if onlySepsMoved {
		for i := range oldNonSepSeq {
			if oldNonSepSeq[i] != newNonSepSeq[i] {
				onlySepsMoved = false
				break
			}
		}
	}

	tx, err := db.DB().Begin()
	if err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE firewall_rules SET order_idx = ? WHERE id = ?`, i, id); err != nil {
			tx.Rollback()
			return err
		}
	}

	// Always sync separator order_idx to applied (separators bypass the apply cycle).
	if _, err := tx.Exec(`
		UPDATE firewall_rules_applied
		SET order_idx = (SELECT order_idx FROM firewall_rules WHERE firewall_rules.id = firewall_rules_applied.id)
		WHERE id IN (SELECT id FROM firewall_rules WHERE rule_type = 'separator')
	`); err != nil {
		tx.Rollback()
		return err
	}

	if onlySepsMoved {
		// Non-separator relative order unchanged → also sync non-separator order_idx
		// so HasPendingChanges does not produce a false positive.
		if _, err := tx.Exec(`
			UPDATE firewall_rules_applied
			SET order_idx = (SELECT order_idx FROM firewall_rules WHERE firewall_rules.id = firewall_rules_applied.id)
			WHERE id IN (SELECT id FROM firewall_rules WHERE rule_type != 'separator')
		`); err != nil {
			tx.Rollback()
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	if onlySepsMoved {
		log.Printf("firewall: rules reordered (%d rules) — separators only, no pending diff", len(ids))
	} else {
		log.Printf("firewall: rules reordered (%d rules) — pending apply", len(ids))
	}
	return nil
}

// SimulateTrace walks the rule list in order, matching srcIP/dstIP against
// each enabled rule. Returns the first matching rule and all evaluated steps.
// Used by the route test API to determine which PBR fwmark (if any) applies.
//
// Rules with protocol or port conditions are recorded in the trace with
// ProtoSkipped=true and never counted as matched — the caller only provides
// L3 addresses, so L4 conditions cannot be evaluated. When protocol/port
// parameters are added to the trace in the future, pass them here and evaluate
// them before the srcMatch/dstMatch block.
func (m *Manager) SimulateTrace(srcIP, dstIP string) (*TraceResult, error) {
	// Prefer the applied snapshot so results match what's actually in iptables.
	// Fall back to live rules when the snapshot is empty (e.g. in tests).
	rules, err := m.getAppliedRules()
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		rules, err = m.GetRules()
		if err != nil {
			return nil, err
		}
	}

	result := &TraceResult{Steps: []TraceStep{}}
	for _, rule := range rules {
		if !rule.Enabled || rule.RuleType == "separator" {
			continue
		}

		// Rules with protocol or port conditions cannot be evaluated with L3-only
		// input. Record them in the trace as skipped so the UI can show them, but
		// never count them as matched — they must not block subsequent rules from
		// being evaluated.
		hasProto := rule.Protocol != "" && rule.Protocol != "any"
		hasPort := rule.Source.Port != "" || rule.Source.PortAliasID != "" ||
			rule.Destination.Port != "" || rule.Destination.PortAliasID != ""
		if hasProto || hasPort {
			result.Steps = append(result.Steps, TraceStep{
				ID:           rule.ID,
				Name:         rule.Name,
				Fwmark:       rule.Fwmark,
				ProtoSkipped: true,
			})
			continue
		}

		srcMatch, err := m.matchEndpoint(&rule.Source, srcIP)
		if err != nil {
			srcMatch = false
		}
		dstMatch, err := m.matchEndpoint(&rule.Destination, dstIP)
		if err != nil {
			dstMatch = false
		}
		matched := srcMatch && dstMatch

		result.Steps = append(result.Steps, TraceStep{
			ID:       rule.ID,
			Name:     rule.Name,
			Fwmark:   rule.Fwmark,
			SrcMatch: srcMatch,
			DstMatch: dstMatch,
			Matched:  matched,
		})

		if matched {
			result.MatchedRule = &MatchedRule{
				ID:     rule.ID,
				Name:   rule.Name,
				Fwmark: rule.Fwmark,
			}
			return result, nil
		}
	}
	return result, nil
}

// GetNetworkInterfaces returns host interfaces for the ingress interface selector.
// Parses "ip -o link show" text output — no -j flag (FIX-11).
func (m *Manager) GetNetworkInterfaces() ([]HostInterface, error) {
	out, err := util.ExecSilentFast("ip -o link show")
	if err != nil {
		return nil, err
	}
	var ifaces []HostInterface
	for _, line := range strings.Split(out, "\n") {
		// "2: eth0: <flags>..." or "3: eth0@if2: <flags>..."
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[1])
		if at := strings.Index(name, "@"); at >= 0 {
			name = name[:at]
		}
		if name == "" || name == "lo" {
			continue
		}
		ifaces = append(ifaces, HostInterface{Name: name})
	}
	return ifaces, nil
}

// localMangleChain is the mangle-OUTPUT counterpart of FIREWALL_MANGLE. Only PBR
// rules with ApplyToLocal set are installed here, so existing rules keep their
// PREROUTING-only (forwarded-traffic) semantics.
const localMangleChain = "FIREWALL_MANGLE_OUT"

// installLocalGuards writes the fixed prologue of FIREWALL_MANGLE_OUT. It runs on
// every rebuild, immediately after the chain is flushed and before any rule is
// appended, so the guards are always evaluated first.
//
// Two classes of packet must never be re-routed by a local PBR rule:
//
//  1. Packets that already carry a mark. A socket that set SO_MARK itself (and
//     wg-quick's fwmark convention) has already chosen its routing table.
//  2. WireGuard/AmneziaWG transport packets. These are UDP datagrams sent from an
//     interface's listen port to the peer's endpoint. Routing them into a tunnel
//     would encapsulate the tunnel inside itself — the classic PBR routing loop.
//     They are identified by source port, which is the listen port of one of our
//     own interfaces, and returned unmarked.
func (m *Manager) installLocalGuards(f fam) {
	for _, cmd := range localGuardCommands(f, wireGuardListenPorts()) {
		fwExec(cmd, 5*time.Second, true) //nolint
	}
}

// localGuardCommands builds the guard prologue for the given WireGuard listen ports.
//
// The same ports guard both families. A WireGuard interface has one listen port
// and speaks it over whichever transport its endpoint uses, so the IPv6 guard
// reads the same source of truth rather than duplicating a port list that could
// drift out of step with the IPv4 one.
func localGuardCommands(f fam, ports []int) []string {
	cmds := []string{
		fmt.Sprintf("%s -t mangle -A %s -m mark ! --mark 0 -j RETURN", f.ipt, localMangleChain),
	}
	for _, port := range ports {
		cmds = append(cmds, fmt.Sprintf("%s -t mangle -A %s -p udp --sport %d -j RETURN", f.ipt, localMangleChain, port))
	}
	return cmds
}

// wireGuardListenPorts returns the UDP listen ports of all configured tunnel
// interfaces. Read straight from SQLite rather than through internal/tunnel,
// which imports this package.
func wireGuardListenPorts() []int {
	rows, err := db.DB().Query(`SELECT DISTINCT listen_port FROM interfaces WHERE listen_port > 0 AND listen_port < 65536`)
	if err != nil {
		log.Printf("firewall: wireGuardListenPorts: %v", err)
		return nil
	}
	defer rows.Close()

	var ports []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			continue
		}
		ports = append(ports, p)
	}
	return ports
}

// localFlags adapts match flags built for PREROUTING to the OUTPUT hook, where
// there is no input interface. A rule pinned to a specific inbound interface has
// no meaning for locally generated traffic, so it is dropped from the OUT chain.
func localFlags(rule *Rule, flags string) (string, bool) {
	if rule.Interface != "" && rule.Interface != "any" {
		return "", false
	}
	return flags, true
}

// markLocal appends a MARK for a PBR rule to FIREWALL_MANGLE_OUT. Same fwmark,
// same ip rule and same routing table as the PREROUTING path — the only
// difference is the hook.
//
// One kernel property limits this for IPv6, and it is not something Cascade can
// work around: locally generated traffic is routed once before OUTPUT mangle
// runs and re-routed afterwards with the new mark. If that first lookup fails,
// connect() returns ENETUNREACH and no packet is ever produced, so the mark is
// never set. On a host with no IPv6 default route at all, an applyToLocal rule
// therefore cannot rescue a destination the main table cannot reach — forwarded
// traffic, marked in PREROUTING, is unaffected.
func (m *Manager) markLocal(rule *Rule, f fam, flags string) {
	lf, ok := localFlags(rule, flags)
	if !ok {
		log.Printf("firewall: rule %q: applyToLocal ignored — rules bound to interface %q match inbound traffic only",
			rule.Name, rule.Interface)
		return
	}
	cmd := fmt.Sprintf("%s -t mangle -A %s%s -j MARK --set-mark %d", f.ipt, localMangleChain, lf, *rule.Fwmark)
	if _, err := fwExec(cmd, 10*time.Second, true); err != nil {
		log.Printf("firewall: %s local mangle MARK %q: %v", f.tag, rule.Name, err)
	}
}

// jumpLocal is the subchain equivalent of markLocal: the address/ipset match goes
// in FIREWALL_MANGLE_OUT, the port match stays in the per-rule FM<id> subchain.
func (m *Manager) jumpLocal(rule *Rule, f fam, addrFlags, mangleChain string) {
	lf, ok := localFlags(rule, addrFlags)
	if !ok {
		log.Printf("firewall: rule %q: applyToLocal ignored — rules bound to interface %q match inbound traffic only",
			rule.Name, rule.Interface)
		return
	}
	cmd := fmt.Sprintf("%s -t mangle -A %s%s -j %s", f.ipt, localMangleChain, lf, mangleChain)
	fwExec(cmd, 10*time.Second, true) //nolint
}

// endLocalRule mirrors the PREROUTING first-match terminator in the OUT chain.
func (m *Manager) endLocalRule(f fam) {
	fwExec(fmt.Sprintf("%s -t mangle -A %s -m mark ! --mark 0 -j RETURN", f.ipt, localMangleChain),
		10*time.Second, true) //nolint
}

// ── Private: chain management ─────────────────────────────────────────────────

// initChains creates FIREWALL_FORWARD (filter) and FIREWALL_MANGLE (mangle)
// and hooks them at position 1 in their respective base chains (idempotent).
func (m *Manager) initChains() error {
	cmds := []string{
		// filter: FIREWALL_FORWARD — IPv4 only, see applyRuleKernelFamily.
		"iptables-nft -t filter -N FIREWALL_FORWARD 2>/dev/null || true",
		"iptables-nft -t filter -C FORWARD -j FIREWALL_FORWARD 2>/dev/null || iptables-nft -t filter -I FORWARD 1 -j FIREWALL_FORWARD",
	}
	// mangle chains exist in both families and carry the same names — the
	// binary already distinguishes them.
	for _, f := range families() {
		cmds = append(cmds,
			f.ipt+" -t mangle -N FIREWALL_MANGLE 2>/dev/null || true",
			f.ipt+" -t mangle -C PREROUTING -j FIREWALL_MANGLE 2>/dev/null || "+f.ipt+" -t mangle -I PREROUTING 1 -j FIREWALL_MANGLE",
			// FIREWALL_MANGLE_OUT — locally generated traffic (applyToLocal rules only).
			f.ipt+" -t mangle -N "+localMangleChain+" 2>/dev/null || true",
			f.ipt+" -t mangle -C OUTPUT -j "+localMangleChain+" 2>/dev/null || "+f.ipt+" -t mangle -I OUTPUT 1 -j "+localMangleChain,
		)
	}
	for _, cmd := range cmds {
		if _, err := fwExec(cmd, 10*time.Second, true); err != nil {
			log.Printf("firewall: initChains: %s: %v", cmd, err)
		}
	}
	return nil
}

// RebuildChains is the public entry point for rebuildChains.
// Called from main.go after WireGuard interfaces are up, and from interface
// start/restart handlers so that "ip route replace ... dev wgX table N"
// always runs with the interface already in existence.
func (m *Manager) RebuildChains() error {
	return m.rebuildChains()
}

// FlushAll flushes and removes Cascade-owned iptables chains.
// Used before a full restore so no stale rules survive the DB replacement.
func (m *Manager) FlushAll() {
	cmds := []string{
		"iptables-nft -t filter -F FIREWALL_FORWARD 2>/dev/null || true",
		"iptables-nft -t filter -D FORWARD -j FIREWALL_FORWARD 2>/dev/null || true",
		"iptables-nft -t filter -X FIREWALL_FORWARD 2>/dev/null || true",
	}
	for _, f := range families() {
		cmds = append(cmds,
			f.ipt+" -t mangle -F FIREWALL_MANGLE 2>/dev/null || true",
			f.ipt+" -t mangle -D PREROUTING -j FIREWALL_MANGLE 2>/dev/null || true",
			f.ipt+" -t mangle -X FIREWALL_MANGLE 2>/dev/null || true",
			f.ipt+" -t mangle -F "+localMangleChain+" 2>/dev/null || true",
			f.ipt+" -t mangle -D OUTPUT -j "+localMangleChain+" 2>/dev/null || true",
			f.ipt+" -t mangle -X "+localMangleChain+" 2>/dev/null || true",
		)
	}
	for _, cmd := range cmds {
		fwExec(cmd, 5*time.Second, true) //nolint:errcheck
	}
	log.Printf("firewall: FlushAll: Cascade chains removed")
}

// rebuildChains flushes both chains, cleans up PBR routing, then re-applies
// all enabled rules in order. Also resets fallback state.
func (m *Manager) rebuildChains() error {
	m.rebuildMu.Lock()
	defer m.rebuildMu.Unlock()

	// Drop the in-memory view of what is installed. Every rule is reconciled
	// from scratch below against live gateway health, so this bookkeeping is
	// rebuilt rather than carried over.
	m.StopPendingRouteRestores()
	m.fallbackMu.Lock()
	m.fallbackActive = make(map[stateKey]bool)
	m.fallbackMu.Unlock()
	m.routeStateMu.Lock()
	m.activeGateway = make(map[stateKey]resolvedGW)
	// Forcing appliedState empty makes the reconciliation below write every
	// route rather than short-circuit on a stale "already applied" record.
	m.appliedState = make(map[stateKey]pbrDesired)
	m.routeStateMu.Unlock()

	// Flush custom chains, both families.
	fwExec("iptables-nft -t filter -F FIREWALL_FORWARD", 5*time.Second, true) //nolint
	for _, f := range families() {
		fwExec(f.ipt+" -t mangle -F FIREWALL_MANGLE", 5*time.Second, true)   //nolint
		fwExec(f.ipt+" -t mangle -F "+localMangleChain, 5*time.Second, true) //nolint

		// Guard rules at the top of the local (OUTPUT) chain — see installLocalGuards.
		m.installLocalGuards(f)
	}

	// Remove per-rule subchains from previous run (FW*/FM* created by applyRuleKernelSubchain).
	m.cleanupSubchains()

	// Always allow ESTABLISHED/RELATED traffic first — return packets from the internet
	// to VPN clients must pass even when default policy is DROP.
	fwExec("iptables-nft -t filter -A FIREWALL_FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", 5*time.Second, true) //nolint

	// Clean up PBR routing rules from a previous run.
	if err := m.cleanupRoutingRules(); err != nil {
		log.Printf("firewall: cleanupRoutingRules: %v", err)
	}

	// Re-apply all enabled rules in order — always from the applied snapshot.
	rules, err := m.getAppliedRules()
	if err != nil {
		return err
	}

	count := 0
	ruleErrors := 0
	for _, rule := range rules {
		if rule.RuleType == "separator" {
			continue // separators are visual-only, no iptables commands
		}
		if !rule.Enabled {
			continue
		}
		if err := m.applyRuleKernel(&rule); err != nil {
			if errors.Is(err, errAliasMissing) {
				log.Printf("firewall: rule %q references a deleted alias — rule not installed "+
					"(installing it without the match would widen it to every address)", rule.Name)
			} else {
				log.Printf("firewall: applyRuleKernel %q: %v", rule.Name, err)
			}
			ruleErrors++
		}
		count++
	}

	// Terminal default policy for FIREWALL_FORWARD.
	// Added AFTER all per-rule ACCEPT/DROP/REJECT commands so it is always last.
	gs, err := settings.GetSettings()
	if err != nil {
		// DB error — cannot determine policy. Log and leave chain without terminal rule
		// (effective policy remains ACCEPT via base chain).
		log.Printf("firewall: rebuildChains: failed to read default policy: %v — terminal rule not appended", err)
	} else if gs.DefaultFwPolicy == "drop" {
		// M2: warn if any rule failed to install — a failed ACCEPT rule combined with
		// a terminal DROP may silently block traffic that was expected to be permitted.
		if ruleErrors > 0 {
			log.Printf("firewall: WARNING: default policy is DROP but %d rule(s) failed to install — some expected ACCEPT rules may be missing; verify connectivity", ruleErrors)
		}
		fwExec("iptables-nft -t filter -A FIREWALL_FORWARD -j DROP", 5*time.Second, true) //nolint
		log.Printf("firewall: default policy DROP appended to FIREWALL_FORWARD")
	}

	log.Printf("firewall: chains rebuilt (%d active rules)", count)
	return nil
}

// cleanupRoutingRules releases the PBR state that the rebuild about to follow
// will not reinstall — marks owned by rules that are disabled, are separators,
// or linger in one table but not the other.
//
// Marks belonging to rules the rebuild will reconcile are deliberately left
// alone. "ip route replace" overwrites in place, so there is nothing to clear
// first, and not clearing has two real benefits: the table is never momentarily
// empty (a restart used to leak matched traffic to the main table until the
// route was rewritten), and a route that cannot be rewritten yet — an interface
// that has not come up — keeps its previous, still-correct contents instead of
// being emptied. Policy-rule priorities also stay stable across rebuilds.
//
// The owned set spans the draft table and the applied snapshot: the kernel is
// built from the applied snapshot while allocation and deletion happen in the
// draft, so reading only one of them loses marks and leaves their ip rule and
// table behind.
func (m *Manager) cleanupRoutingRules() error {
	owned, err := pbrMarksInUse("")
	if err != nil {
		return err
	}

	applied, err := m.getAppliedRules()
	if err != nil {
		return err
	}
	keep := make(map[int]bool)
	for _, r := range applied {
		if r.RuleType == "separator" || !r.Enabled || r.Fwmark == nil {
			continue
		}
		keep[*r.Fwmark] = true
	}

	for fwmark := range owned {
		if !keep[fwmark] {
			m.releasePBRState(fwmark)
		}
	}
	return nil
}

// ── Private: apply rule to kernel ─────────────────────────────────────────────

// applyRuleKernel installs iptables-nft rules for a single firewall rule.
// It computes the cartesian product of port combinations × src endpoints × dst endpoints.
//
// iptables-nft limitation: a single rule cannot combine native nft expressions
// (protocol/port matches like "udp dport 53") with xt_compat expressions (ipset
// via "-m set --match-set"). The xt_compat match is silently dropped.
// When both are present, applyRuleKernelSubchain is used instead: address/ipset
// matching stays in FIREWALL_FORWARD (xt_compat only), port matching moves to a
// per-rule subchain (native nft only).
func (m *Manager) applyRuleKernel(rule *Rule) error {
	combos, err := m.buildPortCombinations(rule)
	if err != nil {
		return err
	}

	// A rule pointing at a deleted alias is not installed at all — in either
	// family, and without routing state. Checked before anything is written so
	// a broken rule cannot leave half of itself in the kernel.
	if err := m.checkEndpointAliases(rule); err != nil {
		return err
	}

	// Set up PBR routing once per rule, for both families (outside the
	// cartesian product loop).
	if isPBRRule(rule) {
		if err := m.applyRoutingForRule(rule); err != nil {
			log.Printf("firewall: applyRoutingForRule %q: %v", rule.Name, err)
		}
	}

	// One pass per address family. A family the rule cannot validly match in is
	// skipped entirely — no chain entries and, in the PBR case, no policy rule
	// or routing table either (see desiredRoutingState).
	for _, f := range families() {
		if err := m.applyRuleKernelFamily(rule, f, combos); err != nil {
			return err
		}
	}
	return nil
}

// checkEndpointAliases reports an error when either endpoint names an alias that
// no longer exists.
func (m *Manager) checkEndpointAliases(rule *Rule) error {
	for _, ep := range []*Endpoint{&rule.Source, &rule.Destination} {
		if ep.Type != "alias" || ep.AliasID == "" {
			continue
		}
		if spec, err := m.am.GetMatchSpec(ep.AliasID); err != nil || spec == nil {
			return fmt.Errorf("%w: %s", errAliasMissing, ep.AliasID)
		}
	}
	return nil
}

// isPBRRule reports whether a rule policy-routes its matches through a gateway.
func isPBRRule(rule *Rule) bool {
	return rule.Action == "accept" && (rule.GatewayID != "" || rule.GatewayGroupID != "")
}

// applyRuleKernelFamily installs one rule's kernel entries for one address family.
//
// Packet filtering stays IPv4-only. Cascade has never emitted ip6tables rules,
// so building a FIREWALL_FORWARD chain in the v6 filter table — complete with
// the terminal DROP that the default policy appends — would start blocking IPv6
// traffic that flows freely today. Stage 4 is about policy routing, so the v6
// pass populates the mangle chains only.
func (m *Manager) applyRuleKernelFamily(rule *Rule, f fam, combos []portCombo) error {
	srcParts, srcOK, err := m.buildMatchParts("src", &rule.Source, f)
	if err != nil {
		return err
	}
	dstParts, dstOK, err := m.buildMatchParts("dst", &rule.Destination, f)
	if err != nil {
		return err
	}
	if !srcOK || !dstOK {
		return nil // rule has no valid expression in this family
	}
	combos = combosFor(combos, f)
	if len(combos) == 0 {
		return nil
	}

	// Use subchain approach when mixing port matches (native nft) with ipset matches
	// (xt_compat) to avoid silent ipset-match loss.
	if anyComboHasPort(combos) && (anyPartIsIpset(srcParts) || anyPartIsIpset(dstParts)) {
		return m.applyRuleKernelSubchain(rule, f, combos, srcParts, dstParts)
	}

	isPBR := isPBRRule(rule)
	for _, combo := range combos {
		for _, srcPart := range srcParts {
			for _, dstPart := range dstParts {
				flags := buildMatchFlags(rule, combo, srcPart, dstPart)

				// Optional LOG target (IPv4 filter only).
				if rule.Log && !f.v6 {
					cmd := fmt.Sprintf(`%s -t filter -A FIREWALL_FORWARD%s -j LOG --log-prefix "FW: "`, f.ipt, flags)
					fwExec(cmd, 10*time.Second, true) //nolint
				}

				// Mangle MARK (PBR) or RETURN (non-PBR) — in PREROUTING/FIREWALL_MANGLE.
				if isPBR {
					cmd := fmt.Sprintf("%s -t mangle -A FIREWALL_MANGLE%s -j MARK --set-mark %d", f.ipt, flags, *rule.Fwmark)
					if _, err := fwExec(cmd, 10*time.Second, true); err != nil {
						log.Printf("firewall: %s mangle MARK %q: %v", f.tag, rule.Name, err)
					}
					if rule.ApplyToLocal {
						m.markLocal(rule, f, flags)
					}
				} else {
					// RETURN prevents downstream PBR rules from marking this traffic.
					cmd := fmt.Sprintf("%s -t mangle -A FIREWALL_MANGLE%s -j RETURN", f.ipt, flags)
					fwExec(cmd, 10*time.Second, true) //nolint
				}

				// Filter action (IPv4 only — see applyRuleKernelFamily).
				if !f.v6 {
					cmd := fmt.Sprintf("%s -t filter -A FIREWALL_FORWARD%s -j %s", f.ipt, flags, ruleTarget(rule))
					if _, err := fwExec(cmd, 10*time.Second, true); err != nil {
						log.Printf("firewall: filter %q: %v", rule.Name, err)
					}
				}
			}
		}
	}
	// PBR first-match semantics: once a mark is set, stop processing so that
	// subsequent (more general) PBR rules cannot override it.
	if isPBR {
		fwExec(f.ipt+" -t mangle -A FIREWALL_MANGLE -m mark ! --mark 0 -j RETURN", 10*time.Second, true) //nolint
		if rule.ApplyToLocal {
			m.endLocalRule(f)
		}
	}
	return nil
}

// combosFor adapts port/protocol combinations to one address family, dropping
// any that cannot be expressed there.
func combosFor(combos []portCombo, f fam) []portCombo {
	out := make([]portCombo, 0, len(combos))
	for _, c := range combos {
		proto, ok := f.protocolFor(c.proto)
		if !ok {
			continue
		}
		c.proto = proto
		out = append(out, c)
	}
	return out
}

// applyRuleKernelSubchain handles rules that combine ipset address matching with
// protocol/port matching. iptables-nft cannot combine these in one rule.
//
// Structure:
//
//	FIREWALL_FORWARD: -m set --match-set <ipset> src  →  JUMP FW<id8>   (xt_compat only)
//	FW<id8>:          -p udp --dport 53               →  ACCEPT          (native nft only)
//	FW<id8>:          (no match)                      →  RETURN
func (m *Manager) applyRuleKernelSubchain(rule *Rule, f fam, combos []portCombo, srcParts, dstParts []string) error {
	// Subchain name: "FW"/"FM" + first 8 hex chars of rule UUID (length = 10).
	// The same names are reused in the v6 tables — a different binary means a
	// different namespace, so there is nothing to disambiguate.
	shortID := strings.ReplaceAll(rule.ID, "-", "")[:8]
	filterChain := "FW" + shortID
	mangleChain := "FM" + shortID
	isPBR := isPBRRule(rule)

	// Create and flush subchains (idempotent). The filter subchain is IPv4 only.
	subchains := []struct{ table, chain string }{{"mangle", mangleChain}}
	if !f.v6 {
		subchains = append(subchains, struct{ table, chain string }{"filter", filterChain})
	}
	for _, tc := range subchains {
		fwExec(fmt.Sprintf("%s -t %s -N %s 2>/dev/null || true", f.ipt, tc.table, tc.chain), 5*time.Second, true)
		fwExec(fmt.Sprintf("%s -t %s -F %s", f.ipt, tc.table, tc.chain), 5*time.Second, true)
	}

	// Populate subchains with port-only rules (no address/ipset — avoids mixing).
	for _, combo := range combos {
		portFlags := buildPortFlags(combo)

		// Filter subchain: optional LOG + action (IPv4 only).
		if !f.v6 {
			if rule.Log {
				cmd := fmt.Sprintf(`%s -t filter -A %s%s -j LOG --log-prefix "FW: "`, f.ipt, filterChain, portFlags)
				fwExec(cmd, 10*time.Second, true) //nolint
			}
			cmd := fmt.Sprintf("%s -t filter -A %s%s -j %s", f.ipt, filterChain, portFlags, ruleTarget(rule))
			if _, err := fwExec(cmd, 10*time.Second, true); err != nil {
				log.Printf("firewall: subchain filter %q: %v", rule.Name, err)
			}
		}

		// Mangle subchain: MARK (PBR) or RETURN.
		if isPBR {
			cmd := fmt.Sprintf("%s -t mangle -A %s%s -j MARK --set-mark %d", f.ipt, mangleChain, portFlags, *rule.Fwmark)
			if _, err := fwExec(cmd, 10*time.Second, true); err != nil {
				log.Printf("firewall: subchain mangle MARK %q: %v", rule.Name, err)
			}
		} else {
			cmd := fmt.Sprintf("%s -t mangle -A %s%s -j RETURN", f.ipt, mangleChain, portFlags)
			fwExec(cmd, 10*time.Second, true) //nolint
		}
	}
	// Terminal RETURN: port not matched → fall through to next rule in FIREWALL_FORWARD.
	if !f.v6 {
		fwExec(fmt.Sprintf("%s -t filter -A %s -j RETURN", f.ipt, filterChain), 5*time.Second, true)
	}
	fwExec(fmt.Sprintf("%s -t mangle -A %s -j RETURN", f.ipt, mangleChain), 5*time.Second, true)

	// FIREWALL_FORWARD / FIREWALL_MANGLE: address-only matches → jump to subchains.
	// No port/proto here — xt_compat (ipset) remains isolated from native nft (port).
	for _, srcPart := range srcParts {
		for _, dstPart := range dstParts {
			addrFlags := buildAddrFlags(rule, srcPart, dstPart)

			if !f.v6 {
				cmd := fmt.Sprintf("%s -t filter -A FIREWALL_FORWARD%s -j %s", f.ipt, addrFlags, filterChain)
				if _, err := fwExec(cmd, 10*time.Second, true); err != nil {
					log.Printf("firewall: subchain jump filter %q: %v", rule.Name, err)
				}
			}
			cmd := fmt.Sprintf("%s -t mangle -A FIREWALL_MANGLE%s -j %s", f.ipt, addrFlags, mangleChain)
			fwExec(cmd, 10*time.Second, true) //nolint
			if isPBR && rule.ApplyToLocal {
				m.jumpLocal(rule, f, addrFlags, mangleChain)
			}
		}
	}
	// PBR first-match semantics: once a mark is set, stop processing so that
	// subsequent (more general) PBR rules cannot override it.
	if isPBR {
		fwExec(f.ipt+" -t mangle -A FIREWALL_MANGLE -m mark ! --mark 0 -j RETURN", 10*time.Second, true) //nolint
		if rule.ApplyToLocal {
			m.endLocalRule(f)
		}
	}
	return nil
}

// cleanupSubchains removes all FW*/FM* per-rule subchains left from a previous run.
// Called at the start of rebuildChains before re-applying rules.
func (m *Manager) cleanupSubchains() {
	type target struct {
		f             fam
		table, prefix string
	}
	targets := []target{
		{famV4, "filter", "FW"},
		{famV4, "mangle", "FM"},
		{famV6, "mangle", "FM"},
	}
	for _, tc := range targets {
		out, err := fwExec(fmt.Sprintf("%s -t %s -S", tc.f.ipt, tc.table), 5*time.Second, false)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			parts := strings.Fields(line)
			// Lines like "-N FWa1b2c3d4" — our chains are exactly 10 chars (prefix 2 + id 8).
			if len(parts) >= 2 && parts[0] == "-N" &&
				strings.HasPrefix(parts[1], tc.prefix) && len(parts[1]) == 10 {
				chain := parts[1]
				fwExec(fmt.Sprintf("%s -t %s -F %s 2>/dev/null || true", tc.f.ipt, tc.table, chain), 5*time.Second, true)
				fwExec(fmt.Sprintf("%s -t %s -X %s 2>/dev/null || true", tc.f.ipt, tc.table, chain), 5*time.Second, true)
			}
		}
	}
}

// applyRoutingForRule installs ip route + ip rule for a PBR rule.
//
// It reconciles rather than assuming health: a rule applied while its gateway is
// already down gets its fallback or blackhole route straight away, without
// waiting for the monitor to emit another transition.
func (m *Manager) applyRoutingForRule(rule *Rule) error {
	return m.reconcileRoutingForRule(rule)
}

func (m *Manager) withRuleApply(ruleID string, fn func() error) error {
	value, _ := m.ruleApplyMu.LoadOrStore(ruleID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// activeGatewayForRule returns the gateway a rule is currently routed through.
// Used for gateway-group change detection, which is family-independent — the
// group picks one gateway for the rule — so the IPv4 record answers, falling
// back to the IPv6 one for a rule that only routes IPv6.
func (m *Manager) activeGatewayForRule(ruleID string) (resolvedGW, bool) {
	m.routeStateMu.Lock()
	defer m.routeStateMu.Unlock()
	if gw, ok := m.activeGateway[stateKey{ruleID: ruleID}]; ok {
		return gw, true
	}
	gw, ok := m.activeGateway[stateKey{ruleID: ruleID, v6: true}]
	return gw, ok
}

// ruleInFallback reports whether a rule is on its fallback or blackhole route in
// either family. Either one is a reason to re-resolve on recovery.
func (m *Manager) ruleInFallback(ruleID string) bool {
	m.fallbackMu.Lock()
	defer m.fallbackMu.Unlock()
	for _, f := range families() {
		if m.fallbackActive[keyFor(ruleID, f)] {
			return true
		}
	}
	return false
}

// fwExec runs the iptables/ip6tables commands that build Cascade's chains.
//
// A package variable for the same reason as the routing seams below: it is the
// one place every chain command passes through, so a test can point the whole
// firewall at a network namespace and exercise the real binaries against a real
// kernel without touching the host.
var fwExec = util.Exec

var pbrRouteExec = util.Exec

// pbrRuleExec is kept separate from route execution so policy-rule handling can
// be tested without requiring CAP_NET_ADMIN in the test environment.
var pbrRuleExec = util.Exec

// ── Private: match building ───────────────────────────────────────────────────

// buildPortCombinations returns all protocol+port combinations for a rule.
// With port aliases: cartesian product of srcSpecs × dstSpecs (skip incompatible protos).
// Without port aliases: legacy path using rule.Protocol + plain port strings.
func (m *Manager) buildPortCombinations(rule *Rule) ([]portCombo, error) {
	srcHasAlias := rule.Source.PortAliasID != ""
	dstHasAlias := rule.Destination.PortAliasID != ""

	if !srcHasAlias && !dstHasAlias {
		// Legacy path.
		srcPort := strings.TrimSpace(rule.Source.Port)
		dstPort := strings.TrimSpace(rule.Destination.Port)
		hasPort := srcPort != "" || dstPort != ""

		protos := expandProtocol(rule.Protocol)
		var combos []portCombo
		for _, proto := range protos {
			if proto == "" && hasPort {
				// iptables requires -p <proto> before --sport/--dport.
				// When protocol is "any" but ports are specified, expand to both
				// tcp and udp so the port constraint is actually applied.
				for _, p := range []string{"tcp", "udp"} {
					combos = append(combos, portCombo{proto: p, srcPort: srcPort, dstPort: dstPort})
				}
			} else {
				combos = append(combos, portCombo{proto: proto, srcPort: srcPort, dstPort: dstPort})
			}
		}
		return combos, nil
	}

	// Port alias path.
	var srcSpecs []aliases.PortMatchSpec
	if srcHasAlias {
		specs, err := m.am.GetPortMatchSpec(rule.Source.PortAliasID)
		if err != nil {
			return nil, err
		}
		srcSpecs = specs
	} else {
		srcSpecs = []aliases.PortMatchSpec{{Proto: "", Ports: rule.Source.Port, Multiport: false}}
	}

	var dstSpecs []aliases.PortMatchSpec
	if dstHasAlias {
		specs, err := m.am.GetPortMatchSpec(rule.Destination.PortAliasID)
		if err != nil {
			return nil, err
		}
		dstSpecs = specs
	} else {
		dstSpecs = []aliases.PortMatchSpec{{Proto: "", Ports: rule.Destination.Port, Multiport: false}}
	}

	var combos []portCombo
	for _, src := range srcSpecs {
		for _, dst := range dstSpecs {
			// Skip incompatible protocols (a packet cannot be both TCP and UDP).
			if src.Proto != "" && dst.Proto != "" && src.Proto != dst.Proto {
				continue
			}
			proto := src.Proto
			if proto == "" {
				proto = dst.Proto
			}
			combos = append(combos, portCombo{
				proto:        proto,
				srcPort:      src.Ports,
				srcMultiport: src.Multiport,
				dstPort:      dst.Ports,
				dstMultiport: dst.Multiport,
			})
		}
	}

	if len(combos) == 0 {
		// All combos were filtered — return a no-protocol fallback.
		return []portCombo{{}}, nil
	}
	return combos, nil
}

// buildMatchFlags constructs the iptables match flag string for one cartesian cell.
// Example: " -i wg10 -p tcp -s 10.0.0.0/8 --sport 80 -d 8.8.8.8 --dport 53"
func buildMatchFlags(rule *Rule, combo portCombo, srcPart, dstPart string) string {
	var sb strings.Builder
	if rule.Interface != "" && rule.Interface != "any" {
		fmt.Fprintf(&sb, " -i %s", rule.Interface)
	}
	if combo.proto != "" {
		fmt.Fprintf(&sb, " -p %s", combo.proto)
	}
	// Port matches MUST come immediately after -p <proto>, before any -m set or address
	// matches. iptables-nft splits the rule incorrectly when --sport/--dport appear after
	// -m set, producing two separate rules (one with the set match, one bare) and losing
	// the port constraint entirely.
	if combo.proto != "" {
		if combo.srcPort != "" {
			sb.WriteString(portPartStr("--sport", combo.srcPort, combo.srcMultiport))
		}
		if combo.dstPort != "" {
			sb.WriteString(portPartStr("--dport", combo.dstPort, combo.dstMultiport))
		}
	}
	if srcPart != "" {
		sb.WriteByte(' ')
		sb.WriteString(srcPart)
	}
	if dstPart != "" {
		sb.WriteByte(' ')
		sb.WriteString(dstPart)
	}
	return sb.String()
}

// ruleTarget returns the iptables target string for a rule's action.
func ruleTarget(rule *Rule) string {
	switch strings.ToLower(rule.Action) {
	case "drop":
		return "DROP"
	case "reject":
		return "REJECT --reject-with icmp-port-unreachable"
	default:
		return "ACCEPT"
	}
}

// anyComboHasPort reports whether any portCombo has a src or dst port match
// that will actually be emitted (requires proto to be set — iptables cannot
// match ports without -p <proto>).
func anyComboHasPort(combos []portCombo) bool {
	for _, c := range combos {
		if c.proto != "" && (c.srcPort != "" || c.dstPort != "") {
			return true
		}
	}
	return false
}

// anyPartIsIpset reports whether any match-part string uses an ipset (-m set) match.
func anyPartIsIpset(parts []string) bool {
	for _, p := range parts {
		if strings.Contains(p, "--match-set") {
			return true
		}
	}
	return false
}

// buildAddrFlags builds match flags for interface and address/ipset matches only
// (no protocol or port). Used for the FIREWALL_FORWARD jump entry in the subchain approach.
func buildAddrFlags(rule *Rule, srcPart, dstPart string) string {
	var sb strings.Builder
	if rule.Interface != "" && rule.Interface != "any" {
		fmt.Fprintf(&sb, " -i %s", rule.Interface)
	}
	if srcPart != "" {
		sb.WriteByte(' ')
		sb.WriteString(srcPart)
	}
	if dstPart != "" {
		sb.WriteByte(' ')
		sb.WriteString(dstPart)
	}
	return sb.String()
}

// buildPortFlags builds match flags for protocol and port only (no address or interface).
// Used for rules inside per-rule subchains.
func buildPortFlags(combo portCombo) string {
	var sb strings.Builder
	if combo.proto != "" {
		fmt.Fprintf(&sb, " -p %s", combo.proto)
	}
	if combo.proto != "" {
		if combo.srcPort != "" {
			sb.WriteString(portPartStr("--sport", combo.srcPort, combo.srcMultiport))
		}
		if combo.dstPort != "" {
			sb.WriteString(portPartStr("--dport", combo.dstPort, combo.dstMultiport))
		}
	}
	return sb.String()
}

// portPartStr returns "--dport 443" or "-m multiport --dports 80,443".
func portPartStr(flag, ports string, multiport bool) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(ports), "-", ":")
	// Use multiport module when there are multiple ports or explicit multiport flag.
	if multiport || strings.Contains(normalized, ",") {
		pluralFlag := "--dports"
		if flag == "--sport" {
			pluralFlag = "--sports"
		}
		return fmt.Sprintf(" -m multiport %s %s", pluralFlag, normalized)
	}
	return fmt.Sprintf(" %s %s", flag, normalized)
}

// expandProtocol maps rule.Protocol to a slice of iptables protocol strings.
// "tcp/udp" → ["tcp","udp"], "any" → [""], "tcp" → ["tcp"]
func expandProtocol(protocol string) []string {
	if protocol == "" || protocol == "any" {
		return []string{""}
	}
	if protocol == "tcp/udp" {
		return []string{"tcp", "udp"}
	}
	return []string{protocol}
}

// errAliasMissing marks a rule that points at an alias which no longer exists.
//
// The compiler refuses such a rule outright instead of emitting it without the
// match. Dropping the match would silently widen the rule to every address —
// a PBR rule for one service would start policy-routing the whole internet —
// which is far worse than the rule being absent.
var errAliasMissing = errors.New("alias not found")

// buildMatchParts returns iptables source/destination match fragments for an
// endpoint in one address family.
//
// The bool reports whether the endpoint can be expressed in this family at all.
// False means the rule must not be emitted here: an IPv4 CIDR has no meaning to
// ip6tables, and an IPv4-only ipset produces a rule the kernel rejects. Only
// data that can validly match is compiled.
//
// Returns [""] for "any" (no -s/-d flag), ["-s CIDR"] for cidr, or expanded
// alias entries.
func (m *Manager) buildMatchParts(dir string, ep *Endpoint, f fam) ([]string, bool, error) {
	flag := "-s"
	matchDir := "src"
	if dir == "dst" {
		flag = "-d"
		matchDir = "dst"
	}

	if ep == nil || ep.Type == "" || ep.Type == "any" {
		return []string{""}, true, nil
	}

	invert := ""
	if ep.Invert {
		invert = "! "
	}

	if ep.Type == "cidr" {
		if err := validateCIDROrIP(ep.Value); err != nil {
			return nil, false, fmt.Errorf("buildMatchParts cidr: %w", err)
		}
		if !f.matchesFamily(ep.Value) {
			return nil, false, nil
		}
		return []string{fmt.Sprintf("%s%s %s", invert, flag, ep.Value)}, true, nil
	}

	if ep.Type == "alias" {
		spec, err := m.am.GetMatchSpec(ep.AliasID)
		if err != nil || spec == nil {
			return nil, false, fmt.Errorf("%w: %s", errAliasMissing, ep.AliasID)
		}
		if spec.Type == "ipset" {
			// One set per family. An alias with no set for this family (a plain
			// ipset or client-group alias, both hash:net family inet) simply
			// does not exist here.
			set := spec.Name
			if f.v6 {
				set = spec.NameV6
			}
			if set == "" {
				return nil, false, nil
			}
			return []string{fmt.Sprintf("-m set %s--match-set %s %s", invert, set, matchDir)}, true, nil
		}
		// CIDR-based alias: one fragment per entry, filtered to this family.
		// An alias with no entries at all keeps its existing "no constraint"
		// meaning; one whose entries are all of the other family does not apply
		// here and the rule is skipped.
		if len(spec.Entries) == 0 {
			return []string{""}, true, nil
		}
		var parts []string
		for _, cidr := range spec.Entries {
			if f.matchesFamily(cidr) {
				parts = append(parts, fmt.Sprintf("%s%s %s", invert, flag, cidr))
			}
		}
		if len(parts) == 0 {
			return nil, false, nil
		}
		return parts, true, nil
	}

	return []string{""}, true, nil
}

// ── Private: gateway resolution ───────────────────────────────────────────────

type resolvedGW struct {
	gatewayIP string
	iface     string
}

// resolveGateway finds the active gateway for a PBR rule, as an IPv4 next hop.
// Gateway groups use the shared health-aware tier resolver.
func (m *Manager) resolveGateway(rule *Rule) (resolvedGW, error) {
	gw, err := m.resolveGatewayObj(rule)
	if err != nil {
		return resolvedGW{}, err
	}
	return resolvedGW{gatewayIP: gw.GatewayIP, iface: gw.Interface}, nil
}

// resolveGatewayObj returns the gateway a rule currently routes through.
//
// Family-specific next hops are derived from it by gatewayRouteFor, which needs
// the whole object — the IPv6 next hop and the interface both live on it, and
// an IPv4-shaped resolvedGW cannot carry them.
func (m *Manager) resolveGatewayObj(rule *Rule) (*gateway.Gateway, error) {
	if rule.GatewayID != "" {
		gw, err := m.gm.GetGateway(rule.GatewayID)
		if err != nil || gw == nil {
			return nil, fmt.Errorf("gateway %s not found", rule.GatewayID)
		}
		return gw, nil
	}

	if rule.GatewayGroupID != "" {
		return m.gm.ResolveGroupGateway(rule.GatewayGroupID)
	}

	return nil, fmt.Errorf("rule has no gateway or gateway group")
}

// ipRuleExists checks whether an ip rule for fwmark already exists.
// Parses "ip rule show" text output (FIX-11).
func (m *Manager) ipRuleExists(fwmark int, f fam) bool {
	out, err := pbrRuleExec(f.ipr+" rule show", 5*time.Second, false)
	if err != nil {
		return false
	}
	hex := fmt.Sprintf("0x%x", fwmark)
	dec := fmt.Sprintf("%d", fwmark)
	return strings.Contains(out, "fwmark "+hex) || strings.Contains(out, "fwmark "+dec+" ")
}

// ── Private: gateway fallback ─────────────────────────────────────────────────

// handleGatewayStatusChange is the GatewayMonitor callback (FIX-15b).
//
// Edges are handled as before. A status change that is not an edge but reports a
// healthy gateway is also passed to onGatewayUp, because a rule can be parked in
// fallback with no recovery edge left to deliver: editing a gateway stops its
// monitor and starts a fresh one, whose first result is "unknown" → "healthy",
// an up-to-up change under the old test. onGatewayUp only acts on rules that are
// actually still in fallback, so this stays cheap and keeps the anti-flap delay.
func (m *Manager) handleGatewayStatusChange(gatewayID, newStatus, oldStatus string) error {
	isDown := func(s string) bool { return s == "down" || s == "admin_down" }
	switch {
	case isDown(newStatus) && !isDown(oldStatus):
		return m.onGatewayDown(gatewayID)
	case !isDown(newStatus):
		return m.onGatewayUp(gatewayID)
	}
	return nil
}

// onGatewayDown re-resolves affected Gateway Group rules even when the group
// still has another healthy member.
func (m *Manager) onGatewayDown(gatewayID string) error {
	rules, err := m.GetRules()
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if !rule.Enabled || rule.Fwmark == nil {
			continue
		}
		if rule.GatewayID == gatewayID {
			m.triggerFallback(&rule, fmt.Sprintf("gateway %s down", gatewayID))
			continue
		}
		if rule.GatewayGroupID != "" {
			contains, err := m.gm.GroupContainsGateway(rule.GatewayGroupID, gatewayID)
			if err != nil || !contains {
				continue
			}
			m.cancelRestoreTimer(rule.ID)
			allDown, err := m.isGroupAllDown(rule.GatewayGroupID, famV4)
			if err != nil {
				return err
			}
			if allDown {
				m.triggerFallback(&rule, fmt.Sprintf("all gateways in group %s down", rule.GatewayGroupID))
			} else if err := m.reapplyGatewayGroupRule(&rule, gatewayID); err != nil {
				log.Printf("firewall: re-resolve group %s for rule %q: %v", rule.GatewayGroupID, rule.Name, err)
			}
		}
	}
	return nil
}

// onGatewayUp schedules delayed re-resolution for affected rules. Group
// failback is required even when the group remains overall UP.
func (m *Manager) onGatewayUp(gatewayID string) error {
	rules, err := m.GetRules()
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if !rule.Enabled || rule.Fwmark == nil {
			continue
		}
		shouldSchedule := rule.GatewayID == gatewayID
		if rule.GatewayGroupID != "" {
			contains, err := m.gm.GroupContainsGateway(rule.GatewayGroupID, gatewayID)
			if err != nil {
				return err
			}
			if contains {
				allDown, err := m.isGroupAllDown(rule.GatewayGroupID, famV4)
				if err != nil {
					return err
				}
				shouldSchedule = !allDown
			}
		}
		if !shouldSchedule {
			continue
		}

		if rule.GatewayID == gatewayID {
			if !m.ruleInFallback(rule.ID) {
				continue
			}
		}
		if rule.GatewayGroupID != "" {
			desired, err := m.resolveGateway(&rule)
			if err != nil {
				return err
			}
			current, ok := m.activeGatewayForRule(rule.ID)
			if ok && current == desired && !m.ruleInFallback(rule.ID) {
				continue
			}
		}

		if shouldSchedule {
			m.scheduleRouteRestore(rule)
		}
	}
	return nil
}

// StopPendingRouteRestores cancels every scheduled anti-flap re-resolution and
// waits for any callback already running to finish. Used before a full rebuild,
// where each rule is reconciled immediately anyway, and on shutdown so no timer
// fires against a torn-down manager.
func (m *Manager) StopPendingRouteRestores() {
	m.fallbackMu.Lock()
	for _, t := range m.restoreTimers {
		if t.Stop() {
			m.restoreWG.Done() // cancelled before firing — its callback never runs
		}
	}
	m.restoreTimers = make(map[string]*time.Timer)
	m.fallbackMu.Unlock()
	m.restoreWG.Wait()
}

func (m *Manager) cancelRestoreTimer(ruleID string) {
	m.fallbackMu.Lock()
	defer m.fallbackMu.Unlock()
	if timer, ok := m.restoreTimers[ruleID]; ok {
		if timer.Stop() {
			m.restoreWG.Done()
		}
		delete(m.restoreTimers, ruleID)
	}
}

func (m *Manager) scheduleRouteRestore(rule Rule) {
	m.fallbackMu.Lock()
	// A restore already pending is left to run. Restarting it on every healthy
	// probe would push the deadline out forever and the rule would never leave
	// fallback. onGatewayDown cancels the timer explicitly when that is wanted.
	if _, pending := m.restoreTimers[rule.ID]; pending {
		m.fallbackMu.Unlock()
		return
	}
	log.Printf("firewall: rule %q: scheduling route re-resolution in %s", rule.Name, m.restoreDelay)
	m.restoreWG.Add(1)
	m.restoreTimers[rule.ID] = time.AfterFunc(m.restoreDelay, func() {
		defer m.restoreWG.Done()
		m.fallbackMu.Lock()
		delete(m.restoreTimers, rule.ID)
		m.fallbackMu.Unlock()
		if err := m.restoreRoute(&rule); err != nil {
			log.Printf("firewall: restoreRoute %q: %v", rule.Name, err)
		}
	})
	m.fallbackMu.Unlock()
}

// reapplyGatewayGroupRule re-resolves a group rule after one member changed
// state while the group as a whole is still up.
func (m *Manager) reapplyGatewayGroupRule(rule *Rule, gatewayID string) error {
	before, hadBefore := m.activeGatewayForRule(rule.ID)
	if err := m.reconcileRoutingForRule(rule); err != nil {
		return err
	}
	if after, ok := m.activeGatewayForRule(rule.ID); ok && (!hadBefore || before != after) {
		log.Printf("firewall: rule %q: Gateway Group route switched to %s via %s (gateway %s)",
			rule.Name, after.gatewayIP, after.iface, gatewayID)
	}
	return nil
}

// triggerFallback moves a rule onto its fallback or blackhole route because its
// gateway went down. The decision itself lives in desiredRoutingState, so this
// is the monitor-driven entry point into the same reconciliation used by apply
// and startup — the two can never disagree.
func (m *Manager) triggerFallback(rule *Rule, reason string) {
	m.cancelRestoreTimer(rule.ID)
	if err := m.reconcileRoutingForRule(rule); err != nil {
		log.Printf("firewall: triggerFallback %q (%s): %v", rule.Name, reason, err)
	}
}

// restoreRoute re-resolves a rule after its gateway recovered. Fires from the
// anti-flap timer; the desired state is recomputed from live health, so a
// gateway that went down again in the meantime simply stays in fallback.
func (m *Manager) restoreRoute(rule *Rule) error {
	return m.reconcileRoutingForRule(rule)
}

// getSystemDefaultGateway parses "ip route show default" for the host's default gw.
// Uses text output (FIX-11): "default via 192.168.1.1 dev eth0 ..."
func (m *Manager) getSystemDefaultGateway() (resolvedGW, error) {
	out, err := sysRouteExec("ip route show default", 5*time.Second, false)
	if err != nil {
		return resolvedGW{}, err
	}
	// Parse: "default via <ip> dev <iface> ..."
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// fields: [default, via, IP, dev, IFACE, ...]
		if len(fields) >= 5 && fields[0] == "default" && fields[1] == "via" && fields[3] == "dev" {
			return resolvedGW{gatewayIP: fields[2], iface: fields[4]}, nil
		}
	}
	return resolvedGW{}, fmt.Errorf("system default gateway not found in: %q", out)
}

// isGroupAllDown returns true when every member of a gateway group has status
// "down" or "admin_down" in the given address family.
//
// Judged per family: a group whose members answer IPv4 but have lost IPv6 is
// down for IPv6 routing and up for IPv4, and each family falls back on its own.
func (m *Manager) isGroupAllDown(groupID string, f fam) (bool, error) {
	grp, err := m.gm.GetGroup(groupID)
	if err != nil {
		return true, err
	}
	if grp == nil || len(grp.Gateways) == 0 {
		return true, nil
	}
	for _, member := range grp.Gateways {
		if !isDownStatus(m.gatewayFamilyStatus(member.GatewayID, f)) {
			return false, nil
		}
	}
	return true, nil
}

// ── Private: simulateTrace helpers ────────────────────────────────────────────

// matchEndpoint checks whether ip matches the endpoint condition.
func (m *Manager) matchEndpoint(ep *Endpoint, ip string) (bool, error) {
	if ep == nil || ep.Type == "" || ep.Type == "any" {
		return true, nil
	}

	var rawMatch bool
	var err error

	switch ep.Type {
	case "cidr":
		rawMatch = ipInCIDR(ip, ep.Value)
	case "alias":
		rawMatch, err = m.matchAlias(ep.AliasID, ip)
		if err != nil {
			return false, err
		}
	}

	if ep.Invert {
		return !rawMatch, nil
	}
	return rawMatch, nil
}

// matchAlias checks whether ip is a member of the named alias.
func (m *Manager) matchAlias(aliasID, ip string) (bool, error) {
	spec, err := m.am.GetMatchSpec(aliasID)
	if err != nil || spec == nil {
		// Return an error so the caller skips Invert logic — otherwise a missing
		// alias with Invert=true would produce !false=true (spurious match).
		return false, fmt.Errorf("alias %s not found", aliasID)
	}
	if spec.Type == "ipset" {
		return m.ipsetTest(spec.Name, ip), nil
	}
	for _, cidr := range spec.Entries {
		if ipInCIDR(ip, cidr) {
			return true, nil
		}
	}
	return false, nil
}

// ipsetTest runs "ipset test <name> <ip>" and returns true on exit 0.
// Both setName and ip are validated before shell interpolation (CRIT-2).
func (m *Manager) ipsetTest(setName, ip string) bool {
	if err := validate.IpsetName(setName); err != nil {
		return false
	}
	if err := validate.IP(ip); err != nil {
		return false
	}
	_, err := fwExec(fmt.Sprintf("ipset test %s %s", setName, ip), 3*time.Second, false)
	return err == nil
}

// ipInCIDR reports whether ip falls within cidr (e.g. "10.0.0.0/8").
// Uses net.ParseCIDR + ipNet.Contains — correct and handles host-bits-set CIDRs.
//
// Previous implementation used bits.RotateLeft32(^uint32(0), -prefixLen) for
// the subnet mask. Rotating all-ones by any amount always returns all-ones
// (0xFFFFFFFF), so the mask was never applied and only exact-address matches
// succeeded. This broke SimulateTrace CIDR source matching (FIX-GO-10).
func ipInCIDR(ipStr, cidr string) bool {
	if cidr == "" || ipStr == "" {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if !strings.Contains(cidr, "/") {
		// Host address without prefix — exact match.
		return ip.Equal(net.ParseIP(cidr))
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return ipNet.Contains(ip)
}

// ── Private: DB helpers ───────────────────────────────────────────────────────

type ruleScanner interface {
	Scan(dest ...any) error
}

func scanRule(rows *sql.Rows) (Rule, error) {
	r, err := scanRuleRow(rows)
	if err != nil {
		return Rule{}, err
	}
	return *r, nil
}

func scanRuleRow(s ruleScanner) (*Rule, error) {
	var r Rule
	var enabled, fallback, applyLocal, logVal int
	var srcJSON, dstJSON string
	var fwmark sql.NullInt64

	err := s.Scan(
		&r.ID, &r.RuleType, &r.Name, &enabled, &r.Order, &r.Interface, &r.Protocol,
		&srcJSON, &dstJSON, &r.Action,
		&r.GatewayID, &r.GatewayGroupID, &fwmark, &fallback,
		&applyLocal, &logVal, &r.Comment, &r.SeparatorColor, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	r.Enabled = enabled != 0
	r.FallbackToDefault = fallback != 0
	r.ApplyToLocal = applyLocal != 0
	r.Log = logVal != 0

	if fwmark.Valid {
		v := int(fwmark.Int64)
		r.Fwmark = &v
	}

	if srcJSON != "" && srcJSON != "{}" {
		_ = json.Unmarshal([]byte(srcJSON), &r.Source)
	}
	if dstJSON != "" && dstJSON != "{}" {
		_ = json.Unmarshal([]byte(dstJSON), &r.Destination)
	}

	// Normalise rule_type.
	if r.RuleType == "" {
		r.RuleType = "rule"
	}

	// Normalise action to lowercase.
	r.Action = strings.ToLower(r.Action)
	if r.Action == "" {
		r.Action = "accept"
	}

	return &r, nil
}

func insertRule(r Rule) error {
	srcJSON, _ := json.Marshal(r.Source)
	dstJSON, _ := json.Marshal(r.Destination)

	var fwmark interface{}
	if r.Fwmark != nil {
		fwmark = *r.Fwmark
	}

	ruleType := r.RuleType
	if ruleType == "" {
		ruleType = "rule"
	}
	_, err := db.DB().Exec(`
		INSERT INTO firewall_rules
		    (id, rule_type, name, enabled, order_idx, interface, protocol,
		     source, destination, action,
		     gateway_id, gateway_group_id, fwmark, fallback_to_default,
		     apply_to_local, log, comment, separator_color, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		r.ID, ruleType, r.Name, boolInt(r.Enabled), r.Order, r.Interface, r.Protocol,
		string(srcJSON), string(dstJSON), r.Action,
		r.GatewayID, r.GatewayGroupID, fwmark, boolInt(r.FallbackToDefault),
		boolInt(r.ApplyToLocal), boolInt(r.Log), r.Comment, r.SeparatorColor, r.CreatedAt,
	)
	return err
}

func updateRule(r Rule) error {
	srcJSON, _ := json.Marshal(r.Source)
	dstJSON, _ := json.Marshal(r.Destination)

	var fwmark interface{}
	if r.Fwmark != nil {
		fwmark = *r.Fwmark
	}

	_, err := db.DB().Exec(`
		UPDATE firewall_rules
		SET name = ?, enabled = ?, interface = ?, protocol = ?,
		    source = ?, destination = ?, action = ?,
		    gateway_id = ?, gateway_group_id = ?, fwmark = ?,
		    fallback_to_default = ?, apply_to_local = ?, log = ?, comment = ?
		WHERE id = ?
	`,
		r.Name, boolInt(r.Enabled), r.Interface, r.Protocol,
		string(srcJSON), string(dstJSON), r.Action,
		r.GatewayID, r.GatewayGroupID, fwmark,
		boolInt(r.FallbackToDefault), boolInt(r.ApplyToLocal), boolInt(r.Log), r.Comment,
		r.ID,
	)
	return err
}

// ── Private: validation + helpers ─────────────────────────────────────────────

func validateInput(inp RuleInput) error {
	if strings.TrimSpace(inp.Name) == "" {
		return fmt.Errorf("rule name is required")
	}
	if inp.Action != "" {
		switch strings.ToLower(inp.Action) {
		case "accept", "drop", "reject":
		default:
			return fmt.Errorf("action must be accept, drop, or reject")
		}
	}
	if inp.Interface != "" && inp.Interface != "any" {
		for _, c := range inp.Interface {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
				c == '.' || c == '-' || c == '_') {
				return fmt.Errorf("invalid interface name")
			}
		}
	}
	if inp.Protocol != "" {
		switch inp.Protocol {
		case "any", "tcp", "udp", "tcp/udp", "icmp":
		default:
			return fmt.Errorf("protocol must be any, tcp, udp, tcp/udp, or icmp")
		}
	}
	if inp.Source.Type == "cidr" {
		if strings.TrimSpace(inp.Source.Value) == "" {
			return fmt.Errorf("source CIDR value is required")
		}
		if err := validateCIDROrIP(inp.Source.Value); err != nil {
			return fmt.Errorf("source: %w", err)
		}
	}
	if inp.Destination.Type == "cidr" {
		if strings.TrimSpace(inp.Destination.Value) == "" {
			return fmt.Errorf("destination CIDR value is required")
		}
		if err := validateCIDROrIP(inp.Destination.Value); err != nil {
			return fmt.Errorf("destination: %w", err)
		}
	}
	return nil
}

// validateCIDROrIP rejects any value that is not a valid IP address or CIDR notation.
// This prevents command injection when the value is interpolated into iptables commands.
func validateCIDROrIP(s string) error {
	s = strings.TrimSpace(s)
	if net.ParseIP(s) != nil {
		return nil
	}
	if _, _, err := net.ParseCIDR(s); err == nil {
		return nil
	}
	return fmt.Errorf("invalid IP/CIDR value %q", s)
}

// normalizeEndpoint sanitises and fills defaults for an endpoint.
// Port fields are preserved even for type=any — a rule can match "any source,
// but only on port 53" which is perfectly valid.
func normalizeEndpoint(ep Endpoint) Endpoint {
	ep.Port = strings.TrimSpace(ep.Port)
	ep.PortAliasID = strings.TrimSpace(ep.PortAliasID)
	if ep.Type == "" || ep.Type == "any" {
		return Endpoint{
			Type:        "any",
			Port:        ep.Port,
			PortAliasID: ep.PortAliasID,
		}
	}
	ep.Value = strings.TrimSpace(ep.Value)
	ep.AliasID = strings.TrimSpace(ep.AliasID)
	return ep
}

// nextOrder returns max(order) + 1 across all existing rules.
func (m *Manager) nextOrder() (int, error) {
	var max int
	err := db.DB().QueryRow(`SELECT COALESCE(MAX(order_idx), 0) FROM firewall_rules`).Scan(&max)
	return max + 1, err
}

// nextFwmark returns the smallest integer >= 1000 not already used as a fwmark.
//
// Candidates are drawn from the draft table and the applied snapshot together. A
// mark whose rule was deleted from the draft but is still in the applied
// snapshot is still backing a live routing table, so handing it to a new rule
// would silently graft that rule onto the old one's routes.
func (m *Manager) nextFwmark() (int, error) {
	used, err := pbrMarksInUse("")
	if err != nil {
		return 0, err
	}

	mark := 1000
	for used[mark] {
		mark++
	}
	return mark, nil
}

// countRules returns the number of rules stored.
func (m *Manager) countRules() (int, error) {
	var n int
	err := db.DB().QueryRow(`SELECT COUNT(*) FROM firewall_rules`).Scan(&n)
	return n, err
}

// boolInt converts bool to 0/1 for SQLite.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// strOr returns s if non-empty, otherwise def.
func strOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// ── Singleton accessor ────────────────────────────────────────────────────────

var fwInstance *Manager

// SetInstance stores the initialized Manager for package-level access.
// Must be called from main() before serving requests.
func SetInstance(m *Manager) { fwInstance = m }

// Get returns the package-level Manager singleton.
// Panics with a clear message if SetInstance was not called (programming error).
func Get() *Manager {
	if fwInstance == nil {
		panic("firewall: manager not initialized — call SetInstance before Get()")
	}
	return fwInstance
}

// TryGet returns the package-level Manager singleton, or nil if not yet initialized.
// Prefer this over Get() in code that may run before SetInstance (e.g. tests).
func TryGet() *Manager { return fwInstance }
