package ipset

// Timeout-capable ipsets.
//
// The sets managed here differ from the ones in manager.go in three ways:
//
//   - type hash:ip (single addresses from DNS) instead of hash:net (CIDR prefixes),
//   - an explicit "timeout" property so the kernel expires entries on its own,
//   - both address families — "family inet" and "family inet6".
//
// They are also updated differently. The hash:net sets are rebuilt wholesale via
// a tmp-set swap (LoadFromFile / LoadEntries); these are updated *incrementally*
// with "add ... -exist", which refreshes the timer of an entry that is already
// present and leaves every other entry alone. Nothing is ever flushed, so a DNS
// failure cannot wipe a set — stale addresses simply age out.
//
// Contents are deliberately not persisted to a .save file: DNS is the source of
// truth, and the owning subsystem re-resolves on startup.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alexnikon/cascade/internal/util"
)

// restoreMu serialises writes of the shared "<name>_restore.tmp" scratch files in
// dataDir. The hash:net paths in manager.go are unsynchronised and get away with
// it because each set has a single writer; timeout sets are refreshed from
// background goroutines, so they need the guard.
var restoreMu sync.Mutex

// CreateTimeoutSet creates a hash:ip set with a default entry timeout, for the
// given address family. Idempotent via -exist.
//
// defaultTimeout is the timeout (in seconds) applied to entries added without an
// explicit one; per-entry timeouts from AddTimeoutEntries always win.
//
// Note that -exist will not change the properties of a set that already exists
// with a different type or family; such a set must be destroyed first.
func (m *Manager) CreateTimeoutSet(name string, v6 bool, defaultTimeout int) error {
	if err := m.validateName(name); err != nil {
		return err
	}
	if defaultTimeout <= 0 {
		return fmt.Errorf("ipset: defaultTimeout must be positive, got %d", defaultTimeout)
	}
	_, err := util.ExecDefault(fmt.Sprintf(
		"ipset create %s hash:ip family %s timeout %d -exist", name, family(v6), defaultTimeout))
	return err
}

// AddTimeoutEntries adds (or refreshes) entries in a timeout set, mapping each
// address to its timeout in seconds. Existing entries keep their place and have
// their timer reset; entries not mentioned are left untouched and expire on
// their own schedule. The whole batch goes through a single "ipset restore" so a
// refresh of N addresses costs one exec, not N.
//
// Returns the number of entries written.
func (m *Manager) AddTimeoutEntries(name string, entries map[string]int) (int, error) {
	if err := m.validateName(name); err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}

	var sb strings.Builder
	n := 0
	for ip, ttl := range entries {
		ip = strings.TrimSpace(ip)
		if ip == "" || ttl <= 0 {
			continue
		}
		// Reject anything that is not a bare address so a malformed DNS answer
		// can never turn into a shell fragment.
		if strings.ContainsAny(ip, " \t\n;|&$`<>()") {
			continue
		}
		fmt.Fprintf(&sb, "add %s %s timeout %d -exist\n", name, ip, ttl)
		n++
	}
	if n == 0 {
		return 0, nil
	}

	restoreMu.Lock()
	defer restoreMu.Unlock()

	tmpScript := filepath.Join(m.dataDir, name+"_to_restore.tmp")
	if err := os.WriteFile(tmpScript, []byte(sb.String()), 0644); err != nil {
		return 0, fmt.Errorf("write restore script: %w", err)
	}
	defer os.Remove(tmpScript) //nolint:errcheck

	if _, err := util.ExecDefault(fmt.Sprintf("ipset restore -! < %s", tmpScript)); err != nil {
		return 0, fmt.Errorf("ipset restore: %w", err)
	}
	return n, nil
}

// ListTimeoutEntries returns the addresses currently in a timeout set, with the
// trailing "timeout <n>" stripped. Returns nil if the set is missing or empty.
func (m *Manager) ListTimeoutEntries(name string) []string {
	var out []string
	for _, line := range m.ListEntries(name) {
		if addr, _, ok := strings.Cut(line, " "); ok {
			line = addr
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ListSetNames returns the names of every ipset currently present in the kernel.
// Used to reconcile away sets whose owning object no longer exists.
func (m *Manager) ListSetNames() []string {
	out, err := util.ExecFast("ipset list -n 2>/dev/null")
	if err != nil || out == "" {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

func family(v6 bool) string {
	if v6 {
		return "inet6"
	}
	return "inet"
}
