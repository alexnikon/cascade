// Package metrics collects system metrics (CPU, RAM, network interfaces)
// and persists them to SQLite for historical queries.
//
// Collection interval: every 5 seconds.
// Retention: 30 days (cleanup runs once per day).
//
// Keys written to metrics_history:
//
//	"cpu"            — CPU utilisation % (0–100)
//	"mem"            — RAM used % (0–100)
//	"net:<iface>:rx" — interface receive rate, Mbps
//	"net:<iface>:tx" — interface transmit rate, Mbps
package metrics

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexnikon/cascade/internal/db"
)

const (
	collectInterval = 5 * time.Second
	retentionDays   = 30
)

// Snapshot holds a single point-in-time reading of all metrics.
type Snapshot struct {
	CPU        float64 // percent 0–100
	MemUsedPct float64 // percent 0–100
	MemUsedMB  int64
	MemTotalMB int64
	Net        map[string]NetStat // key = interface name
	Interfaces []string           // sorted list of interface names
	Gateways   map[string]int     // key = gateway ID, value: 3=healthy 2=degraded 1=down 0=admin_down
	Processes  map[string]ProcessStat
}

// GatewayStatusFn is a callback that returns current gateway statuses.
// Registered from main.go after gateway manager is initialised to avoid import cycle.
type GatewayStatusFn func() map[string]int

var gatewayFnAtom atomic.Value // stores GatewayStatusFn
var historyEnabled atomic.Bool

func init() {
	historyEnabled.Store(parseHistoryEnabled(os.Getenv("METRICS_HISTORY_ENABLED")))
}

// RegisterGatewaySource sets the callback used to collect gateway statuses each tick.
// Safe to call concurrently with the collector goroutine.
func RegisterGatewaySource(fn GatewayStatusFn) { gatewayFnAtom.Store(fn) }

// HistoryEnabled reports whether periodic metrics are persisted to SQLite.
func HistoryEnabled() bool { return historyEnabled.Load() }

func parseHistoryEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// NetStat holds instantaneous RX/TX rates for one interface.
type NetStat struct {
	RxMbps float64
	TxMbps float64
}

// ProcessStat reports CPU and memory for the control and userspace data plane.
type ProcessStat struct {
	CPUPercent float64 `json:"cpuPercent"`
	RSSMB      float64 `json:"rssMb"`
	PIDs       int     `json:"pids"`
}

type processSample struct {
	CPU  uint64
	RSS  int64
	PIDs int
}

// collector is the singleton that owns all mutable state.
type collector struct {
	mu sync.Mutex

	// previous /proc/stat reading for CPU delta
	prevCPUIdle  uint64
	prevCPUTotal uint64

	// previous /proc/net/dev readings for network delta
	prevNet     map[string][2]uint64 // iface → [rxBytes, txBytes]
	prevNetTime time.Time
	prevProcess map[string]processSample

	// last computed snapshot (served by /api/metrics)
	last *Snapshot

	// cleanup: track last retention run
	lastCleanup time.Time
}

var (
	instance *collector
	once     sync.Once
)

// Start launches the background collection goroutine.
// Safe to call multiple times — only the first call has effect.
func Start(stop <-chan struct{}) {
	once.Do(func() {
		historyEnabled.Store(parseHistoryEnabled(os.Getenv("METRICS_HISTORY_ENABLED")))
		instance = &collector{
			prevNet:     make(map[string][2]uint64),
			prevProcess: make(map[string]processSample),
		}
		go instance.run(stop)
	})
}

