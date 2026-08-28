// Package prometheusmetrics exposes Cascade's shared runtime state in the
// Prometheus text format.
package prometheusmetrics

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultConnectedPeerThreshold = 180 * time.Second

// Config controls the native Prometheus endpoint.
type Config struct {
	Enabled                bool
	Path                   string
	Token                  string
	ConnectedPeerThreshold time.Duration
	HistoryEnabled         bool
}

// ConfigFromEnv follows Cascade's existing environment-based configuration.
func ConfigFromEnv() (Config, error) {
	enabled, err := parseBool("METRICS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	path := strings.TrimSpace(os.Getenv("METRICS_PATH"))
	if path == "" {
		path = "/metrics"
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") || path == "/" {
		return Config{}, fmt.Errorf("METRICS_PATH must be an absolute path without query or fragment")
	}
	threshold := defaultConnectedPeerThreshold
	if raw := strings.TrimSpace(os.Getenv("METRICS_CONNECTED_PEER_THRESHOLD")); raw != "" {
		threshold, err = time.ParseDuration(raw)
		if err != nil || threshold <= 0 {
			return Config{}, fmt.Errorf("METRICS_CONNECTED_PEER_THRESHOLD must be a positive duration")
		}
	}
	return Config{
		Enabled: enabled, Path: path, Token: os.Getenv("METRICS_TOKEN"),
		ConnectedPeerThreshold: threshold,
		HistoryEnabled:         parseHistoryEnabled(os.Getenv("METRICS_HISTORY_ENABLED")),
	}, nil
}

func parseHistoryEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func parseBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return value, nil
}
