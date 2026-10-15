// Package webaccess restricts the administrative HTTP surface independently of VPN forwarding.
package webaccess

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const ProxyVersion = "1"

type Client struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Interface string `json:"interface"`
}

type Policy struct {
	Enabled     bool     `json:"enabled"`
	InterfaceID string   `json:"interfaceId"`
	Clients     []Client `json:"clients"`
}

type Rule struct {
	Source    netip.Prefix
	Interface string
}
type Prepared struct {
	Policy Policy
	Rules  []Rule
	Admin  []netip.Prefix
}
type Interface struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Enabled bool   `json:"enabled"`
}
type State struct {
	Policy    Policy     `json:"policy"`
	Pending   bool       `json:"pending"`
	ExpiresAt *time.Time `json:"expiresAt"`
	Error     string     `json:"error"`
}

// Dependencies keep kernel operations injectable for deterministic rollback tests.
type Dependencies struct {
	Prepare  func(Policy) (Prepared, error)
	Validate func(Prepared) error
	Apply    func([]Rule) error
	Trial    time.Duration
}

type Manager struct {
	mu                sync.Mutex
	lifecycle         sync.Mutex
	db                *sql.DB
	deps              Dependencies
	active, confirmed Prepared
	pending           bool
	expires           time.Time
	timer             *time.Timer
	err               string
	closed            bool
}

var current atomic.Pointer[Manager]

func SetManager(m *Manager) { current.Store(m) }

// LockInterfaceChange serializes destructive tunnel changes with policy activation.
// The caller must release the lock after the complete operation.
func LockInterfaceChange(id string) (func(), error) {
	m := current.Load()
	if m == nil {
		return func() {}, nil
	}
	m.lifecycle.Lock()
	m.mu.Lock()
	if (m.active.Policy.Enabled && m.active.Policy.InterfaceID == id) || (m.confirmed.Policy.Enabled && m.confirmed.Policy.InterfaceID == id) {
		m.mu.Unlock()
		m.lifecycle.Unlock()
		return nil, fmt.Errorf("interface %s protects web access; change or disable the web access policy first", id)
	}
	m.mu.Unlock()
	return m.lifecycle.Unlock, nil
}

func New(d *sql.DB, deps Dependencies) (*Manager, error) {
	if deps.Trial == 0 {
		deps.Trial = 120 * time.Second
	}
	m := &Manager{db: d, deps: deps}
	var raw string
	if err := d.QueryRow("SELECT policy FROM web_access WHERE id=1").Scan(&raw); err != nil {
		return nil, err
	}
	var p Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("invalid stored web access policy: %w", err)
	}
	// Restore the confirmed policy even if its VPN is temporarily absent. Kernel
	// guards must be installed before HTTP starts; an absent VPN never opens access.
	prepared, err := deps.Prepare(p)
	if err != nil {
		m.active.Policy = p
		m.confirmed = m.active
		m.err = "web access unavailable: " + err.Error()
		return m, err
	}
	m.active, m.confirmed = prepared, prepared
	if err = deps.Apply(prepared.Rules); err != nil {
		m.err = "web access guards unavailable: " + err.Error()
		return m, err
	}
	return m, nil
}

func clonePolicy(p Policy) Policy { p.Clients = append([]Client{}, p.Clients...); return p }
func (m *Manager) State() State   { m.mu.Lock(); defer m.mu.Unlock(); return m.state() }
func (m *Manager) state() State {
	s := State{Policy: clonePolicy(m.active.Policy), Pending: m.pending, Error: m.err}
	if m.pending {
		t := m.expires
		s.ExpiresAt = &t
	}
	return s
}