// Current returns the most recently collected snapshot, or nil if not yet available.
func Current() *Snapshot {
	if instance == nil {
		return nil
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return instance.last
}

// History queries aggregated metrics from the DB.
// key: metric key (e.g. "cpu", "net:eth0:rx")
// from/to: unix timestamps
// stepSec: aggregation bucket size in seconds
func History(key string, from, to int64, stepSec int) ([][2]float64, error) {
	rows, err := db.MetricsDB().Query(`
		SELECT (ts / ?) * ? AS bucket, AVG(val)
		FROM metrics_history
		WHERE key = ? AND ts >= ? AND ts <= ?
		GROUP BY bucket
		ORDER BY bucket`,
		stepSec, stepSec, key, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out [][2]float64
	for rows.Next() {
		var bucket int64
		var avg float64
		if err := rows.Scan(&bucket, &avg); err != nil {
			continue
		}
		out = append(out, [2]float64{float64(bucket * 1000), avg}) // ms for JS
	}
	return out, rows.Err()
}

// GatewayDistribution returns per-bucket status counts for a gateway metric key.
// Each element: [ts_ms, healthy_count, degraded_count, down_count, admin_down_count]
// Uses MIN-friendly raw ticks so worst-case events are never hidden by averaging.
func GatewayDistribution(key string, from, to int64, stepSec int) ([][5]float64, error) {
	rows, err := db.MetricsDB().Query(`
		SELECT
			(ts / ?) * ? AS bucket,
			SUM(CASE WHEN ROUND(val) >= 3 THEN 1 ELSE 0 END),
			SUM(CASE WHEN ROUND(val) = 2  THEN 1 ELSE 0 END),
			SUM(CASE WHEN ROUND(val) = 1  THEN 1 ELSE 0 END),
			SUM(CASE WHEN ROUND(val) <= 0 THEN 1 ELSE 0 END)
		FROM metrics_history
		WHERE key = ? AND ts >= ? AND ts <= ?
		GROUP BY bucket
		ORDER BY bucket`,
		stepSec, stepSec, key, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out [][5]float64
	for rows.Next() {
		var bucket int64
		var healthy, degraded, down, adminDown float64
		if err := rows.Scan(&bucket, &healthy, &degraded, &down, &adminDown); err != nil {
			continue
		}
		out = append(out, [5]float64{float64(bucket * 1000), healthy, degraded, down, adminDown})
	}
	return out, rows.Err()
}

// AvailableKeys returns all distinct metric keys stored in the DB.
func AvailableKeys() ([]string, error) {
	rows, err := db.MetricsDB().Query(
		`SELECT DISTINCT key FROM metrics_history ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil {
			keys = append(keys, k)
		}
	}
	return keys, rows.Err()
}

// ── internal ──────────────────────────────────────────────────────────────────

func (c *collector) run(stop <-chan struct{}) {
	// First tick: initialise prev readings without writing (no delta yet).
	c.initReadings()

	ticker := time.NewTicker(collectInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			snap := c.collect()
			c.persist(snap)

			c.mu.Lock()
			c.last = snap
			c.mu.Unlock()

			c.maybeCleanup()
		}
	}
}

func (c *collector) initReadings() {
	idle, total, _ := readCPUStat()
	c.prevCPUIdle = idle
	c.prevCPUTotal = total

	net, _ := readNetDev()
	c.prevNet = net
	c.prevNetTime = time.Now()
	c.prevProcess, _ = readProcessSamples()
}

func (c *collector) collect() *Snapshot {
	snap := &Snapshot{
		Net: make(map[string]NetStat), Gateways: make(map[string]int),
		Processes: make(map[string]ProcessStat),
	}

	// CPU
	idle, total, err := readCPUStat()
	var deltaTotal uint64
	if err == nil {
		deltaIdle := idle - c.prevCPUIdle
		deltaTotal = total - c.prevCPUTotal
		if deltaTotal > 0 {
			snap.CPU = (1 - float64(deltaIdle)/float64(deltaTotal)) * 100
		}
		c.prevCPUIdle = idle
		c.prevCPUTotal = total
	}

	processes, err := readProcessSamples()
	if err == nil {
		for name, current := range processes {
			previous, hasPrevious := c.prevProcess[name]
			cpuPercent := processCPUPercent(current.CPU, previous.CPU, hasPrevious, deltaTotal, runtime.NumCPU())
			snap.Processes[name] = ProcessStat{
				CPUPercent: cpuPercent,
				RSSMB:      float64(current.RSS) / 1024 / 1024,
				PIDs:       current.PIDs,
			}
		}
		c.prevProcess = processes
	}

	// Memory
	snap.MemUsedMB, snap.MemTotalMB, snap.MemUsedPct, _ = readMemInfo()

	// Network
	now := time.Now()
	net, err := readNetDev()
	if err == nil {
		elapsed := now.Sub(c.prevNetTime).Seconds()
		if elapsed > 0 {
			for iface, cur := range net {
				prev := c.prevNet[iface]
				rxBytes := cur[0] - prev[0]
				txBytes := cur[1] - prev[1]
				snap.Net[iface] = NetStat{
					RxMbps: float64(rxBytes) * 8 / elapsed / 1e6,
					TxMbps: float64(txBytes) * 8 / elapsed / 1e6,
				}
				snap.Interfaces = append(snap.Interfaces, iface)
			}
		}
		c.prevNet = net
		c.prevNetTime = now
	}

	sort.Strings(snap.Interfaces)

	// Gateways — collected via registered callback to avoid import cycle
	if fn, ok := gatewayFnAtom.Load().(GatewayStatusFn); ok && fn != nil {
		snap.Gateways = fn()
	}

	return snap
}

func (c *collector) persist(snap *Snapshot) {
	if !HistoryEnabled() {
		return
	}
	ts := time.Now().Unix()
	database := db.MetricsDB()

	rows := []struct {
		key string
		val float64
	}{
		{"cpu", snap.CPU},
		{"mem", snap.MemUsedPct},
	}
	for iface, ns := range snap.Net {
		rows = append(rows,
			struct {
				key string
				val float64
			}{fmt.Sprintf("net:%s:rx", iface), ns.RxMbps},
			struct {
				key string
				val float64
			}{fmt.Sprintf("net:%s:tx", iface), ns.TxMbps},
		)
	}
	for id, status := range snap.Gateways {
		rows = append(rows, struct {
			key string
			val float64
		}{fmt.Sprintf("gateway:%s", id), float64(status)})
	}

	tx, err := database.Begin()
	if err != nil {
		log.Printf("metrics: begin tx: %v", err)
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO metrics_history(ts,key,val) VALUES(?,?,?)`)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		log.Printf("metrics: prepare: %v", err)
		return
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.Exec(ts, r.key, r.val); err != nil {
			log.Printf("metrics: insert %s: %v", r.key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("metrics: commit: %v", err)
	}
}

func (c *collector) maybeCleanup() {
	if !HistoryEnabled() {
		return
	}
	if time.Since(c.lastCleanup) < 24*time.Hour {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
	if _, err := db.MetricsDB().Exec(
		`DELETE FROM metrics_history WHERE ts < ?`, cutoff,
	); err != nil {
		log.Printf("metrics: cleanup: %v", err)
	}
	c.lastCleanup = time.Now()
}

// ── /proc readers ─────────────────────────────────────────────────────────────

// readCPUStat reads the first "cpu" line of /proc/stat and returns
// (idleJiffies, totalJiffies, error).
func readCPUStat() (uint64, uint64, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		// fields: cpu user nice system idle iowait irq softirq steal guest guest_nice
		if len(fields) < 5 {
			break
		}
		var total, idle uint64
		for i, f := range fields[1:] {
			v, _ := strconv.ParseUint(f, 10, 64)
			total += v
			if i == 3 { // idle
				idle = v
			}
		}
		return idle, total, nil
	}
	return 0, 0, fmt.Errorf("cpu line not found in /proc/stat")
}

// readMemInfo parses /proc/meminfo and returns (usedMB, totalMB, usedPct, error).
func readMemInfo() (int64, int64, float64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()

	vals := make(map[string]int64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		vals[key] = v
	}

	total := vals["MemTotal"]
	free := vals["MemFree"] + vals["Buffers"] + vals["Cached"] + vals["SReclaimable"] - vals["Shmem"]
	used := total - free
	if total == 0 {
		return 0, 0, 0, fmt.Errorf("MemTotal not found")
	}
	usedMB := used / 1024
	totalMB := total / 1024
	pct := float64(used) / float64(total) * 100
	return usedMB, totalMB, pct, nil
}

func readProcessSamples() (map[string]processSample, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	result := make(map[string]processSample)
	selfPID := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		comm, cpu, rss, err := parseProcessStat(string(content))
		if err != nil {
			continue
		}
		name := ""
		switch {
		case pid == selfPID:
			name = "cascade"
		case comm == "amneziawg-go":
			name = "amneziawg-go"
		default:
			continue
		}
		sample := result[name]
		sample.CPU += cpu
		sample.RSS += rss
		sample.PIDs++
		result[name] = sample
	}
	return result, nil
}

func processCPUPercent(current, previous uint64, hasPrevious bool, totalDelta uint64, cpuCount int) float64 {
	if !hasPrevious || totalDelta == 0 || current < previous || cpuCount <= 0 {
		return 0
	}
	return float64(current-previous) / float64(totalDelta) * float64(cpuCount) * 100
}

func parseProcessStat(content string) (string, uint64, int64, error) {
	open := strings.IndexByte(content, '(')
	close := strings.LastIndexByte(content, ')')
	if open < 0 || close <= open {
		return "", 0, 0, fmt.Errorf("invalid process stat")
	}
	comm := content[open+1 : close]
	fields := strings.Fields(content[close+1:])
	if len(fields) <= 21 {
		return "", 0, 0, fmt.Errorf("short process stat")
	}
	userTicks, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return "", 0, 0, err
	}
	systemTicks, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return "", 0, 0, err
	}
	rssPages, err := strconv.ParseInt(fields[21], 10, 64)
	if err != nil {
		return "", 0, 0, err
	}
	return comm, userTicks + systemTicks, rssPages * int64(os.Getpagesize()), nil
}

// readNetDev parses /proc/net/dev and returns a map of
// interfaceName → [rxBytes, txBytes].
// Loopback (lo) is excluded.
func readNetDev() (map[string][2]uint64, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string][2]uint64)
	scanner := bufio.NewScanner(f)
	// Skip two header lines.
	scanner.Scan()
	scanner.Scan()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colonIdx])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(line[colonIdx+1:])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		result[iface] = [2]uint64{rx, tx}
	}
	return result, scanner.Err()
}
