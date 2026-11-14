// Package sessions stores revocable browser sessions and short-lived authentication
// challenges. Only hashes of browser credentials are persisted.
package sessions

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/google/uuid"
)

const Lifetime = 400 * 24 * time.Hour
const ChallengeLifetime = 10 * time.Minute

var ErrInvalid = errors.New("session or challenge is no longer valid")
var cleanupAt atomic.Int64

type Session struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Username   string    `json:"username"`
	Device     string    `json:"device"`
	Browser    string    `json:"browser"`
	OS         string    `json:"os"`
	LoginIP    string    `json:"login_ip"`
	LastIP     string    `json:"last_ip"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
	RenewedAt  time.Time `json:"-"`
}

type Challenge struct {
	UserID      string
	AuthVersion int64
	Secret      string
}

func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Cleanup runs at startup and at most hourly on incoming authentication traffic.
// No background goroutine outlives the database that owns its records.
func Cleanup(now time.Time, force bool) error {
	previous := cleanupAt.Load()
	if !force && now.Unix()-previous < 3600 {
		return nil
	}
	if !force && !cleanupAt.CompareAndSwap(previous, now.Unix()) {
		return nil
	}
	_, err := db.DB().Exec(`DELETE FROM browser_sessions WHERE expires_at<=?;
DELETE FROM auth_challenges WHERE expires_at<=?`, now.Unix(), now.Unix())
	if err == nil {
		cleanupAt.Store(now.Unix())
	} else {
		cleanupAt.Store(0)
	}
	return err
}

// Create optionally consumes a login challenge in the same transaction. The
// credential version prevents a concurrent password/2FA change from admitting
// a login that was verified against the previous credentials.
func Create(userID string, version int64, agent, ip, loginChallenge string, now time.Time) (*Session, string, error) {
	token, err := newToken()
	if err != nil {
		return nil, "", err
	}
	tx, err := db.DB().Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if loginChallenge != "" {
		result, err := tx.Exec(`DELETE FROM auth_challenges WHERE token_hash=? AND purpose='login' AND user_id=? AND auth_version=? AND expires_at>?`, Hash(loginChallenge), userID, version, now.Unix())
		if err != nil {
			return nil, "", err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return nil, "", ErrInvalid
		}
	}
	id := uuid.NewString()
	result, err := tx.Exec(`INSERT INTO browser_sessions(id,token_hash,user_id,user_agent,login_ip,last_ip,created_at,last_seen_at,renewed_at,expires_at)
SELECT ?,?,id,?,?,?,?,?,?,? FROM users WHERE id=? AND auth_version=?`, id, Hash(token), agent, ip, ip, now.Unix(), now.Unix(), now.Unix(), now.Add(Lifetime).Unix(), userID, version)
	if err != nil {
		return nil, "", err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, "", ErrInvalid
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	s, err := Lookup(token, now)
	return s, token, err
}

func scan(row interface{ Scan(...any) error }) (*Session, error) {
	var s Session
	var agent string
	var created, seen, renewed, expires int64
	err := row.Scan(&s.ID, &s.UserID, &s.Username, &agent, &s.LoginIP, &s.LastIP, &created, &seen, &renewed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	s.CreatedAt, s.LastSeenAt = time.Unix(created, 0).UTC(), time.Unix(seen, 0).UTC()
	s.RenewedAt, s.ExpiresAt = time.Unix(renewed, 0).UTC(), time.Unix(expires, 0).UTC()
	s.Device, s.Browser, s.OS = describeAgent(agent)
	return &s, nil
}

const columns = `s.id,s.user_id,u.username,s.user_agent,s.login_ip,s.last_ip,s.created_at,s.last_seen_at,s.renewed_at,s.expires_at`

func Lookup(token string, now time.Time) (*Session, error) {
	if token == "" {
		return nil, ErrInvalid
	}
	return scan(db.DB().QueryRow(`SELECT `+columns+` FROM browser_sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>?`, Hash(token), now.Unix()))
}

// Touch uses UPDATE only, so a stale request cannot recreate a revoked session.
// Conditional timestamps ensure parallel requests perform each write once.
func Touch(s *Session, ip string, now time.Time) (bool, error) {
	if now.Sub(s.LastSeenAt) < time.Minute && now.Sub(s.RenewedAt) < 24*time.Hour {
		return false, nil
	}
	result, err := db.DB().Exec(`UPDATE browser_sessions SET
