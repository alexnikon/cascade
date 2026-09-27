package gateway

// Manager handles CRUD for Gateway and GatewayGroup in SQLite,
// and controls the Monitor lifecycle for each gateway.
//
// Singleton pattern: call NewManager() once at startup, then Init()
// to load existing gateways from DB and start their monitors.
//
// JSON columns:
//   - gateways.monitor_http   → MonitorHttpConfig (JSON)
//   - gateway_groups.members  → []GatewayGroupMember (JSON)

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/remoteclient"
	"github.com/alexnikon/cascade/internal/util"
	"github.com/alexnikon/cascade/internal/validate"
)

// Manager manages gateways and gateway groups.
type Manager struct {
	monitor *Monitor
}

// NewManager creates a Manager with a freshly created Monitor.
// Call Init() to restore previously saved gateways.
func NewManager() *Manager {
	return &Manager{monitor: NewMonitor()}
}

// Monitor exposes the embedded Monitor so callers (e.g. FirewallManager)
// can register StatusChangeFunc callbacks.
func (m *Manager) Monitor() *Monitor {
	return m.monitor
}

// Init loads all gateways from SQLite and starts their monitors.
// Must be called after db.Init().
func (m *Manager) Init() error {
	gateways, err := m.GetGateways()
	if err != nil {
		return fmt.Errorf("gateway manager init: %w", err)
	}
	for _, gw := range gateways {
		m.monitor.Start(gw)
	}
	m.selfHealStaleGroupMembers()
	log.Printf("gateway-manager: init complete (%d gateways)", len(gateways))
	return nil
}

// ── Gateways ──────────────────────────────────────────────────────────────────

