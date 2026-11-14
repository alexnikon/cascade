package sessions

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/db"
)

func setup(t *testing.T) time.Time {
	t.Helper()
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err := db.DB().Exec(`INSERT INTO users(id,username,password_hash,is_admin) VALUES('owner','owner','hash',1),('other','other','hash',0)`); err != nil {
		t.Fatal(err)
	}
	return time.Now().UTC().Truncate(time.Second)
}

func mustCreate(t *testing.T, id string, now time.Time) (*Session, string) {
	t.Helper()
	s, raw, err := Create(id, 0, "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Version/18.0 Mobile/15 Safari/604.1", "10.8.0.2", "", now)
	if err != nil {
		t.Fatal(err)
	}
	return s, raw
}

func TestDurableSessionsRenewalAndExpiry(t *testing.T) {
	now := setup(t)
	s, raw := mustCreate(t, "owner", now)
	var stored string
	if err := db.DB().QueryRow(`SELECT token_hash FROM browser_sessions WHERE id=?`, s.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == raw || stored != Hash(raw) || len(raw) != 64 {
		t.Fatal("raw browser credential stored or insufficient entropy")
	}
	if !s.ExpiresAt.Equal(now.Add(Lifetime)) || s.Device != "iPhone" || s.Browser != "Safari" {
		t.Fatalf("unexpected session: %+v", s)
	}
	if renewed, err := Touch(s, "10.8.0.3", now.Add(30*time.Second)); err != nil || renewed {
		t.Fatalf("early touch %v %v", renewed, err)
	}
	loaded, _ := Lookup(raw, now)
	if loaded.LastIP != "10.8.0.2" {
		t.Fatal("activity written too frequently")
	}
	if renewed, err := Touch(s, "10.8.0.3", now.Add(time.Minute)); err != nil || renewed {
		t.Fatalf("activity touch %v %v", renewed, err)
	}
	loaded, _ = Lookup(raw, now)
	if loaded.LastIP != "10.8.0.3" {
		t.Fatal("activity not saved")
	}
	if renewed, err := Touch(loaded, "10.8.0.4", now.Add(24*time.Hour)); err != nil || !renewed {
		t.Fatalf("renewal %v %v", renewed, err)
	}
	again, err := Lookup(raw, now.Add(24*time.Hour))
	if err != nil || !again.ExpiresAt.Equal(now.Add(24*time.Hour+Lifetime)) {
		t.Fatalf("expiry not extended: %+v %v", again, err)
	}
	if renewed, err := Touch(again, "10.8.0.5", now.Add(24*time.Hour+time.Minute)); err != nil || renewed {
		t.Fatalf("repeated renewal %v %v", renewed, err)
	}
	if _, err := Lookup(raw, again.ExpiresAt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired session accepted: %v", err)
	}
}

func TestListOwnershipCurrentAndNoCredentials(t *testing.T) {
	now := setup(t)
	first, raw := mustCreate(t, "owner", now)
	second, _ := mustCreate(t, "owner", now.Add(time.Second))
	mustCreate(t, "other", now.Add(2*time.Second))
	list, err := List("owner", false, first.ID, now)
	if err != nil || len(list) != 2 || list[0].ID != first.ID || !list[0].Current || list[1].ID != second.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	all, err := List("owner", true, "", now)
	if err != nil || len(all) != 3 {
		t.Fatalf("admin list %+v %v", all, err)
	}
	encoded, _ := json.Marshal(all)
	for _, secret := range []string{raw, Hash(raw), "token_hash", "user_agent", "auth_version", "renewed_at"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("exposed %s", secret)
		}
	}
	if all[0].Current {
		t.Fatal("Bearer request claimed a current browser")
	}
	if ok, err := Revoke(first.ID, "other", false, now); err != nil || ok {
		t.Fatalf("cross-user revoke: %v %v", ok, err)
	}
	if ok, err := Revoke(first.ID, "other", true, now); err != nil || !ok {
		t.Fatalf("admin revoke: %v %v", ok, err)
	}
	if _, err := Lookup(raw, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("revocation not enforced")
	}
}

