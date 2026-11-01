package ipset

import (
	"testing"
	"time"
)

func TestCompletedJobRetention(t *testing.T) {
	m := &Manager{}
	now := time.Now()
	for i := 0; i < 10000; i++ {
		m.jobs.Store(i, JobStatus{Status: "done", CompletedAt: now.Add(-time.Hour)})
	}
	m.jobs.Store("running", JobStatus{Status: "running"})
	m.jobs.Store("recent", JobStatus{Status: "error", CompletedAt: now})
	m.pruneCompletedJobs(now)
	count := 0
	m.jobs.Range(func(_, _ any) bool { count++; return true })
	if count != 2 {
		t.Fatalf("retained %d jobs, want 2", count)
	}
}

func TestJobWorkerShutdown(t *testing.T) {
	m := &Manager{stop: make(chan struct{}), done: make(chan struct{})}
	m.StartJobRetention()
	m.Close()
	m.Close()
}
