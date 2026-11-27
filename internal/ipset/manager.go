// Package ipset manages kernel ipsets used by FirewallManager for alias matching.
// Port of IpsetManager.js.
//
// Ipsets are persisted to DATA_DIR/*.save files and restored on startup via
// "ipset restore -!" so firewall rules survive container restarts.
//
// Async generation jobs (fetchCountry / fetchASN) run in goroutines and are
// tracked in a sync.Map keyed by a random hex job-ID.
package ipset

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/alexnikon/cascade/internal/util"
)

// JobStatus represents the current state of an async prefix-generation job.
type JobStatus struct {
	AliasID     string    `json:"-"`
	Status      string    `json:"status"`               // "running" | "done" | "error" | "unknown"
	EntryCount  int       `json:"entryCount,omitempty"` // populated when status == "done"
	CompletedAt time.Time `json:"-"`
	Error       string    `json:"error,omitempty"` // populated when status == "error"
}

// GenerateOpts controls which prefixes to fetch.
// Exactly one of Country, ASN, or ASNList must be set.
type GenerateOpts struct {
	Country string // 2-letter ISO country code, e.g. "RU"
	ASN     string // single ASN, e.g. "AS12345" or "12345"
	ASNList string // comma-separated ASNs, e.g. "12345,20485,3216"
}

// Manager manages kernel ipsets for firewall aliases.
type Manager struct {
	dataDir    string
	jobs       sync.Map // jobID (string) → JobStatus
	stop       chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
	workerOnce sync.Once
}

var nameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,30}$`)

// New creates a Manager, ensures the data directory exists, and restores saved ipsets.
func New(dataDir string) (*Manager, error) {
	m := &Manager{dataDir: dataDir, stop: make(chan struct{}), done: make(chan struct{})}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("ipset: mkdir %s: %w", dataDir, err)
	}
	m.RestoreAll()

	return m, nil
}

// RestoreAll loads all *.save files from the data directory into the kernel.
// Errors are logged but not returned — a missing ipset is non-fatal at startup.
func (m *Manager) RestoreAll() {
	entries, err := os.ReadDir(m.dataDir)
	if err != nil {
		log.Printf("ipset: restoreAll readdir %s: %v", m.dataDir, err)
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".save") {
			continue
		}
		filePath := filepath.Join(m.dataDir, e.Name())
		if _, err := util.ExecDefault(fmt.Sprintf("ipset restore -! < %s", filePath)); err != nil {
			log.Printf("ipset: restore %s: %v", e.Name(), err)
		} else {
			log.Printf("ipset: restored %s", e.Name())
		}
	}
}

// CreateSet creates an ipset of type hash:net (idempotent via -exist).
func (m *Manager) CreateSet(name string) error {
	if err := m.validateName(name); err != nil {
		return err
	}
	_, err := util.ExecDefault(fmt.Sprintf("ipset create %s hash:net family inet -exist", name))
	return err
}

// DestroyAll destroys all ipsets tracked by Cascade (.save files) and
// removes their save files. Used before a full restore.
func (m *Manager) DestroyAll() {
	entries, err := os.ReadDir(m.dataDir)
	if err != nil {
		log.Printf("ipset: DestroyAll readdir: %v", err)
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".save") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".save")
		util.ExecSilent(fmt.Sprintf("ipset destroy %s", name)) //nolint:errcheck
		os.Remove(filepath.Join(m.dataDir, e.Name()))          //nolint:errcheck
		log.Printf("ipset: DestroyAll: removed %s", name)
	}
}

// DestroySet destroys the named ipset and removes its save file.
//
// A set that is already absent is success. A set that is still present after the
// attempt is a real failure and is reported: the kernel refuses to destroy a set
// that an iptables rule still references, and swallowing that error is what used
// to leave a domain alias's sets orphaned until the next restart. Callers that
// destroy a set must first make sure nothing in the kernel points at it.
func (m *Manager) DestroySet(name string) error {
	if err := m.validateName(name); err != nil {
		return err
	}
	os.Remove(filepath.Join(m.dataDir, name+".save")) //nolint:errcheck

	if _, err := util.ExecSilent(fmt.Sprintf("ipset destroy %s", name)); err != nil {
		if m.SetExists(name) {
			return fmt.Errorf("destroy ipset %s: still in use by a kernel component: %w", name, err)
		}
	}
	return nil
}

// SetExists reports whether a set of this name is currently present in the kernel.
func (m *Manager) SetExists(name string) bool {
	if err := m.validateName(name); err != nil {
		return false
	}
	_, err := util.ExecSilentFast(fmt.Sprintf("ipset list -n %s", name))
	return err == nil
}

// LoadFromFile loads CIDRs from a plain-text file (one per line, # comments ignored)
// into the named ipset using an atomic swap via a temporary set.
// Returns the number of entries loaded.
func (m *Manager) LoadFromFile(name, filePath string) (int, error) {
	return m.loadFromFile(name, filePath, false)
}

// LoadAndSaveFromFile replaces the live set and saves it for restart.
func (m *Manager) LoadAndSaveFromFile(name, filePath string) (int, error) {
	return m.loadFromFile(name, filePath, true)
}

func (m *Manager) loadFromFile(name, filePath string, persist bool) (int, error) {
	if err := m.validateName(name); err != nil {
		return 0, err
	}
	tmpName := "cas_tmp_" + randomHex(8)

	cleanup := func() {
		util.ExecSilent(fmt.Sprintf("ipset destroy %s 2>/dev/null || true", tmpName)) //nolint:errcheck
	}

	// (Re)create the tmp set.
	util.ExecSilent(fmt.Sprintf("ipset destroy %s 2>/dev/null || true", tmpName)) //nolint:errcheck
	if _, err := util.ExecDefault(fmt.Sprintf("ipset create %s hash:net family inet -exist", tmpName)); err != nil {
		return 0, fmt.Errorf("create tmp set: %w", err)
	}

	// Read source file.
	data, err := os.ReadFile(filePath)
	if err != nil {
		cleanup()
		return 0, fmt.Errorf("read %s: %w", filePath, err)
	}

	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}

	// Build an ipset restore script for batch add (much faster than individual adds).
	var sb strings.Builder
	fmt.Fprintf(&sb, "create %s hash:net family inet -exist\n", tmpName)
	for _, cidr := range lines {
		fmt.Fprintf(&sb, "add %s %s -exist\n", tmpName, cidr)
	}

	tmpScript := filepath.Join(m.dataDir, name+"_restore.tmp")
	if err := os.WriteFile(tmpScript, []byte(sb.String()), 0644); err != nil {
		cleanup()
		return 0, fmt.Errorf("write restore script: %w", err)
	}
	defer os.Remove(tmpScript) //nolint:errcheck

	if _, err := util.ExecDefault(fmt.Sprintf("ipset restore -! < %s", tmpScript)); err != nil {
		cleanup()
		return 0, fmt.Errorf("ipset restore: %w", err)
	}

	// Ensure target set exists before the atomic swap.
	if _, err := util.ExecDefault(fmt.Sprintf("ipset create %s hash:net family inet -exist", name)); err != nil {
		cleanup()
		return 0, err
	}

	// Atomic swap: live set instantly gets all new entries.
	if _, err := util.ExecDefault(fmt.Sprintf("ipset swap %s %s", tmpName, name)); err != nil {
		cleanup()
		return 0, fmt.Errorf("ipset swap: %w", err)
	}
	if persist {
		if err := m.SaveSet(name); err != nil {
			if _, rollbackErr := util.ExecDefault(fmt.Sprintf("ipset swap %s %s", tmpName, name)); rollbackErr != nil {
				return 0, errors.Join(err, fmt.Errorf("restore previous ipset from %s: %w", tmpName, rollbackErr))
			}
			cleanup()
			return 0, err
		}
	}

	util.ExecSilent(fmt.Sprintf("ipset destroy %s 2>/dev/null || true", tmpName)) //nolint:errcheck

	return len(lines), nil
}

// LoadEntries atomically replaces the contents of the named ipset with the given entries.
// Each entry should be a bare IP address (e.g. "10.8.0.2") for hash:net/hash:ip sets.
// Uses the same tmp-swap approach as LoadFromFile for atomicity.
func (m *Manager) LoadEntries(name string, entries []string) (int, error) {
	if err := m.validateName(name); err != nil {
		return 0, err
	}
	tmpName := name + "_tmp"
	cleanup := func() {
		util.ExecSilent(fmt.Sprintf("ipset destroy %s 2>/dev/null || true", tmpName)) //nolint:errcheck
	}
	util.ExecSilent(fmt.Sprintf("ipset destroy %s 2>/dev/null || true", tmpName)) //nolint:errcheck
	if _, err := util.ExecDefault(fmt.Sprintf("ipset create %s hash:net family inet -exist", tmpName)); err != nil {
		return 0, fmt.Errorf("create tmp set: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "create %s hash:net family inet -exist\n", tmpName)
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		fmt.Fprintf(&sb, "add %s %s -exist\n", tmpName, e)
	}

	tmpScript := filepath.Join(m.dataDir, name+"_cg_restore.tmp")
	if err := os.WriteFile(tmpScript, []byte(sb.String()), 0644); err != nil {
		cleanup()
		return 0, fmt.Errorf("write restore script: %w", err)
	}
	defer os.Remove(tmpScript) //nolint:errcheck

	if _, err := util.ExecDefault(fmt.Sprintf("ipset restore -! < %s", tmpScript)); err != nil {
		cleanup()
		return 0, fmt.Errorf("ipset restore: %w", err)
	}
	if _, err := util.ExecDefault(fmt.Sprintf("ipset create %s hash:net family inet -exist", name)); err != nil {
		cleanup()
		return 0, err
	}
	if _, err := util.ExecDefault(fmt.Sprintf("ipset swap %s %s", tmpName, name)); err != nil {
		cleanup()
		return 0, fmt.Errorf("ipset swap: %w", err)
	}
	util.ExecSilent(fmt.Sprintf("ipset destroy %s 2>/dev/null || true", tmpName)) //nolint:errcheck
	return len(entries), nil
}

// SaveSet persists the named ipset to disk so it survives container restarts.
func (m *Manager) SaveSet(name string) error {
	if err := m.validateName(name); err != nil {
		return err
	}
	saveFile := filepath.Join(m.dataDir, name+".save")
	tmp := saveFile + ".tmp"
	defer os.Remove(tmp) //nolint:errcheck
	if _, err := util.ExecDefault(fmt.Sprintf("ipset save %s > %s", name, tmp)); err != nil {
		return err
	}
	return os.Rename(tmp, saveFile)
}

// GetEntryCount returns the number of entries in the named ipset (0 on error).
// ListEntries returns all CIDR entries currently in the named ipset.
// Parses `ipset list <name>` output — lines after "Members:" are the entries.
// Returns nil if the set doesn't exist or is empty.
func (m *Manager) ListEntries(name string) []string {
	out, err := util.ExecFast(fmt.Sprintf("ipset list %s 2>/dev/null", name))
	if err != nil || out == "" {
		return nil
	}
	var entries []string
	inMembers := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "Members:" {
			inMembers = true
			continue
		}
		if inMembers && line != "" {
			entries = append(entries, line)
		}
	}
	return entries
}

func (m *Manager) GetEntryCount(name string) int {
	out, err := util.ExecFast(fmt.Sprintf("ipset list %s -t 2>/dev/null", name))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "Number of entries: %d", &n); err == nil {
			return n
		}
	}
	return 0
}

// RunGenerator starts an async job that fetches prefixes from RIPEstat and loads
// them into the named ipset. Returns a job ID that can be polled with GetJobStatus.
func (m *Manager) RunGenerator(ctx context.Context, aliasID, name string, opts GenerateOpts, finish func(JobStatus) error) (string, error) {
	if err := m.validateName(name); err != nil {
		return "", err
	}
	jobID := randomHex(8)
	m.jobs.Store(jobID, JobStatus{AliasID: aliasID, Status: "running"})
	go func() {
		count, err := m.generate(ctx, name, opts)
		status := JobStatus{AliasID: aliasID, Status: "done", EntryCount: count}
		if err != nil {
			status.Status, status.Error = "error", err.Error()
		}
		if err := finish(status); err != nil {
			status.Status, status.Error = "error", fmt.Sprintf("save generation status: %v", err)
		}
		status.CompletedAt = time.Now()
		m.jobs.Store(jobID, status)
		log.Printf("ipset: job %s for %s: %s (%d entries) %s", jobID, name, status.Status, count, status.Error)
	}()
	return jobID, nil
}

func (m *Manager) GetJobStatus(jobID, aliasID string) JobStatus {
	if v, ok := m.jobs.Load(jobID); ok {
		status := v.(JobStatus)
		if status.AliasID == aliasID {
			return status
		}
	}
	return JobStatus{Status: "unknown"}
}

func (m *Manager) generate(ctx context.Context, name string, opts GenerateOpts) (int, error) {
	var prefixes []string
	var err error
	switch {
	case opts.ASNList != "":
		prefixes, err = fetchASNList(ctx, opts.ASNList)
	case opts.ASN != "":
		prefixes, err = fetchASN(ctx, opts.ASN)
	case opts.Country != "":
		prefixes, err = fetchCountry(ctx, opts.Country)
	}
	if err != nil {
		return 0, err
	}
	if len(prefixes) == 0 {
		return 0, errors.New("no prefixes returned — check source params")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	outFile := filepath.Join(m.dataDir, name+"_generated.txt")
	defer os.Remove(outFile) //nolint:errcheck
	if err := writeToFile(prefixes, outFile); err != nil {
		return 0, err
	}
	return m.LoadAndSaveFromFile(name, outFile)
}

func (m *Manager) validateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid ipset name: %q", name)
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}

// StartJobRetention starts the retention worker once, for the application lifecycle.
func (m *Manager) StartJobRetention() { m.workerOnce.Do(func() { go m.pruneJobs() }) }

// Close stops the job retention worker. It is safe to call repeatedly.
func (m *Manager) Close() {
	if m.stop == nil {
		return
	}
	m.StartJobRetention()
	m.closeOnce.Do(func() { close(m.stop) })
	<-m.done
}

func (m *Manager) pruneJobs() {
	defer close(m.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-ticker.C:
			m.pruneCompletedJobs(now)
		}
	}
}

func (m *Manager) pruneCompletedJobs(now time.Time) {
	m.jobs.Range(func(key, value any) bool {
		status := value.(JobStatus)
		if !status.CompletedAt.IsZero() && now.Sub(status.CompletedAt) >= time.Hour {
			m.jobs.CompareAndDelete(key, value)
		}
		return true
	})
}
