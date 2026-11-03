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
const DefaultPort = 9351

// Config controls the native Prometheus endpoint.
type Config struct {
	Enabled                bool // Deprecated: endpoints require an explicit access-mode selection.
	Token                  string
	ConnectedPeerThreshold time.Duration // Deprecated: fixed at 180 seconds.
	HistoryEnabled         bool
}

// ConfigFromEnv follows Cascade's existing environment-based configuration.
func ConfigFromEnv() (Config, error) {
	enabled, err := parseBool("METRICS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Enabled: enabled, Token: os.Getenv("METRICS_TOKEN"),
		ConnectedPeerThreshold: defaultConnectedPeerThreshold,
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
