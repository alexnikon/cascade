// cascade-bench runs a bounded HTTP load test against a Cascade API route.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	StartedAt      string  `json:"startedAt"`
	DurationSec    float64 `json:"durationSec"`
	Concurrency    int     `json:"concurrency"`
	Requests       uint64  `json:"requests"`
	Errors         uint64  `json:"errors"`
	ResponseBytes  uint64  `json:"responseBytes"`
	RequestsPerSec float64 `json:"requestsPerSec"`
	LatencyMs      summary `json:"latencyMs"`
}

type summary struct {
	Min float64 `json:"min"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
	Avg float64 `json:"avg"`
}

func main() {
	baseURL := flag.String("base-url", "", "full API base URL, for example https://host/admin/api")
	path := flag.String("path", "/peers", "API path relative to base-url")
	token := flag.String("token", os.Getenv("CASCADE_API_TOKEN"), "Bearer token (or set CASCADE_API_TOKEN)")
	duration := flag.Duration("duration", 30*time.Second, "test duration")
	concurrency := flag.Int("concurrency", 4, "number of concurrent workers")
	timeout := flag.Duration("timeout", 5*time.Second, "per-request timeout")
	flag.Parse()

	if *baseURL == "" || *token == "" || *duration <= 0 || *concurrency <= 0 || *timeout <= 0 {
		flag.Usage()
		os.Exit(2)
	}

	endpoint := strings.TrimRight(*baseURL, "/") + "/" + strings.TrimLeft(*path, "/")
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	client := &http.Client{Timeout: *timeout}
	started := time.Now()
	var requests atomic.Uint64
	var failures atomic.Uint64
	var responseBytes atomic.Uint64
	latencies := make([]time.Duration, 0, 4096)
	var latencyMu sync.Mutex
	var workers sync.WaitGroup

	for i := 0; i < *concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				requestStarted := time.Now()
				size, err := makeRequest(ctx, client, endpoint, *token)
				elapsed := time.Since(requestStarted)
				if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
					return
				}
				requests.Add(1)
				if err != nil {
					failures.Add(1)
				} else {
					responseBytes.Add(uint64(size))
				}
				latencyMu.Lock()
				latencies = append(latencies, elapsed)
				latencyMu.Unlock()
			}
		}()
	}
	workers.Wait()
	elapsed := time.Since(started)
	count := requests.Load()
	report := result{
		StartedAt: started.UTC().Format(time.RFC3339), DurationSec: elapsed.Seconds(),
		Concurrency: *concurrency, Requests: count, Errors: failures.Load(),
		ResponseBytes: responseBytes.Load(), LatencyMs: summarize(latencies),
	}
	if elapsed > 0 {
		report.RequestsPerSec = float64(count) / elapsed.Seconds()
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func makeRequest(ctx context.Context, client *http.Client, endpoint, token string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	size, readErr := io.Copy(io.Discard, resp.Body)
	if readErr != nil {
		return int(size), readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return int(size), fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return int(size), nil
}

func summarize(values []time.Duration) summary {
	if len(values) == 0 {
		return summary{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var total time.Duration
	for _, value := range values {
		total += value
	}
	ms := func(value time.Duration) float64 { return float64(value) / float64(time.Millisecond) }
	percentile := func(p float64) float64 {
		index := int(float64(len(values)-1) * p)
		return ms(values[index])
	}
	return summary{
		Min: ms(values[0]), P50: percentile(0.50), P95: percentile(0.95),
		P99: percentile(0.99), Max: ms(values[len(values)-1]),
		Avg: ms(total) / float64(len(values)),
	}
}
