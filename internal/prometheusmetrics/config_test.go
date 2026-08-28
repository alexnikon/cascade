package prometheusmetrics

import (
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_TOKEN", "secret")
	t.Setenv("METRICS_CONNECTED_PEER_THRESHOLD", "4m")
	t.Setenv("METRICS_HISTORY_ENABLED", "false")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || config.Token != "secret" || config.ConnectedPeerThreshold != 4*time.Minute || config.HistoryEnabled {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestConfigFromEnvRejectsInvalidValues(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "yes")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected invalid boolean error")
	}
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_CONNECTED_PEER_THRESHOLD", "0s")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected invalid threshold error")
	}
}