last_seen_at=?,last_ip=?,
expires_at=CASE WHEN renewed_at<=? THEN ? ELSE expires_at END,
renewed_at=CASE WHEN renewed_at<=? THEN ? ELSE renewed_at END
WHERE id=? AND expires_at>? AND (last_seen_at<=? OR renewed_at<=?)`, now.Unix(), ip,
		now.Add(-24*time.Hour).Unix(), now.Add(Lifetime).Unix(), now.Add(-24*time.Hour).Unix(), now.Unix(),
		s.ID, now.Unix(), now.Add(-time.Minute).Unix(), now.Add(-24*time.Hour).Unix())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	renewed := n == 1 && now.Sub(s.RenewedAt) >= 24*time.Hour
	if renewed {
		s.ExpiresAt = now.Add(Lifetime).UTC()
		s.RenewedAt = now.UTC()
	}
	return renewed, nil
}

func List(userID string, admin bool, currentID string, now time.Time) ([]Session, error) {
	rows, err := db.DB().Query(`SELECT `+columns+` FROM browser_sessions s JOIN users u ON u.id=s.user_id WHERE s.expires_at>? AND (? OR s.user_id=?) ORDER BY (s.id=?) DESC,s.last_seen_at DESC,s.created_at DESC,s.id`, now.Unix(), admin, userID, currentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Session{}
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, err
		}
		s.Current = s.ID == currentID
		list = append(list, *s)
	}
	return list, rows.Err()
}

func Revoke(id, userID string, admin bool, now time.Time) (bool, error) {
	result, err := db.DB().Exec(`DELETE FROM browser_sessions WHERE id=? AND (? OR user_id=?) AND expires_at>?`, id, admin, userID, now.Unix())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func Logout(token string) error {
	_, err := db.DB().Exec(`DELETE FROM browser_sessions WHERE token_hash=?`, Hash(token))
	return err
}

func NewChallenge(userID string, version int64, purpose, context, secret string, now time.Time) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	result, err := db.DB().Exec(`INSERT INTO auth_challenges(token_hash,user_id,auth_version,purpose,context,secret,expires_at)
SELECT ?,id,?,?,?,?,? FROM users WHERE id=? AND auth_version=?`, Hash(token), version, purpose, context, secret, now.Add(ChallengeLifetime).Unix(), userID, version)
	if err != nil {
		return "", err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return "", ErrInvalid
	}
	return token, nil
}

func GetChallenge(token, purpose, context string, now time.Time) (*Challenge, error) {
	if token == "" {
		return nil, ErrInvalid
	}
	var ch Challenge
	err := db.DB().QueryRow(`SELECT c.user_id,c.auth_version,c.secret FROM auth_challenges c JOIN users u ON u.id=c.user_id AND u.auth_version=c.auth_version WHERE c.token_hash=? AND c.purpose=? AND c.context=? AND c.expires_at>?`, Hash(token), purpose, context, now.Unix()).Scan(&ch.UserID, &ch.AuthVersion, &ch.Secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalid
	}
	return &ch, err
}

func DeleteChallenge(token string) error {
	_, err := db.DB().Exec(`DELETE FROM auth_challenges WHERE token_hash=?`, Hash(token))
	return err
}

// InvalidateUserTx belongs to the credential-change transaction. Keep only the
// caller's fully authenticated session; all pending challenges are invalidated.
func InvalidateUserTx(tx *sql.Tx, userID, keepID string) error {
	if _, err := tx.Exec(`DELETE FROM browser_sessions WHERE user_id=? AND id<>?`, userID, keepID); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM auth_challenges WHERE user_id=?`, userID)
	return err
}

// ClearRestored removes browser credentials from an offline restored database.
// Older backups do not contain these tables.
func ClearRestored(d *sql.DB) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"browser_sessions", "auth_challenges"} {
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func describeAgent(agent string) (device, browser, os string) {
	device, browser, os = "Unknown device", "Unknown browser", "Unknown OS"
	switch {
	case strings.Contains(agent, "iPhone"):
		device, os = "iPhone", "iOS"
	case strings.Contains(agent, "iPad") || strings.Contains(agent, "Macintosh") && strings.Contains(agent, "Mobile/"):
		device, os = "iPad", "iPadOS"
	case strings.Contains(agent, "Android"):
		device, os = "Android device", "Android"
	case strings.Contains(agent, "Windows"):
		device, os = "Computer", "Windows"
	case strings.Contains(agent, "Macintosh"):
		device, os = "Mac", "macOS"
	case strings.Contains(agent, "CrOS"):
		device, os = "Chromebook", "ChromeOS"
	case strings.Contains(agent, "Linux"):
		device, os = "Computer", "Linux"
	}
	switch {
	case strings.Contains(agent, "Edg/") || strings.Contains(agent, "EdgiOS/") || strings.Contains(agent, "EdgA/"):
		browser = "Edge"
	case strings.Contains(agent, "OPR/") || strings.Contains(agent, "OPiOS/"):
		browser = "Opera"
	case strings.Contains(agent, "Firefox/") || strings.Contains(agent, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(agent, "Chrome/") || strings.Contains(agent, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(agent, "Safari/") && strings.Contains(agent, "Version/"):
		browser = "Safari"
	}
	return
}