// GetGateways returns all gateways ordered by created_at.
func (m *Manager) GetGateways() ([]Gateway, error) {
	rows, err := db.DB().Query(`
		SELECT id, name, interface, gateway_ip, monitor_address,
		       gateway_ipv6, monitor_address_v6,
		       enabled, monitor, monitor_interval, window_seconds,
		       latency_threshold, monitor_http, monitor_rule,
		       description, admin_down, created_at
		FROM gateways ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Gateway
	for rows.Next() {
		gw, err := scanGateway(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, gw)
	}
	return out, rows.Err()
}

// GetGateway returns a single gateway by ID, or nil if not found.
func (m *Manager) GetGateway(id string) (*Gateway, error) {
	row := db.DB().QueryRow(`
		SELECT id, name, interface, gateway_ip, monitor_address,
		       gateway_ipv6, monitor_address_v6,
		       enabled, monitor, monitor_interval, window_seconds,
		       latency_threshold, monitor_http, monitor_rule,
		       description, admin_down, created_at
		FROM gateways WHERE id = ?
	`, id)
	gw, err := scanGatewayRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return gw, err
}

// GatewayInput is the create/update request payload.
type GatewayInput struct {
	Name             string            `json:"name"`
	Interface        string            `json:"interface"`
	GatewayIP        string            `json:"gatewayIP"`
	MonitorAddress   string            `json:"monitorAddress"`
	GatewayIPv6      string            `json:"gatewayIPv6"`      // optional IPv6 next hop
	MonitorAddressV6 string            `json:"monitorAddressV6"` // optional ICMPv6 probe target
	Enabled          *bool             `json:"enabled"`
	Monitor          *bool             `json:"monitor"`
	MonitorInterval  int               `json:"monitorInterval"`
	WindowSeconds    int               `json:"windowSeconds"`
	LatencyThreshold int               `json:"latencyThreshold"`
	MonitorHttp      MonitorHttpConfig `json:"monitorHttp"`
	MonitorRule      string            `json:"monitorRule"`
	Description      string            `json:"description"`
	AdminDown        *bool             `json:"adminDown"`
}

// CreateGateway persists a new gateway and starts its monitor.
func (m *Manager) CreateGateway(inp GatewayInput) (*Gateway, error) {
	if err := validateGatewayInput(inp); err != nil {
		return nil, err
	}

	gw := gatewayFromInput(inp)
	gw.ID = uuid.New().String()
	gw.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := insertGateway(gw); err != nil {
		return nil, err
	}

	// If the gateway interface is a WireGuard/AmneziaWG device, add a host route
	// so that the monitor can ping the inner tunnel IP (which has no route in the
	// main table when DisableRoutes=true). Without this the ping would fail with
	// "no route to host" even though the tunnel is up.
	ensureHostRoute(gw.GatewayIP, gw.Interface)

	m.monitor.Start(gw)
	log.Printf("gateway-manager: created gateway %q (%s)", gw.Name, gw.ID)
	return &gw, nil
}

// ensureHostRoute adds a /32 host route for ip via dev if it does not already exist.
// Used when a gateway points to a WireGuard tunnel IP (DisableRoutes=true interface)
// where the kernel has no route to the inner peer IP.
// Errors are logged and ignored — monitoring degrades gracefully.
func ensureHostRoute(ip, dev string) {
	if ip == "" || dev == "" {
		return
	}
	// Skip non-WireGuard interfaces (eth0, ens3, etc.) — they don't need this.
	if !strings.HasPrefix(dev, "wg") && !strings.HasPrefix(dev, "awg") {
		return
	}
	cmd := fmt.Sprintf("ip route replace %s/32 dev %s", ip, dev)
	if _, err := util.ExecDefault(cmd); err != nil {
		log.Printf("gateway: ensureHostRoute %s/32 dev %s: %v (monitoring may degrade)", ip, dev, err)
	}
}

// SetAdminDown updates only administrative state, preserving concurrent edits.
func (m *Manager) SetAdminDown(id string, down bool) (*Gateway, error) {
	result, err := db.DB().Exec(`UPDATE gateways SET admin_down = ? WHERE id = ?`, boolInt(down), id)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("gateway not found")
	}
	m.monitor.SetAdminDown(id, down)
	return m.GetGateway(id)
}

// UpdateGateway replaces gateway fields, persists, and restarts its monitor.
func (m *Manager) UpdateGateway(id string, inp GatewayInput) (*Gateway, error) {
	existing, err := m.GetGateway(id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("gateway not found")
	}
	if err := validateGatewayInput(inp); err != nil {
		return nil, err
	}

	gw := gatewayFromInput(inp)
	gw.ID = existing.ID
	gw.CreatedAt = existing.CreatedAt

	if err := updateGateway(gw); err != nil {
		return nil, err
	}
	if gw.MonitorAddress != existing.MonitorAddress {
		log.Printf("gateway-manager: %s: monitor target changed from %q to %q (empty uses gateway IP)", id, existing.MonitorAddress, gw.MonitorAddress)
	}

	// If only AdminDown changed, update the flag without resetting probe windows.
	// Otherwise restart monitor with updated parameters (resets all probe windows).
	adminDownChanged := gw.AdminDown != existing.AdminDown
	otherChanged := gw.Name != existing.Name ||
		gw.Interface != existing.Interface ||
		gw.GatewayIP != existing.GatewayIP ||
		gw.MonitorAddress != existing.MonitorAddress ||
		gw.Enabled != existing.Enabled ||
		gw.Monitor != existing.Monitor ||
		gw.MonitorInterval != existing.MonitorInterval ||
		gw.WindowSeconds != existing.WindowSeconds ||
		gw.MonitorRule != existing.MonitorRule

	if otherChanged {
		m.monitor.Start(gw)
	} else if adminDownChanged {
		m.monitor.SetAdminDown(gw.ID, gw.AdminDown)
	}
	log.Printf("gateway-manager: updated gateway %q (%s)", gw.Name, gw.ID)
	return &gw, nil
}

// DeleteGateway stops the monitor, removes the gateway from SQLite, and
// prunes any saved dashboard/diagnostics widgets referencing it — both in
// the same transaction, so a crash between the two can't leave widgets
// pointing at an already-deleted gateway forever (see #96).
func (m *Manager) DeleteGateway(id string) error {
	gw, err := m.GetGateway(id)
	if err != nil {
		return err
	}
	if gw == nil {
		return fmt.Errorf("gateway not found")
	}

	m.monitor.Stop(id)

	tx, err := db.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once committed

	if _, err := tx.Exec(`DELETE FROM gateways WHERE id = ?`, id); err != nil {
		return err
	}

	pruneDashboardWidgetsForGateway(tx, id)
	pruneGatewayFromGroups(tx, id)

	if err := tx.Commit(); err != nil {
		return err
	}

	log.Printf("gateway-manager: deleted gateway %q (%s)", gw.Name, id)
	return nil
}

// pruneDashboardWidgetsForGateway removes references to a deleted gateway's
// "gateway:<id>" metric key from every user's saved dashboard/diagnostics
// widgets. Without this, a stale widget keeps its dead gateway ID forever —
// dashboard_widgets is persisted in SQLite, so it survives process restarts
// and reboots, unlike the live gateway list.
//
// Runs inside the caller's transaction (see DeleteGateway) so the gateway
// row delete and the widget cleanup commit atomically. Per-row failures
// (bad JSON, a failed UPDATE) are logged and skipped rather than aborting
// the whole batch — this is best-effort cleanup, not something that should
// block the gateway deletion itself from succeeding.
func pruneDashboardWidgetsForGateway(tx *sql.Tx, id string) {
	graphKey := "gateway:" + id

	// Prefilter with LIKE before paying for a JSON decode per row — on an
	// install with many users/pages, most dashboard_widgets rows won't
	// mention this gateway at all.
	rows, err := tx.Query(`SELECT user_id, page, widgets FROM dashboard_widgets WHERE widgets LIKE '%' || ? || '%'`, graphKey)
	if err != nil {
		log.Printf("gateway-manager: prune widgets: query failed: %v", err)
		return
	}
	type widgetRow struct{ userID, page, widgetsJSON string }
	var candidates []widgetRow
	for rows.Next() {
		var r widgetRow
		if err := rows.Scan(&r.userID, &r.page, &r.widgetsJSON); err != nil {
			log.Printf("gateway-manager: prune widgets: scan failed: %v", err)
			continue
		}
		candidates = append(candidates, r)
	}
	// Must fully drain and close rows before issuing further statements on
	// this same transaction/connection — modernc.org/sqlite (like most Go
	// SQL drivers) can't interleave an open *sql.Rows with further tx.Exec
	// calls, and this project also runs with a single shared DB connection
	// (db.SetMaxOpenConns(1)), so there's no second connection to fall back
	// on. Buffer into candidates first, then close, then iterate+update.
	rows.Close()

	for _, r := range candidates {
		var widgets []map[string]interface{}
		if err := json.Unmarshal([]byte(r.widgetsJSON), &widgets); err != nil {
			log.Printf("gateway-manager: prune widgets: unmarshal failed for user %s/%s: %v", r.userID, r.page, err)
			continue
		}
		changed := FilterGraphRefs(widgets, func(key string) bool { return key == graphKey })
		if !changed {
			continue
		}
		out, err := json.Marshal(widgets)
		if err != nil {
			log.Printf("gateway-manager: prune widgets: marshal failed for user %s/%s: %v", r.userID, r.page, err)
			continue
		}
		if _, err := tx.Exec(`UPDATE dashboard_widgets SET widgets = ? WHERE user_id = ? AND page = ?`,
			string(out), r.userID, r.page); err != nil {
			log.Printf("gateway-manager: prune widgets: update failed for user %s/%s: %v", r.userID, r.page, err)
		}
	}
}

// pruneGatewayFromGroups removes gatewayID from every gateway_groups.members
// list that references it. Without this, deleting a gateway that's a member
// of a group leaves a "ghost" entry behind — GetGroup/ResolveGroupGateway
// still see a GatewayGroupMember pointing at an ID that no longer exists in
// the gateways table, which GetGateway/GroupContainsGateway then silently
// skip or misreport (see GitHub issue #106: a deleted gateway kept showing
// up as a group member indefinitely, surviving even the v0.9.7 update that
// only fixed the analogous dashboard-widget staleness, issue #96 — a
// different storage location for the same class of dangling reference).
//
// Runs inside the caller's transaction (see DeleteGateway) so the gateway
// row delete and the group membership cleanup commit atomically.
func pruneGatewayFromGroups(tx *sql.Tx, gatewayID string) {
	rows, err := tx.Query(`SELECT id, members FROM gateway_groups WHERE members LIKE '%' || ? || '%'`, gatewayID)
	if err != nil {
		log.Printf("gateway-manager: prune groups: query failed: %v", err)
		return
	}
	type groupRow struct{ id, membersJSON string }
	var candidates []groupRow
	for rows.Next() {
		var r groupRow
		if err := rows.Scan(&r.id, &r.membersJSON); err != nil {
			log.Printf("gateway-manager: prune groups: scan failed: %v", err)
			continue
		}
		candidates = append(candidates, r)
	}
	// Drain and close rows before issuing further statements on this same
	// transaction — see pruneDashboardWidgetsForGateway's identical comment;
	// same single-connection (SetMaxOpenConns(1)) constraint applies here.
	rows.Close()

	for _, r := range candidates {
		var members []GatewayGroupMember
		if err := json.Unmarshal([]byte(r.membersJSON), &members); err != nil {
			log.Printf("gateway-manager: prune groups: unmarshal failed for group %s: %v", r.id, err)
			continue
		}
		filtered := members[:0]
		changed := false
		for _, mem := range members {
			if mem.GatewayID == gatewayID {
				changed = true
				continue
			}
			filtered = append(filtered, mem)
		}
		if !changed {
			continue
		}
		out, err := json.Marshal(filtered)
		if err != nil {
			log.Printf("gateway-manager: prune groups: marshal failed for group %s: %v", r.id, err)
			continue
		}
		if _, err := tx.Exec(`UPDATE gateway_groups SET members = ? WHERE id = ?`, string(out), r.id); err != nil {
			log.Printf("gateway-manager: prune groups: update failed for group %s: %v", r.id, err)
		}
	}
}

// selfHealStaleGroupMembers removes any GatewayGroupMember from every group
// whose GatewayID no longer exists in the gateways table. Deleting a gateway
// prunes group membership going forward (see pruneGatewayFromGroups), but
// rows written before that fix shipped — or by any other path that could
// leave a dangling reference — never got cleaned up on their own. Called
// once from Init() so this runs automatically on every startup/update with
// no user action required, mirroring how getDashboardWidgets self-heals
// stale widget refs on read (issue #96) — except this runs proactively at
// startup rather than lazily on the next read, since group membership isn't
// naturally re-read on every page load the way dashboard widgets are.
//
// Best-effort: errors are logged, never returned — a failed self-heal here
// must not block server startup.
func (m *Manager) selfHealStaleGroupMembers() {
	groups, err := m.GetGroups()
	if err != nil {
		log.Printf("gateway-manager: self-heal group members: list groups failed: %v", err)
		return
	}
	if len(groups) == 0 {
		return
	}

	live := make(map[string]bool)
	gateways, err := m.GetGateways()
	if err != nil {
		log.Printf("gateway-manager: self-heal group members: list gateways failed: %v", err)
		return
	}
	for _, gw := range gateways {
		live[gw.ID] = true
	}

	healed := 0
	for _, grp := range groups {
		filtered := grp.Gateways[:0]
		changed := false
		for _, mem := range grp.Gateways {
			if !live[mem.GatewayID] {
				changed = true
				continue
			}
			filtered = append(filtered, mem)
		}
		if !changed {
			continue
		}
		out, err := json.Marshal(filtered)
		if err != nil {
			log.Printf("gateway-manager: self-heal group members: marshal failed for group %s: %v", grp.ID, err)
			continue
		}
		if _, err := db.DB().Exec(`UPDATE gateway_groups SET members = ? WHERE id = ?`, string(out), grp.ID); err != nil {
			log.Printf("gateway-manager: self-heal group members: update failed for group %s: %v", grp.ID, err)
			continue
		}
		healed++
	}
	if healed > 0 {
		log.Printf("gateway-manager: self-heal: removed stale member(s) from %d gateway group(s)", healed)
	}
}

// GetGatewayWithStatus combines gateway data with live monitoring status.
func (m *Manager) GetGatewayWithStatus(id string) (*GatewayWithStatus, error) {
	gw, err := m.GetGateway(id)
	if err != nil || gw == nil {
		return nil, err
	}
	ws := &GatewayWithStatus{Gateway: *gw, MonitorStatus: m.monitor.GetStatus(id)}
	if gw.AdminDown {
		ws.RealStatus = m.monitor.GetProbeStatus(id)
	}
	return ws, nil
}

// GetAllGatewaysWithStatus returns all gateways enriched with live status.
func (m *Manager) GetAllGatewaysWithStatus() ([]GatewayWithStatus, error) {
	gateways, err := m.GetGateways()
	if err != nil {
		return nil, err
	}
	out := make([]GatewayWithStatus, len(gateways))
	for i, gw := range gateways {
		out[i] = GatewayWithStatus{Gateway: gw, MonitorStatus: m.monitor.GetStatus(gw.ID)}
		if gw.AdminDown {
			out[i].RealStatus = m.monitor.GetProbeStatus(gw.ID)
		}
	}
	return out, nil
}

// ── Gateway Groups ────────────────────────────────────────────────────────────

// GetGroups returns all gateway groups ordered by created_at.
func (m *Manager) GetGroups() ([]GatewayGroup, error) {
	rows, err := db.DB().Query(`
		SELECT id, name, trigger, description, members, created_at
		FROM gateway_groups ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GatewayGroup
	for rows.Next() {
		grp, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, grp)
	}
	return out, rows.Err()
}