func (m *Manager) Trial(p Policy) (State, error) {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return State{}, errors.New("web access manager is closed")
	}
	if m.pending {
		return State{}, errors.New("confirm or cancel the existing trial first")
	}
	next, err := m.deps.Prepare(p)
	if err != nil {
		return State{}, err
	}
	if m.deps.Validate != nil {
		if err := m.deps.Validate(next); err != nil {
			return State{}, err
		}
	}
	guards := next.Rules
	// Keep the previous VPN's ingress check until a disabling trial is confirmed.
	if !next.Policy.Enabled {
		guards = m.confirmed.Rules
	}
	if err = m.deps.Apply(guards); err != nil {
		if rollback := m.deps.Apply(m.active.Rules); rollback != nil {
			m.err = "kernel rollback failed: " + rollback.Error()
			m.pending = true
			m.expires = time.Now().UTC()
			m.timer = time.AfterFunc(5*time.Second, m.expire)
		}
		return State{}, fmt.Errorf("apply web access guards: %w", err)
	}
	m.active = next
	m.pending = true
	m.err = ""
	m.expires = time.Now().UTC().Add(m.deps.Trial)
	m.timer = time.AfterFunc(m.deps.Trial, m.expire)
	return m.state(), nil
}

func (m *Manager) expire() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.pending {
		return
	}
	if err := m.rollback(); err != nil {
		m.err = "rollback pending: " + err.Error()
		m.timer = time.AfterFunc(5*time.Second, m.expire)
	}
}

func (m *Manager) rollback() error {
	if err := m.deps.Apply(m.confirmed.Rules); err != nil {
		return err
	}
	m.active = m.confirmed
	m.pending = false
	m.err = ""
	if m.timer != nil {
		m.timer.Stop()
	}
	return nil
}

func (m *Manager) Cancel() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.pending {
		return m.state(), nil
	}
	if err := m.rollback(); err != nil {
		m.err = err.Error()
		return m.state(), err
	}
	return m.state(), nil
}

// Confirm checks the actual request origin against the candidate VPN. Disabling
// a policy is confirmed through the previously configured VPN instead.
func (m *Manager) Confirm(ip netip.Addr) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.pending || !time.Now().Before(m.expires) {
		return State{}, errors.New("no unexpired trial")
	}
	if m.err != "" {
		return State{}, errors.New(m.err)
	}
	check := m.active
	if !check.Policy.Enabled {
		check = m.confirmed
	}
	if check.Policy.Enabled && !isAdmin(check, ip) {
		return State{}, errors.New("confirm through the selected administrative VPN")
	}
	if !m.active.Policy.Enabled {
		if err := m.deps.Apply(nil); err != nil {
			if rollback := m.deps.Apply(m.confirmed.Rules); rollback != nil {
				m.err = "kernel restore failed: " + rollback.Error()
			}
			return State{}, err
		}
	}
	raw, err := json.Marshal(m.active.Policy)
	if err != nil {
		return State{}, err
	}
	if _, err = m.db.Exec("UPDATE web_access SET policy=? WHERE id=1", string(raw)); err != nil {
		if rollback := m.deps.Apply(check.Rules); rollback != nil {
			m.err = "kernel restore failed: " + rollback.Error()
		}
		return State{}, err
	}
	m.confirmed = m.active
	m.pending = false
	if m.timer != nil {
		m.timer.Stop()
	}
	return m.state(), nil
}

func isAdmin(p Prepared, ip netip.Addr) bool {
	for _, prefix := range p.Admin {
		if prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// Classify returns admin, machine, public, or denied. Incoming-interface guards
// enforce the source/interface pairing before Caddy accepts the connection.
func (m *Manager) Classify(ip netip.Addr) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != "" {
		return "denied"
	}
	if !m.active.Policy.Enabled {
		return "public"
	}
	if isAdmin(m.active, ip) {
		return "admin"
	}
	for _, c := range m.active.Policy.Clients {
		a, _ := netip.ParseAddr(c.IP)
		if a == ip.Unmap() {
			return "machine"
		}
	}
	return "denied"
}

// CanManageOrigin retains the previous VPN requirement during a disabling trial.
func (m *Manager) CanManageOrigin(ip netip.Addr) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != "" {
		return false
	}
	if m.active.Policy.Enabled {
		return isAdmin(m.active, ip)
	}
	if m.pending && m.confirmed.Policy.Enabled {
		return isAdmin(m.confirmed, ip)
	}
	return true
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.timer != nil {
		m.timer.Stop()
	}
}

func Reset(d *sql.DB) error {
	_, err := d.Exec(`UPDATE web_access SET policy='{"enabled":false,"interfaceId":"","clients":[]}' WHERE id=1`)
	return err
}

