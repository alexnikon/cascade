// Package users manages multi-user authentication data in SQLite.
//
// Users are stored in the `users` table created by migration v7.
// Password hashes use bcrypt cost 12.
// TOTP secrets are stored in plain text (they are already random base32 keys).
// Neither password_hash nor totp_secret is ever exposed in JSON responses.
package users

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/sessions"
)

// bcryptCost is the work factor for hashing new passwords.
const bcryptCost = 12

// User is the public view of a user record (no sensitive fields).
type User struct {
	AuthVersion int64     `json:"-"`
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	IsAdmin     bool      `json:"is_admin"`
	TOTPEnabled bool      `json:"totp_enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

// ── Read operations ───────────────────────────────────────────────────────────

// List returns all users sorted by created_at ascending.
func List() ([]User, error) {
	rows, err := db.DB().Query(
		`SELECT id, username, is_admin, totp_enabled, created_at FROM users ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("users.List: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt string
		if err := rows.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.TOTPEnabled, &createdAt); err != nil {
			return nil, fmt.Errorf("users.List scan: %w", err)
		}
		u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		users = append(users, u)
	}
	if users == nil {
		users = []User{}
	}
	return users, rows.Err()
}

// GetByID returns the user with the given ID, or nil if not found.
func GetByID(id string) (*User, error) {
	var u User
	var createdAt string
	err := db.DB().QueryRow(
		`SELECT id, username, is_admin, totp_enabled, created_at, auth_version FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.IsAdmin, &u.TOTPEnabled, &createdAt, &u.AuthVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("users.GetByID: %w", err)
	}
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return &u, nil
}

// GetByUsername returns the user with the given username (case-insensitive), or nil.
func GetByUsername(username string) (*User, error) {
	var u User
	var createdAt string
	err := db.DB().QueryRow(
		`SELECT id, username, is_admin, totp_enabled, created_at, auth_version FROM users WHERE username = ? COLLATE NOCASE`,
		username,
	).Scan(&u.ID, &u.Username, &u.IsAdmin, &u.TOTPEnabled, &createdAt, &u.AuthVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("users.GetByUsername: %w", err)
	}
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return &u, nil
}

// Count returns the total number of users in the table.
func Count() (int, error) {
	var n int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("users.Count: %w", err)
	}
	return n, nil
}

// CountAdmins returns the number of users with is_admin = 1.
func CountAdmins() (int, error) {
	var n int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n); err != nil {
		return 0, fmt.Errorf("users.CountAdmins: %w", err)
	}
	return n, nil
}

// IsAdmin returns true if the user with the given ID has is_admin = 1.
func IsAdmin(userID string) (bool, error) {
	var admin bool
	err := db.DB().QueryRow(
		`SELECT is_admin FROM users WHERE id = ?`, userID,
	).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("users.IsAdmin: %w", err)
	}
	return admin, nil
}

// SetAdmin sets or clears the is_admin flag for the user with the given ID.
// Returns an error if trying to remove admin from the last remaining admin.
func SetAdmin(userID string, admin bool) error {
	if !admin {
		n, err := CountAdmins()
		if err != nil {
			return err
		}
		if n <= 1 {
			return errors.New("cannot remove admin from the last admin user")
		}
	}
	_, err := db.DB().Exec(
		`UPDATE users SET is_admin = ? WHERE id = ?`,
		admin, userID,
	)
	if err != nil {
		return fmt.Errorf("users.SetAdmin: %w", err)
	}
	return nil
}

// ── Write operations ──────────────────────────────────────────────────────────

// Create inserts a new user with a bcrypt-hashed password and returns the created User.
func Create(username, password string) (*User, error) {
	if username == "" {
		return nil, errors.New("username is required")
	}
	if password == "" {
		return nil, errors.New("password is required")
	}
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("users.Create bcrypt: %w", err)
	}

	// First user created in the system automatically becomes admin.
	// This covers the open-mode registration flow: when the DB has 0 users,
	// callerIsAdmin returns true (open mode). After the first user is inserted
	// the count becomes 1, callerIsAdmin switches to DB lookup — without is_admin=1
	// the creator would immediately lose admin access.
	isAdmin := 0
	if n, err := Count(); err == nil && n == 0 {
		isAdmin = 1
	}

	id := uuid.New().String()
	_, err = db.DB().Exec(
		`INSERT INTO users (id, username, password_hash, is_admin) VALUES (?, ?, ?, ?)`,
		id, username, string(hash), isAdmin,
	)
	if err != nil {
		return nil, fmt.Errorf("users.Create insert: %w", err)
	}

	return GetByID(id)
}

// UpdatePassword replaces the password hash for the user with the given ID.
func UpdatePassword(id, newPassword string, keepSessionID ...string) error {
	if newPassword == "" {
		return errors.New("password is required")
	}
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("users.UpdatePassword bcrypt: %w", err)
	}
	return changeCredentials(id, keepSessionID, `UPDATE users SET password_hash = ?, auth_version=auth_version+1 WHERE id = ?`, []any{string(hash), id}, "", "")
}

// UpdateUsername changes the username for the user with the given ID.
func UpdateUsername(id, newUsername string) error {
	if newUsername == "" {
		return errors.New("username is required")
	}
	_, err := db.DB().Exec(
		`UPDATE users SET username = ? WHERE id = ?`, newUsername, id,
	)
	return err
}

// Delete removes the user with the given ID.
// Returns an error if the user is the last one (cannot delete the last user).
func Delete(id string) error {
	n, err := Count()
	if err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("cannot delete the last user")
	}
	_, err = db.DB().Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// ── Authentication ────────────────────────────────────────────────────────────

// VerifyPasswordByID checks whether the given password matches the stored hash
// for the user with the given ID. Returns nil on success, error on failure.
// Used to verify the caller's identity before allowing a password change.
func VerifyPasswordByID(id, password string) error {
	if id == "" || password == "" {
		return errors.New("invalid credentials")
	}
	var hash string
	err := db.DB().QueryRow(
		`SELECT password_hash FROM users WHERE id = ?`, id,
	).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("invalid credentials")
	}
	if err != nil {
		return fmt.Errorf("users.VerifyPasswordByID: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return errors.New("invalid credentials")
	}
	return nil
}

// VerifyPassword checks whether the given username/password pair is valid.
// Returns the User on success, nil + error on failure.
func VerifyPassword(username, password string) (*User, error) {
	if username == "" || password == "" {
		return nil, errors.New("invalid credentials")
	}

	var id, hash, createdAt string
	var isAdmin, totpEnabled bool
	var authVersion int64
	err := db.DB().QueryRow(
		`SELECT id, username, password_hash, is_admin, totp_enabled, created_at, auth_version
		   FROM users WHERE username = ? COLLATE NOCASE`,
		username,
	).Scan(&id, &username, &hash, &isAdmin, &totpEnabled, &createdAt, &authVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("invalid credentials")
	}
	if err != nil {
		return nil, fmt.Errorf("users.VerifyPassword query: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, errors.New("invalid credentials")
	}

	u := &User{ID: id, Username: username, IsAdmin: isAdmin, TOTPEnabled: totpEnabled, AuthVersion: authVersion}
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
	return u, nil
}

// ── TOTP operations ───────────────────────────────────────────────────────────

// GetTOTPSecret returns the stored TOTP secret for the user.
// Returns an empty string if no secret is set.
func GetTOTPSecret(userID string) (string, error) {
	var secret string
	err := db.DB().QueryRow(
		`SELECT totp_secret FROM users WHERE id = ?`, userID,
	).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("users.GetTOTPSecret: %w", err)
	}
	return secret, nil
}

// SetTOTP saves the TOTP secret and marks totp_enabled = 1.
func SetTOTP(userID, secret string, keepSessionID ...string) error {
	return changeCredentials(userID, keepSessionID, `UPDATE users SET totp_secret=?,totp_enabled=1,auth_version=auth_version+1 WHERE id=?`, []any{secret, userID}, "", "")
}

// SetTOTPFromChallenge atomically consumes a setup challenge and enables 2FA.
func SetTOTPFromChallenge(userID, secret, challenge, context, keepID string) error {
	return changeCredentials(userID, []string{keepID}, `UPDATE users SET totp_secret=?,totp_enabled=1,auth_version=auth_version+1 WHERE id=?`, []any{secret, userID}, challenge, context)
}

// ClearTOTP disables 2FA and invalidates other browser authorizations.
func ClearTOTP(userID string, keepSessionID ...string) error {
	return changeCredentials(userID, keepSessionID, `UPDATE users SET totp_secret='',totp_enabled=0,auth_version=auth_version+1 WHERE id=?`, []any{userID}, "", "")
}

func changeCredentials(userID string, keepIDs []string, query string, args []any, challenge, context string) error {
	tx, err := db.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if challenge != "" {
		result, err := tx.Exec(`DELETE FROM auth_challenges WHERE token_hash=? AND user_id=? AND purpose='setup' AND context=? AND expires_at>? AND auth_version=(SELECT auth_version FROM users WHERE id=?)`, sessions.Hash(challenge), userID, context, time.Now().Unix(), userID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return sessions.ErrInvalid
		}
	}
	if _, err := tx.Exec(query, args...); err != nil {
		return err
	}
	keepID := ""
	if len(keepIDs) > 0 {
		keepID = keepIDs[0]
	}
	if err := sessions.InvalidateUserTx(tx, userID, keepID); err != nil {
		return err
	}
	return tx.Commit()
}

// IsTOTPEnabled returns true if totp_enabled = 1 for the given user.
func IsTOTPEnabled(userID string) (bool, error) {
	var enabled bool
	err := db.DB().QueryRow(
		`SELECT totp_enabled FROM users WHERE id = ?`, userID,
	).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("users.IsTOTPEnabled: %w", err)
	}
	return enabled, nil
}
