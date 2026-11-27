package aliases

import (
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/ipset"
)

func TestAutoRefreshSchedule(t *testing.T) {
	m := initTestDB(t)
	a := seedIPSetAlias(t, "scheduled")
	if a.AutoRefresh != nil {
		t.Fatal("expected auto-refresh off by default")
	}
	if _, err := m.Update(a.ID, Alias{AutoRefresh: &AutoRefresh{Enabled: true, Days: 7}}); err == nil {
		t.Fatal("accepted a schedule without a generator source")
	}
	a.GeneratorOpts = &GeneratorOpts{Country: "invalid"}
	now := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	for _, days := range []int{1, 3, 7, 30} {
		if err := configureRefresh(a, &AutoRefresh{Enabled: true, Days: days}, now); err != nil {
			t.Fatal(err)
		}
		if got := a.AutoRefresh.NextAt.Sub(now); got != time.Duration(days)*24*time.Hour {
			t.Fatalf("period %d: deadline offset was %s", days, got)
		}
	}
	if err := configureRefresh(a, &AutoRefresh{Enabled: true, Days: 2}, now); err == nil {
		t.Fatal("accepted unsupported interval")
	}
	if err := updateAlias(a); err != nil {
		t.Fatal(err)
	}
	if err := m.finishGeneration(a.ID, ipset.JobStatus{Status: "done", EntryCount: 0}, now); err != nil {
		t.Fatal(err)
	}
	saved, err := m.GetByID(a.ID)
	if err != nil || saved.LastUpdated != now.Format(time.RFC3339) {
		t.Fatalf("same-count success did not update timestamp: %+v, %v", saved, err)
	}
	// A new manager has no in-memory jobs. The persisted deadline drives the scan.
	restarted := New(m.ipsetMgr)
	defer restarted.StopAutoRefresh()
	restarted.refreshDue(now)
	if len(restarted.running) != 0 {
		t.Fatal("started a job before the deadline")
	}
	restarted.refreshDue(now.Add(30 * 24 * time.Hour))
	restarted.workers.Wait()
	saved, err = restarted.GetByID(a.ID)
	if err != nil || saved.AutoRefresh.LastError == "" || saved.LastUpdated != now.Format(time.RFC3339) {
		t.Fatalf("failed refresh did not preserve last success: %+v, %v", saved, err)
	}
	if remaining := time.Until(*saved.AutoRefresh.NextAt); remaining > time.Hour || remaining < 59*time.Minute {
		t.Fatalf("unexpected retry delay: %s", remaining)
	}
	disabled, err := restarted.Update(a.ID, Alias{AutoRefresh: &AutoRefresh{Days: 30}})
	if err != nil || disabled.AutoRefresh.Enabled || disabled.AutoRefresh.NextAt != nil {
		t.Fatalf("failed to disable schedule: %+v, %v", disabled, err)
	}
}
