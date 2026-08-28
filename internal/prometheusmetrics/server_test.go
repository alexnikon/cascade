package prometheusmetrics

import (
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestServerPortConflictKeepsOldListenerAndSnapshot(t *testing.T) {
	manager, err := NewManager(newSettingsDatabase(t), Config{ConnectedPeerThreshold: time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, NewCollector(nil, nil, nil, "test", "test", time.Minute))
	t.Cleanup(func() { _ = server.Shutdown() })
	oldPort := availablePort(t)
	if _, err := server.Apply(Update{Enabled: true, Port: oldPort, ConnectedPeerThresholdSeconds: 60, HistoryEnabled: true}); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port
	if _, err := server.Apply(Update{Enabled: true, Port: occupiedPort, ConnectedPeerThresholdSeconds: 60, HistoryEnabled: true}); err == nil {
		t.Fatal("expected port conflict")
	}
	if got := manager.Current().Port; got != oldPort {
		t.Fatalf("failed rebind changed snapshot port to %d", got)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get(metricURL(oldPort, Path))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("old listener status=%d", resp.StatusCode)
	}
}

func TestServerStartupBindFailureIsNonFatal(t *testing.T) {
	manager, err := NewManager(newSettingsDatabase(t), Config{ConnectedPeerThreshold: time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	if _, err := manager.Update(Update{Enabled: true, Port: port, ConnectedPeerThresholdSeconds: 60, HistoryEnabled: true}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, NewCollector(nil, nil, nil, "test", "test", time.Minute))
	server.Start()
	listening, listenError := server.Status()
	if listening || listenError == "" {
		t.Fatalf("unexpected startup status: listening=%t error=%q", listening, listenError)
	}
}

func TestServerShutdownClosesListener(t *testing.T) {
	manager, err := NewManager(newSettingsDatabase(t), Config{ConnectedPeerThreshold: time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(manager, NewCollector(nil, nil, nil, "test", "test", time.Minute))
	port := availablePort(t)
	if _, err := server.Apply(Update{Enabled: true, Port: port, ConnectedPeerThresholdSeconds: 60, HistoryEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := server.Shutdown(); err != nil {
		t.Fatal(err)
	}
	client := http.Client{Timeout: 100 * time.Millisecond}
	if resp, err := client.Get(metricURL(port, Path)); err == nil {
		resp.Body.Close()
		t.Fatal("listener still accepts connections after shutdown")
	}
}

func metricURL(port int, path string) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + path
}
