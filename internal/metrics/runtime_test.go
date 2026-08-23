package metrics

import (
	"testing"
	"time"
)

func TestRuntimeMetricsRecordBoundedStats(t *testing.T) {
	RecordHTTPRequest("GET", "/api/test/:id", 200, 40*time.Millisecond, 128)
	RecordHTTPRequest("GET", "/api/test/:id", 500, 600*time.Millisecond, 64)
	RecordStatusCommand("wg10", 25*time.Millisecond, true)
	RecordStatusCommand("wg10", 75*time.Millisecond, false)
	RecordStatusSnapshot(1, 12)

	snapshot := CurrentRuntime()
	httpStat := snapshot.HTTP["GET /api/test/:id"]
	if httpStat.Count != 2 || httpStat.Errors != 1 {
		t.Fatalf("unexpected HTTP counters: %+v", httpStat)
	}
	if httpStat.ResponseBytes != 192 {
		t.Fatalf("response bytes = %d, want 192", httpStat.ResponseBytes)
	}
	if httpStat.LatencyBuckets["le_50ms"] != 1 || httpStat.LatencyBuckets["le_1s"] != 1 {
		t.Fatalf("unexpected latency buckets: %+v", httpStat.LatencyBuckets)
	}
	statusStat := snapshot.StatusCommands["wg10"]
	if statusStat.Count != 2 || statusStat.Errors != 1 {
		t.Fatalf("unexpected status counters: %+v", statusStat)
	}
	if snapshot.StatusInterfaceCount != 1 || snapshot.StatusPeerCount != 12 {
		t.Fatalf("unexpected status snapshot: %+v", snapshot)
	}
	if snapshot.ReconcileErrors != 1 {
		t.Fatalf("reconcile errors = %d, want 1", snapshot.ReconcileErrors)
	}
	if snapshot.StatusSnapshotAgeMs < 0 {
		t.Fatalf("status snapshot age = %d, want non-negative", snapshot.StatusSnapshotAgeMs)
	}
}
