package prometheusmetrics

import (
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"
)

func newSettingsDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", t.TempDir()+"/settings.db")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestManagerBootstrapsOnceAndHashesToken(t *testing.T) {
	database := newSettingsDatabase(t)
	manager, err := NewManager(database, Config{Enabled: true, Token: "plain-secret", ConnectedPeerThreshold: 3 * time.Minute, HistoryEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Current(); !got.Enabled || got.Port != DefaultPort || got.ConnectedPeerThresholdSeconds != 180 || !got.TokenConfigured || got.HistoryEnabled {
		t.Fatalf("unexpected bootstrap snapshot: %+v", got)
	}
	var stored string
	if err := database.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingTokenHash).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "plain-secret" || strings.Contains(stored, "plain-secret") || len(stored) != 64 {
		t.Fatalf("token was not safely hashed: %q", stored)
	}
	if !manager.Authorize("plain-secret") || manager.Authorize("wrong") {
		t.Fatal("token authorization mismatch")
	}

	reloaded, err := NewManager(database, Config{Enabled: false, Token: "ignored", ConnectedPeerThreshold: time.Second, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Current(); got.Port != DefaultPort || !got.Enabled || got.HistoryEnabled {
		t.Fatalf("environment bootstrap unexpectedly overrode SQLite: %+v", got)
	}
}

func TestManagerUpdateTokenSemanticsAndValidation(t *testing.T) {
	manager, err := NewManager(newSettingsDatabase(t), Config{ConnectedPeerThreshold: 3 * time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	update := Update{Enabled: true, Port: 9352, ConnectedPeerThresholdSeconds: 90, HistoryEnabled: false, Token: "secret"}
	if _, err := manager.Update(update); err != nil {
		t.Fatal(err)
	}
	update.Token = ""
	if _, err := manager.Update(update); err != nil {
		t.Fatal(err)
	}
	if !manager.Authorize("secret") {
		t.Fatal("blank update removed the configured token")
	}
	update.ClearToken = true
	if got, err := manager.Update(update); err != nil || got.TokenConfigured || !manager.Authorize("") {
		t.Fatalf("clear token failed: snapshot=%+v err=%v", got, err)
	}
	for _, invalid := range []Update{
		{Port: 0, ConnectedPeerThresholdSeconds: 1},
		{Port: 65536, ConnectedPeerThresholdSeconds: 1},
		{Port: 9351, ConnectedPeerThresholdSeconds: 0},
	} {
		if _, err := manager.Update(invalid); err == nil {
			t.Fatalf("accepted invalid update: %+v", invalid)
		}
	}
}

func TestManagerConcurrentReadAndUpdate(t *testing.T) {
	manager, err := NewManager(newSettingsDatabase(t), Config{ConnectedPeerThreshold: time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = manager.Current()
				_ = manager.ConnectedPeerThreshold()
			}
		}()
	}
	for i := 1; i <= 10; i++ {
		if _, err := manager.Update(Update{Enabled: i%2 == 0, Port: 9351, ConnectedPeerThresholdSeconds: i, HistoryEnabled: i%2 != 0}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestManagerDoesNotPublishFailedDatabaseUpdate(t *testing.T) {
	database := newSettingsDatabase(t)
	manager, err := NewManager(database, Config{ConnectedPeerThreshold: time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	before := manager.Current()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(Update{Enabled: true, Port: 9352, ConnectedPeerThresholdSeconds: 90, HistoryEnabled: false}); err == nil {
		t.Fatal("expected persistence error")
	}
	if after := manager.Current(); after != before {
		t.Fatalf("failed update changed runtime snapshot: before=%+v after=%+v", before, after)
	}
}

func TestManagerMigratesLegacyPathToDefaultPort(t *testing.T) {
	database := newSettingsDatabase(t)
	manager, err := NewManager(database, Config{ConnectedPeerThreshold: time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM settings WHERE key = ?`, settingPort); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, legacySettingPath, "/legacy"); err != nil {
		t.Fatal(err)
	}

	manager, err = NewManager(database, Config{ConnectedPeerThreshold: time.Minute, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Current().Port; got != DefaultPort {
		t.Fatalf("port=%d, want %d", got, DefaultPort)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, legacySettingPath).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("legacy metrics path was not removed")
	}
}