// GetGroup returns a single group by ID, or nil if not found.
func (m *Manager) GetGroup(id string) (*GatewayGroup, error) {
	row := db.DB().QueryRow(`
		SELECT id, name, trigger, description, members, created_at
		FROM gateway_groups WHERE id = ?
	`, id)
	grp, err := scanGroupRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return grp, err
}

// GroupContainsGateway reports whether gatewayID is a member of groupID.
func (m *Manager) GroupContainsGateway(groupID, gatewayID string) (bool, error) {
	grp, err := m.GetGroup(groupID)
	if err != nil {
		return false, err
	}
	if grp == nil {
		return false, nil
	}
	for _, member := range grp.Gateways {
		if member.GatewayID == gatewayID {
			return true, nil
		}
	}
	return false, nil
}

// ResolveGroupGateway returns the highest-priority healthy gateway in groupID.
// Gateway statuses down and admin_down are unavailable; unknown is treated as
// available until the monitor has enough probe data. If every member is down,
// the first tier gateway is returned as the existing last-resort selection.
func (m *Manager) ResolveGroupGateway(groupID string) (*Gateway, error) {
	grp, err := m.GetGroup(groupID)
	if err != nil {
		return nil, err
	}
	if grp == nil || len(grp.Gateways) == 0 {
		return nil, fmt.Errorf("gateway group %s not found or empty", groupID)
	}

	members := append([]GatewayGroupMember(nil), grp.Gateways...)
	sort.SliceStable(members, func(i, j int) bool {
		return members[i].Tier < members[j].Tier
	})

	var fallback *Gateway
	for _, member := range members {
		gw, err := m.GetGateway(member.GatewayID)
		if err != nil || gw == nil {
			continue
		}
		if fallback == nil {
			fallback = gw
		}
		status := m.monitor.GetStatus(member.GatewayID).Status
		if status != "down" && status != "admin_down" {
			return gw, nil
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("no valid gateway found in group %s", groupID)
}

// GatewayGroupInput is the create/update request payload.
type GatewayGroupInput struct {
	Name        string               `json:"name"`
	Trigger     string               `json:"trigger"`
	Description string               `json:"description"`
	Gateways    []GatewayGroupMember `json:"gateways"`
}

// CreateGroup persists a new gateway group.
func (m *Manager) CreateGroup(inp GatewayGroupInput) (*GatewayGroup, error) {
	if err := validateGroupInput(inp); err != nil {
		return nil, err
	}

	membersJSON, _ := json.Marshal(inp.Gateways)
	grp := GatewayGroup{
		ID:          uuid.New().String(),
		Name:        strings.TrimSpace(inp.Name),
		Trigger:     inp.Trigger,
		Description: strings.TrimSpace(inp.Description),
		Gateways:    inp.Gateways,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	_, err := db.DB().Exec(`
		INSERT INTO gateway_groups (id, name, trigger, description, members, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, grp.ID, grp.Name, grp.Trigger, grp.Description, string(membersJSON), grp.CreatedAt)
	if err != nil {
		return nil, err
	}

	log.Printf("gateway-manager: created group %q (%s)", grp.Name, grp.ID)
	return &grp, nil
}

// UpdateGroup replaces group fields and persists.
func (m *Manager) UpdateGroup(id string, inp GatewayGroupInput) (*GatewayGroup, error) {
	existing, err := m.GetGroup(id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("gateway group not found")
	}
	if err := validateGroupInput(inp); err != nil {
		return nil, err
	}

	members := inp.Gateways
	if members == nil {
		members = []GatewayGroupMember{}
	}
	membersJSON, _ := json.Marshal(members)

	_, err = db.DB().Exec(`
		UPDATE gateway_groups
		SET name = ?, trigger = ?, description = ?, members = ?
		WHERE id = ?
	`, strings.TrimSpace(inp.Name), inp.Trigger, strings.TrimSpace(inp.Description),
		string(membersJSON), id)
	if err != nil {
		return nil, err
	}

	grp := GatewayGroup{
		ID:          existing.ID,
		Name:        strings.TrimSpace(inp.Name),
		Trigger:     inp.Trigger,
		Description: strings.TrimSpace(inp.Description),
		Gateways:    members,
		CreatedAt:   existing.CreatedAt,
	}
	log.Printf("gateway-manager: updated group %q (%s)", grp.Name, id)
	return &grp, nil
}

// DeleteGroup removes a gateway group from SQLite.
func (m *Manager) DeleteGroup(id string) error {
	grp, err := m.GetGroup(id)
	if err != nil {
		return err
	}
	if grp == nil {
		return fmt.Errorf("gateway group not found")
	}

	if _, err := db.DB().Exec(`DELETE FROM gateway_groups WHERE id = ?`, id); err != nil {
		return err
	}

	log.Printf("gateway-manager: deleted group %q (%s)", grp.Name, id)
	return nil
}

// ── Private: DB helpers ───────────────────────────────────────────────────────

type gatewayScanner interface {
	Scan(dest ...any) error
}

func scanGateway(rows *sql.Rows) (Gateway, error) {
	gw, err := scanGatewayRow(rows)
	if err != nil || gw == nil {
		return Gateway{}, err
	}
	return *gw, nil
}

func scanGatewayRow(s gatewayScanner) (*Gateway, error) {
	var gw Gateway
	var enabled, monitor, adminDown int
	var monitorHttpJSON string

	err := s.Scan(
		&gw.ID, &gw.Name, &gw.Interface, &gw.GatewayIP, &gw.MonitorAddress,
		&gw.GatewayIPv6, &gw.MonitorAddressV6,
		&enabled, &monitor, &gw.MonitorInterval, &gw.WindowSeconds,
		&gw.LatencyThreshold, &monitorHttpJSON, &gw.MonitorRule,
		&gw.Description, &adminDown, &gw.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	gw.Enabled = enabled != 0
	gw.Monitor = monitor != 0
	gw.AdminDown = adminDown != 0

	if monitorHttpJSON != "" && monitorHttpJSON != "{}" {
		_ = json.Unmarshal([]byte(monitorHttpJSON), &gw.MonitorHttp)
	}

	// Apply defaults for zero values (mirrors Gateway.js constructor defaults).
	if gw.MonitorInterval == 0 {
		gw.MonitorInterval = 5
	}
	if gw.LatencyThreshold == 0 {
		gw.LatencyThreshold = 500
	}
	if gw.MonitorRule == "" {
		gw.MonitorRule = "icmp_only"
	}
	if gw.MonitorHttp.ExpectedStatus == 0 {
		gw.MonitorHttp.ExpectedStatus = 200
	}
	if gw.MonitorHttp.Interval == 0 {
		gw.MonitorHttp.Interval = 10
	}
	if gw.MonitorHttp.Timeout == 0 {
		gw.MonitorHttp.Timeout = 5
	}

	return &gw, nil
}

func insertGateway(gw Gateway) error {
	httpJSON, _ := json.Marshal(gw.MonitorHttp)
	_, err := db.DB().Exec(`
		INSERT INTO gateways
		    (id, name, interface, gateway_ip, monitor_address,
		     gateway_ipv6, monitor_address_v6,
		     enabled, monitor, monitor_interval, window_seconds,
		     latency_threshold, monitor_http, monitor_rule,
		     description, admin_down, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		gw.ID, gw.Name, gw.Interface, gw.GatewayIP, gw.MonitorAddress,
		gw.GatewayIPv6, gw.MonitorAddressV6,
		boolInt(gw.Enabled), boolInt(gw.Monitor), gw.MonitorInterval, gw.WindowSeconds,
		gw.LatencyThreshold, string(httpJSON), gw.MonitorRule,
		gw.Description, boolInt(gw.AdminDown), gw.CreatedAt,
	)
	return err
}

func updateGateway(gw Gateway) error {
	httpJSON, _ := json.Marshal(gw.MonitorHttp)
	_, err := db.DB().Exec(`
		UPDATE gateways
		SET name = ?, interface = ?, gateway_ip = ?, monitor_address = ?,
		    gateway_ipv6 = ?, monitor_address_v6 = ?,
		    enabled = ?, monitor = ?, monitor_interval = ?, window_seconds = ?,
		    latency_threshold = ?, monitor_http = ?, monitor_rule = ?,
		    description = ?, admin_down = ?
		WHERE id = ?
	`,
		gw.Name, gw.Interface, gw.GatewayIP, gw.MonitorAddress,
		gw.GatewayIPv6, gw.MonitorAddressV6,
		boolInt(gw.Enabled), boolInt(gw.Monitor), gw.MonitorInterval, gw.WindowSeconds,
		gw.LatencyThreshold, string(httpJSON), gw.MonitorRule,
		gw.Description, boolInt(gw.AdminDown), gw.ID,
	)
	return err
}

type groupScanner interface {
	Scan(dest ...any) error
}

func scanGroup(rows *sql.Rows) (GatewayGroup, error) {
	grp, err := scanGroupRow(rows)
	if err != nil {
		return GatewayGroup{}, err
	}
	return *grp, nil
}

func scanGroupRow(s groupScanner) (*GatewayGroup, error) {
	var grp GatewayGroup
	var membersJSON string

	err := s.Scan(&grp.ID, &grp.Name, &grp.Trigger, &grp.Description, &membersJSON, &grp.CreatedAt)
	if err != nil {
		return nil, err
	}

	if membersJSON != "" && membersJSON != "[]" {
		_ = json.Unmarshal([]byte(membersJSON), &grp.Gateways)
	}
	if grp.Gateways == nil {
		grp.Gateways = []GatewayGroupMember{}
	}
	if grp.Trigger == "" {
		grp.Trigger = "packetloss"
	}

	return &grp, nil
}

// ── Private: input helpers ────────────────────────────────────────────────────

// gatewayFromInput maps a GatewayInput to a Gateway with defaults applied.
func gatewayFromInput(inp GatewayInput) Gateway {
	enabled := true
	if inp.Enabled != nil {
		enabled = *inp.Enabled
	}
	monitor := true
	if inp.Monitor != nil {
		monitor = *inp.Monitor
	}
	adminDown := false
	if inp.AdminDown != nil {
		adminDown = *inp.AdminDown
	}

	interval := inp.MonitorInterval
	if interval <= 0 {
		interval = 5
	}
	threshold := inp.LatencyThreshold
	if threshold <= 0 {
		threshold = 500
	}
	rule := inp.MonitorRule
	if rule == "" {
		rule = "icmp_only"
	}

	http := inp.MonitorHttp
	if http.ExpectedStatus == 0 {
		http.ExpectedStatus = 200
	}
	if http.Interval == 0 {
		http.Interval = 10
	}
	if http.Timeout == 0 {
		http.Timeout = 5
	}

	return Gateway{
		Name:             strings.TrimSpace(inp.Name),
		Interface:        strings.TrimSpace(inp.Interface),
		GatewayIP:        strings.TrimSpace(inp.GatewayIP),
		MonitorAddress:   strings.TrimSpace(inp.MonitorAddress),
		GatewayIPv6:      strings.TrimSpace(inp.GatewayIPv6),
		MonitorAddressV6: strings.TrimSpace(inp.MonitorAddressV6),
		Enabled:          enabled,
		Monitor:          monitor,
		MonitorInterval:  interval,
		WindowSeconds:    inp.WindowSeconds,
		LatencyThreshold: threshold,
		MonitorHttp:      http,
		MonitorRule:      rule,
		Description:      strings.TrimSpace(inp.Description),
		AdminDown:        adminDown,
	}
}

func validateGatewayInput(inp GatewayInput) error {
	if strings.TrimSpace(inp.Name) == "" {
		return fmt.Errorf("gateway name is required")
	}
	if strings.TrimSpace(inp.Interface) == "" {
		return fmt.Errorf("interface is required")
	}
	if err := validate.IfaceName(strings.TrimSpace(inp.Interface)); err != nil {
		return fmt.Errorf("invalid interface: %w", err)
	}
	if strings.TrimSpace(inp.GatewayIP) == "" {
		return fmt.Errorf("gatewayIP is required")
	}
	if err := validate.IP(strings.TrimSpace(inp.GatewayIP)); err != nil {
		return fmt.Errorf("invalid gatewayIP: %w", err)
	}
	if err := validate.HostOrIP(strings.TrimSpace(inp.MonitorAddress)); err != nil {
		return fmt.Errorf("invalid monitorAddress: %w", err)
	}
	// IPv6 fields are optional; when present they must be IPv6, not IPv4.
	if v6 := strings.TrimSpace(inp.GatewayIPv6); v6 != "" {
		if err := validate.IP(v6); err != nil {
			return fmt.Errorf("invalid gatewayIPv6: %w", err)
		}
		if !isIPv6(v6) {
			return fmt.Errorf("gatewayIPv6 must be an IPv6 address")
		}
	}
	if v6 := strings.TrimSpace(inp.MonitorAddressV6); v6 != "" {
		if err := validate.HostOrIP(v6); err != nil {
			return fmt.Errorf("invalid monitorAddressV6: %w", err)
		}
		if !isHostname(v6) && !isIPv6(v6) {
			return fmt.Errorf("monitorAddressV6 must be an IPv6 address")
		}
	}
	rule := inp.MonitorRule
	if rule != "" && rule != "icmp_only" && rule != "http_only" && rule != "all" && rule != "any" {
		return fmt.Errorf("monitorRule must be icmp_only, http_only, all, or any")
	}
	if url := strings.TrimSpace(inp.MonitorHttp.URL); url != "" {
		if err := remoteclient.ValidateRemoteURL(url); err != nil {
			return fmt.Errorf("monitorHttp.url: %w", err)
		}
	}
	return nil
}

func validateGroupInput(inp GatewayGroupInput) error {
	if strings.TrimSpace(inp.Name) == "" {
		return fmt.Errorf("group name is required")
	}
	trigger := inp.Trigger
	if trigger != "" && trigger != "packetloss" && trigger != "latency" && trigger != "packetloss_latency" {
		return fmt.Errorf("trigger must be packetloss, latency, or packetloss_latency")
	}
	return nil
}

// boolInt converts bool to 0/1 for SQLite storage.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ── Singleton accessor ────────────────────────────────────────────────────────

var gwInstance *Manager

// SetInstance stores the initialized Manager for package-level access.
// Must be called from main() before serving requests.
func SetInstance(m *Manager) { gwInstance = m }

// Get returns the package-level Manager singleton.
// Panics with a clear message if SetInstance was not called (programming error).
func Get() *Manager {
	if gwInstance == nil {
		panic("gateway: manager not initialized — call SetInstance before Get()")
	}
	return gwInstance
}