func TestChallengesAreShortLivedSingleUseAndCredentialBound(t *testing.T) {
	now := setup(t)
	pending, err := NewChallenge("owner", 0, "login", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetChallenge(pending, "login", "", now.Add(ChallengeLifetime)); !errors.Is(err, ErrInvalid) {
		t.Fatal("challenge did not expire")
	}
	ch, err := GetChallenge(pending, "login", "", now)
	if err != nil || ch.UserID != "owner" {
		t.Fatalf("challenge %+v %v", ch, err)
	}
	if _, _, err := Create("other", 0, "agent", "ip", pending, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("challenge switched users")
	}
	if _, _, err := Create("owner", 0, "agent", "ip", pending, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create("owner", 0, "agent", "ip", pending, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("challenge replay accepted")
	}
	setupToken, err := NewChallenge("owner", 0, "setup", "session:first", "secret", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetChallenge(setupToken, "setup", "session:second", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("setup context not enforced")
	}
	if _, err := GetChallenge(setupToken, "login", "session:first", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("setup became login")
	}
	if _, err := db.DB().Exec(`UPDATE users SET auth_version=1 WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create("owner", 0, "agent", "ip", "", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("stale verified password admitted a new session")
	}
	if _, err := GetChallenge(setupToken, "setup", "session:first", now); !errors.Is(err, ErrInvalid) {
		t.Fatal("stale challenge accepted")
	}
}

func TestRevokeRacingActivityCannotResurrectSession(t *testing.T) {
	now := setup(t)
	s, raw := mustCreate(t, "owner", now)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			copy := *s
			if _, err := Touch(&copy, "ip", now.Add(48*time.Hour)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if _, err := Revoke(s.ID, "owner", false, now); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	wg.Wait()
	if _, err := Lookup(raw, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("session resurrected: %v", err)
	}
}

func TestCleanupDeletionAndRestore(t *testing.T) {
	now := setup(t)
	_, raw := mustCreate(t, "owner", now)
	NewChallenge("owner", 0, "setup", "context", "secret", now)
	if err := Cleanup(now.Add(Lifetime), true); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, table := range []string{"browser_sessions", "auth_challenges"} {
		if err := db.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("expired %s: %d %v", table, count, err)
		}
	}
	_, raw = mustCreate(t, "owner", now)
	if _, err := db.DB().Exec(`DELETE FROM users WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(raw, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("deleted user remained authenticated")
	}
	mustCreate(t, "other", now)
	if err := ClearRestored(db.DB()); err != nil {
		t.Fatal(err)
	}
	list, err := List("other", true, "", now)
	if err != nil || len(list) != 0 {
		t.Fatal("restore retained sessions")
	}
	legacy, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if err := ClearRestored(legacy); err != nil {
		t.Fatalf("legacy backup: %v", err)
	}
}

func TestAgentDescriptions(t *testing.T) {
	for _, tc := range []struct{ agent, device, browser, os string }{
		{"", "Unknown device", "Unknown browser", "Unknown OS"},
		{"Mozilla/5.0 (Windows NT 10.0) Chrome/130 Safari/537 Edg/130", "Computer", "Edge", "Windows"},
		{"Mozilla/5.0 (Macintosh) AppleWebKit/605 Version/18 Safari/605", "Mac", "Safari", "macOS"},
		{"Mozilla/5.0 (Macintosh) Version/18 Mobile/15 Safari/605", "iPad", "Safari", "iPadOS"},
		{"Mozilla/5.0 (Android) Chrome/130 Mobile Safari/537", "Android device", "Chrome", "Android"},
		{"Mozilla/5.0 (iPhone) FxiOS/130 Mobile/15 Safari/605", "iPhone", "Firefox", "iOS"},
	} {
		d, b, o := describeAgent(tc.agent)
		if d != tc.device || b != tc.browser || o != tc.os {
			t.Errorf("%s: %s / %s / %s", tc.agent, d, b, o)
		}
	}
}
