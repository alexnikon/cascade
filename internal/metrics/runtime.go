package metrics

import (
	"sync"
	"time"
)

var latencyBounds = [...]time.Duration{
	10 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
}

// HTTPRouteStat contains bounded in-memory request metrics for one route.
type HTTPRouteStat struct {
	Count           uint64            `json:"count"`
	Errors          uint64            `json:"errors"`
	DurationTotalMs float64           `json:"durationTotalMs"`
	DurationMaxMs   float64           `json:"durationMaxMs"`
	ResponseBytes   uint64            `json:"responseBytes"`
	LatencyBuckets  map[string]uint64 `json:"latencyBuckets"`
}

// StatusCommandStat describes runtime status collection for one interface.
type StatusCommandStat struct {
	Count          uint64  `json:"count"`
	Errors         uint64  `json:"errors"`
	LastSuccess    bool    `json:"lastSuccess"`
	DurationLastMs float64 `json:"durationLastMs"`
	DurationMaxMs  float64 `json:"durationMaxMs"`
}

// RuntimeSnapshot contains low-cardinality process-local performance metrics.
type RuntimeSnapshot struct {
	HTTP                 map[string]HTTPRouteStat     `json:"http"`
	StatusCommands       map[string]StatusCommandStat `json:"statusCommands"`
	StatusSnapshotAgeMs  int64                        `json:"statusSnapshotAgeMs"`
	StatusInterfaceCount int                          `json:"statusInterfaceCount"`
	StatusPeerCount      int                          `json:"statusPeerCount"`
	ReconcileErrors      uint64                       `json:"reconcileErrors"`
}

var runtimeState = struct {
	sync.Mutex
	http                 map[string]*HTTPRouteStat
	statusCommands       map[string]*StatusCommandStat
	statusSnapshotAt     time.Time
	statusInterfaceCount int
	statusPeerCount      int
	reconcileErrors      uint64
}{
	http:           make(map[string]*HTTPRouteStat),
	statusCommands: make(map[string]*StatusCommandStat),
}

// RecordHTTPRequest records one completed HTTP request using its route template.
func RecordHTTPRequest(method, route string, status int, duration time.Duration, responseBytes int) {
	key := method + " " + route
	runtimeState.Lock()
	defer runtimeState.Unlock()
	stat := runtimeState.http[key]
	if stat == nil {
		stat = &HTTPRouteStat{LatencyBuckets: make(map[string]uint64)}
		runtimeState.http[key] = stat
	}
	stat.Count++
	if status >= 400 {
		stat.Errors++
	}
	durationMs := float64(duration) / float64(time.Millisecond)
	stat.DurationTotalMs += durationMs
	if durationMs > stat.DurationMaxMs {
		stat.DurationMaxMs = durationMs
	}
	if responseBytes > 0 {
		stat.ResponseBytes += uint64(responseBytes)
	}
	stat.LatencyBuckets[latencyBucket(duration)]++
}

// RecordStatusCommand records one wg/awg status command for an interface.
func RecordStatusCommand(interfaceID string, duration time.Duration, success bool) {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	stat := runtimeState.statusCommands[interfaceID]
	if stat == nil {
		stat = &StatusCommandStat{}
		runtimeState.statusCommands[interfaceID] = stat
	}
	stat.Count++
	stat.LastSuccess = success
	if !success {
		stat.Errors++
		runtimeState.reconcileErrors++
	}
	durationMs := float64(duration) / float64(time.Millisecond)
	stat.DurationLastMs = durationMs
	if durationMs > stat.DurationMaxMs {
		stat.DurationMaxMs = durationMs
	}
}

// RecordStatusSnapshot marks a complete status polling pass.
func RecordStatusSnapshot(interfaceCount, peerCount int) {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	runtimeState.statusSnapshotAt = time.Now()
	runtimeState.statusInterfaceCount = interfaceCount
	runtimeState.statusPeerCount = peerCount
}

// CurrentRuntime returns an immutable copy of the current runtime metrics.
func CurrentRuntime() RuntimeSnapshot {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	httpStats := make(map[string]HTTPRouteStat, len(runtimeState.http))
	for key, value := range runtimeState.http {
		copyValue := *value
		copyValue.LatencyBuckets = make(map[string]uint64, len(value.LatencyBuckets))
		for bucket, count := range value.LatencyBuckets {
			copyValue.LatencyBuckets[bucket] = count
		}
		httpStats[key] = copyValue
	}
	statusStats := make(map[string]StatusCommandStat, len(runtimeState.statusCommands))
	for key, value := range runtimeState.statusCommands {
		statusStats[key] = *value
	}
	age := int64(-1)
	if !runtimeState.statusSnapshotAt.IsZero() {
		age = time.Since(runtimeState.statusSnapshotAt).Milliseconds()
	}
	return RuntimeSnapshot{
		HTTP:                 httpStats,
		StatusCommands:       statusStats,
		StatusSnapshotAgeMs:  age,
		StatusInterfaceCount: runtimeState.statusInterfaceCount,
		StatusPeerCount:      runtimeState.statusPeerCount,
		ReconcileErrors:      runtimeState.reconcileErrors,
	}
}

func latencyBucket(duration time.Duration) string {
	for _, bound := range latencyBounds {
		if duration <= bound {
			return "le_" + bound.String()
		}
	}
	return "gt_" + latencyBounds[len(latencyBounds)-1].String()
}
