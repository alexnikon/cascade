package metrics

import (
	"os"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/db"
)

func TestParseHistoryEnabled(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "", want: true},
		{value: "true", want: true},
		{value: "1", want: true},
		{value: "false", want: false},
		{value: "0", want: false},
		{value: "OFF", want: false},
	} {
		if got := parseHistoryEnabled(test.value); got != test.want {
			t.Fatalf("parseHistoryEnabled(%q) = %t, want %t", test.value, got, test.want)
		}
	}
}

func TestParseProcessStat(t *testing.T) {
	content := "123 (amneziawg-go worker) S 1 2 3 4 5 6 7 8 9 10 120 30 14 15 16 17 18 19 20 21 7"
	name, cpu, rss, err := parseProcessStat(content)
	if err != nil {
		t.Fatalf("parseProcessStat: %v", err)
	}
	if name != "amneziawg-go worker" {
		t.Fatalf("name = %q", name)
	}
	if cpu != 150 {
		t.Fatalf("cpu ticks = %d, want 150", cpu)
	}
	if want := int64(7 * os.Getpagesize()); rss != want {
		t.Fatalf("rss = %d, want %d", rss, want)
	}
}

func TestProcessCPUPercentRequiresPreviousSample(t *testing.T) {
	if got := processCPUPercent(5000, 0, false, 100, 4); got != 0 {
		t.Fatalf("first sample CPU = %f, want 0", got)
	}
	if got := processCPUPercent(120, 100, true, 80, 4); got != 100 {
		t.Fatalf("delta CPU = %f, want 100", got)
	}
}

func TestCounterDelta(t *testing.T) {
	for _, tt := range []struct {
		current, previous uint64
		exists            bool
		want              uint64
	}{
		{100, 0, false, 0}, {10, 100, true, 0}, {120, 100, true, 20},
	} {
		if got := counterDelta(tt.current, tt.previous, tt.exists); got != tt.want {
			t.Fatalf("delta = %d, want %d", got, tt.want)
		}
	}
}

func TestClearHistoryWhileCollectorIsWriting(t *testing.T) {
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	previous := HistoryEnabled()
	t.Cleanup(func() { SetHistoryEnabled(previous) })
	SetHistoryEnabled(true)
	c := &collector{}
	c.persist(&Snapshot{CPU: 7})

	database := db.MetricsDB()
	// Hold the only SQLite connection so the collector queues a write.
	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	written := make(chan struct{})
	go func() {
		defer close(written)
		c.persist(&Snapshot{CPU: 8})
	}()
	deadline := time.Now().Add(time.Second)
	for database.Stats().WaitCount == 0 {
		if time.Now().After(deadline) {
			t.Fatal("collector did not queue a write")
		}
		time.Sleep(time.Millisecond)
	}
	disabled := make(chan struct{})
	go func() {
		SetHistoryEnabled(false)
		close(disabled)
	}()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	<-disabled
	if err := ClearHistory(); err != nil {
		t.Fatal(err)
	}
	<-written
	c.persist(&Snapshot{CPU: 9})

	SetHistoryEnabled(true)
	c.persist(&Snapshot{CPU: 10})
	points, err := History("cpu", 0, time.Now().Unix(), 1)
	if err != nil || len(points) != 1 || points[0][1] != 10 {
		t.Fatalf("expected only the new sample after re-enabling: points=%v err=%v", points, err)
	}
}

func TestRetentionCutoffs(t *testing.T) {
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	t.Cleanup(func() { SetHistoryPolicy(true, DefaultRetention) })
	database := db.MetricsDB()
	now := time.Now().Truncate(time.Second)
	for _, test := range []struct {
		retention Retention
		age       time.Duration
	}{
		{"1h", time.Hour},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"forever", 365 * 24 * time.Hour},
	} {
		t.Run(string(test.retention), func(t *testing.T) {
			if _, err := database.Exec(`DELETE FROM metrics_history`); err != nil {
				t.Fatal(err)
			}
			cutoff := now.Add(-test.age).Unix()
			if _, err := database.Exec(`INSERT INTO metrics_history VALUES (?, 'older', 1), (?, 'boundary', 1)`, cutoff-1, cutoff); err != nil {
				t.Fatal(err)
			}
			want := 1
			if test.retention == "forever" {
				SetHistoryPolicy(true, test.retention)
				(&collector{}).maybeCleanup()
				want = 2
			} else if err := deleteExpired(database, now, test.retention); err != nil {
				t.Fatal(err)
			}
			var count int
			var first string
			if err := database.QueryRow(`SELECT count(*), min(key) FROM metrics_history`).Scan(&count, &first); err != nil || count != want || first != "boundary" {
				t.Fatalf("remaining rows=%d first=%q err=%v", count, first, err)
			}
		})
	}
}