func Interfaces(d *sql.DB) ([]Interface, error) {
	rows, err := d.Query("SELECT id,name,address,enabled FROM interfaces ORDER BY name")
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

func validInterface(s string) bool {
	if len(s) == 0 || len(s) > 15 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return s != "lo"
}

// Prepare validates persistent identities; active kernel existence is checked
// separately when a new trial is requested, not during cold-start recovery.
func Prepare(d *sql.DB, p Policy) (Prepared, error) {
	p = clonePolicy(p)
	result := Prepared{Policy: p}
	if !p.Enabled {
		return result, nil
	}
	if !validInterface(p.InterfaceID) {
		return result, errors.New("select an administrative VPN interface")
	}
	var address string
	var enabled bool
	if err := d.QueryRow("SELECT address,enabled FROM interfaces WHERE id=?", p.InterfaceID).Scan(&address, &enabled); err != nil {
		return result, errors.New("administrative interface does not exist")
	}
	if !enabled {
		return result, errors.New("administrative interface is disabled")
	}
	for _, raw := range strings.Split(address, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return result, fmt.Errorf("invalid administrative interface address: %w", err)
		}
		if !prefix.Addr().IsGlobalUnicast() || prefix.Addr().Is4In6() || prefix.Addr().IsLoopback() || prefix.Addr().IsLinkLocalUnicast() || prefix.Bits() == 0 {
			return result, errors.New("administrative VPN requires a unicast subnet")
		}
		result.Admin = append(result.Admin, prefix.Masked())
		result.Rules = append(result.Rules, Rule{prefix.Masked(), p.InterfaceID})
	}
	if len(p.Clients) > 64 {
		return result, errors.New("at most 64 API clients are allowed")
	}
	seen := map[netip.Addr]bool{}
	for n, c := range p.Clients {
		a, err := netip.ParseAddr(strings.TrimSpace(c.IP))
		a = a.Unmap()
		if err != nil || !a.IsGlobalUnicast() || a.Zone() != "" || a.IsLoopback() || a.IsLinkLocalUnicast() {
			return result, fmt.Errorf("API client %d requires an exact unicast IP", n+1)
		}
		if seen[a] {
			return result, errors.New("duplicate API client IP")
		}
		seen[a] = true
		if !validInterface(c.Interface) {
			return result, errors.New("API client requires a valid ingress interface")
		}
		if c.Interface == p.InterfaceID {
			return result, errors.New("API clients on the administrative VPN do not need an exception")
		}
		if isAdmin(result, a) {
			return result, errors.New("API client IP overlaps the administrative subnet")
		}
		p.Clients[n].IP = a.String()
		p.Clients[n].Name = strings.TrimSpace(c.Name)
		if len(p.Clients[n].Name) > 100 {
			return result, errors.New("API client name is too long")
		}
		result.Rules = append(result.Rules, Rule{netip.PrefixFrom(a, a.BitLen()), c.Interface})
	}
	result.Policy = p
	return result, nil
}

func CheckRuntime(p Policy, bind, network string) error {
	if !p.Enabled {
		return nil
	}
	ip, err := netip.ParseAddr(bind)
	if runtime.GOOS != "linux" || network != "host" || err != nil || !ip.IsLoopback() {
		return errors.New("VPN-only web access requires Linux host networking and a loopback backend bind")
	}
	return nil
}

func CheckLive(prepared Prepared) error {
	if !prepared.Policy.Enabled {
		return nil
	}
	for _, r := range prepared.Rules {
		i, err := net.InterfaceByName(r.Interface)
		if err != nil {
			return fmt.Errorf("interface %s is unavailable", r.Interface)
		}
		if i.Flags&net.FlagUp == 0 {
			return fmt.Errorf("interface %s is down", r.Interface)
		}
	}
	i, _ := net.InterfaceByName(prepared.Policy.InterfaceID)
	addresses, err := i.Addrs()
	if err != nil {
		return err
	}
	for _, prefix := range prepared.Admin {
		found := false
		for _, raw := range addresses {
			a, e := netip.ParsePrefix(raw.String())
			if e == nil && a.Masked() == prefix {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("administrative subnet %s is not installed", prefix)
		}
	}
	return nil
}
