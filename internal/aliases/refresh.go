package aliases

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/alexnikon/cascade/internal/ipset"
)

var ErrGenerationRunning = errors.New("ipset generation is already running")

type AutoRefresh struct {
	Enabled   bool       `json:"enabled"`
	Days      int        `json:"days"`
	NextAt    *time.Time `json:"nextAt,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

func marshalRefresh(config *AutoRefresh) string {
	if config == nil {
		return ""
	}
	data, _ := json.Marshal(config)
	return string(data)
}

func configureRefresh(a *Alias, config *AutoRefresh, now time.Time) error {
	if a.Type != "ipset" || a.GeneratorOpts == nil {
		return errors.New("auto-refresh requires an ipset with a generation source")
	}
	switch config.Days {
	case 1, 3, 7, 30:
	default:
		return errors.New("auto-refresh period must be 1, 3, 7, or 30 days")
	}
	if a.AutoRefresh != nil && a.AutoRefresh.Enabled == config.Enabled && a.AutoRefresh.Days == config.Days {
		return nil
	}
	a.AutoRefresh = &AutoRefresh{Enabled: config.Enabled, Days: config.Days}
	if config.Enabled {
		a.AutoRefresh.schedule(now, time.Duration(config.Days)*24*time.Hour)
	}
	return nil
}

func (r *AutoRefresh) schedule(now time.Time, delay time.Duration) {
	next := now.UTC().Add(delay)
	r.NextAt = &next
}

func (m *Manager) StartGenerate(id string, opts GeneratorOpts, refresh *AutoRefresh) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.getOrNotFound(id)
	if err != nil {
		return "", err
	}
	if a.Type != "ipset" {
		return "", errors.New("generate only supported for ipset-type aliases")
	}
	if strings.TrimSpace(opts.Country) == "" && strings.TrimSpace(opts.ASN) == "" && strings.TrimSpace(opts.ASNList) == "" {
		return "", errors.New("generation requires a country, ASN, or ASN list")
	}
	if m.running[a.IPSetName] != "" {
		return "", ErrGenerationRunning
	}
	if err := m.ctx.Err(); err != nil {
		return "", err
	}
	a.GeneratorOpts = &opts
	if refresh != nil {
		if err := configureRefresh(a, refresh, time.Now()); err != nil {
			return "", err
		}
	}
	if err := updateAlias(a); err != nil {
		return "", err
	}
	return m.generate(a)
}

// generate is called with mu held. Completion commits metadata before publishing job status.
func (m *Manager) generate(a *Alias) (string, error) {
	opts := a.GeneratorOpts
	m.workers.Add(1)
	job, err := m.ipsetMgr.RunGenerator(m.ctx, a.ID, a.IPSetName, ipset.GenerateOpts{
		Country: opts.Country, ASN: opts.ASN, ASNList: opts.ASNList,
	}, func(status ipset.JobStatus) error {
		defer m.workers.Done()
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.running, a.IPSetName)
		return m.finishGeneration(a.ID, status, time.Now())
	})
	if err != nil {
		m.workers.Done()
		return "", err
	}
	m.running[a.IPSetName] = job
	return job, nil
}

func (m *Manager) finishGeneration(id string, status ipset.JobStatus, now time.Time) error {
	a, err := m.getOrNotFound(id)
	if err != nil {
		return err
	}
	if status.Status == "done" {
		a.EntryCount = status.EntryCount
		a.LastUpdated = now.UTC().Format(time.RFC3339)
	}
	if r := a.AutoRefresh; r != nil && r.Enabled {
		r.LastError = status.Error
		delay := time.Duration(r.Days) * 24 * time.Hour
		if status.Status != "done" {
			delay = time.Hour
		}
		r.schedule(now, delay)
	}
	return updateAlias(a)
}

func (m *Manager) GetJobStatus(aliasID, jobID string) ipset.JobStatus {
	return m.ipsetMgr.GetJobStatus(jobID, aliasID)
}

func (m *Manager) StartAutoRefresh() {
	m.workers.Go(func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			m.refreshDue(time.Now())
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (m *Manager) StopAutoRefresh() {
	m.mu.Lock()
	m.cancel()
	m.mu.Unlock()
	m.workers.Wait()
}

func (m *Manager) refreshDue(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return
	}
	all, err := m.GetAll()
	if err != nil {
		log.Printf("aliases: scan auto-refresh schedules: %v", err)
		return
	}
	for i := range all {
		a := &all[i]
		r := a.AutoRefresh
		if a.Type != "ipset" || a.GeneratorOpts == nil || r == nil || !r.Enabled ||
			r.NextAt == nil || r.NextAt.After(now) || m.running[a.IPSetName] != "" {
			continue
		}
		if _, err := m.generate(a); err != nil {
			if saveErr := m.finishGeneration(a.ID, ipset.JobStatus{Status: "error", Error: err.Error()}, now); saveErr != nil {
				log.Printf("aliases: record auto-refresh failure: %v", fmt.Errorf("%s: %w", a.ID, saveErr))
			}
		}
	}
}
