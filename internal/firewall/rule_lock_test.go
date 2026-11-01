package firewall

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestRuleApplySerializationAndRelease(t *testing.T) {
	m := &Manager{}
	var active atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.withRuleApply("rule", func() error {
				if active.Add(1) != 1 {
					t.Error("overlapping rule applications")
				}
				active.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()
	if len(m.ruleLocks) != 0 {
		t.Fatal("retained idle rule locks")
	}
}

func TestRuleApplyIndependentRules(t *testing.T) {
	m := &Manager{}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.withRuleApply("first", func() error { close(entered); <-release; return nil })
	}()
	<-entered
	if err := m.withRuleApply("second", func() error { close(release); return nil }); err != nil {
		t.Fatal(err)
	}
	<-done
	if len(m.ruleLocks) != 0 {
		t.Fatal("retained idle rule locks")
	}
}
