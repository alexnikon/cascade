package prometheusmetrics

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	internalmetrics "github.com/alexnikon/cascade/internal/metrics"
)

const (
	settingBootstrap  = "prometheus_metrics_bootstrapped"
	settingEnabled    = "prometheus_metrics_enabled"
	settingPort       = "prometheus_metrics_port"
	legacySettingPath = "prometheus_metrics_path"
	settingThreshold  = "prometheus_metrics_connected_peer_threshold_seconds"
	settingTokenHash  = "prometheus_metrics_token_sha256"
	settingHistory    = "prometheus_metrics_history_enabled"
)

// Snapshot is an immutable copy of the active metrics configuration.
type Snapshot struct {
	Enabled                       bool
	Port                          int
	ConnectedPeerThresholdSeconds int
	TokenConfigured               bool
	HistoryEnabled                bool
	tokenHash                     [sha256.Size]byte
}

// Update is the complete public settings payload. Token is write-only; an empty
// token preserves the existing credential unless ClearToken is true.
type Update struct {
	Enabled                       bool   `json:"enabled"`
	Port                          int    `json:"port"`
	ConnectedPeerThresholdSeconds int    `json:"connectedPeerThresholdSeconds"`
	HistoryEnabled                bool   `json:"historyEnabled"`
	Token                         string `json:"token,omitempty"`
	ClearToken                    bool   `json:"clearToken,omitempty"`
}

// Manager persists settings and publishes lock-free runtime snapshots.
type Manager struct {
	db      *sql.DB
	current atomic.Pointer[Snapshot]
}

// ValidationError identifies client-supplied settings that can be corrected
// without retrying the request.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(message string) error { return &ValidationError{Message: message} }

func NewManager(database *sql.DB, bootstrap Config) (*Manager, error) {
	if database == nil {
		return nil, errors.New("metrics settings database is required")
	}
	m := &Manager{db: database}
	if err := m.bootstrapOrLoad(bootstrap); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Current() Snapshot {
	s := m.current.Load()
	if s == nil {
		return Snapshot{Port: DefaultPort, ConnectedPeerThresholdSeconds: int(defaultConnectedPeerThreshold.Seconds()), HistoryEnabled: true}
	}
	return *s
}

func (m *Manager) ConnectedPeerThreshold() time.Duration {
	return time.Duration(m.Current().ConnectedPeerThresholdSeconds) * time.Second
}

func (m *Manager) Authorize(token string) bool {
	s := m.Current()
	if !s.TokenConfigured {
		return true
	}
	hash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(hash[:], s.tokenHash[:]) == 1
}

func (m *Manager) Update(update Update) (Snapshot, error) {
	next, err := m.next(update)
	if err != nil {
		return Snapshot{}, err
	}
	if err := m.persist(next); err != nil {
		return Snapshot{}, err
	}
	m.publish(next)
	return next, nil
}

func (m *Manager) next(update Update) (Snapshot, error) {
	if update.Port < 1 || update.Port > 65535 {
		return Snapshot{}, invalid("port must be between 1 and 65535")
	}
	if update.ConnectedPeerThresholdSeconds <= 0 {
		return Snapshot{}, invalid("connectedPeerThresholdSeconds must be greater than zero")
	}
	if update.ClearToken && update.Token != "" {
		return Snapshot{}, invalid("token and clearToken cannot be used together")
	}

	next := m.Current()
	next.Enabled = update.Enabled
	next.Port = update.Port
	next.ConnectedPeerThresholdSeconds = update.ConnectedPeerThresholdSeconds
	next.HistoryEnabled = update.HistoryEnabled
	if update.ClearToken {
		next.TokenConfigured = false
		next.tokenHash = [sha256.Size]byte{}
	} else if update.Token != "" {
		next.TokenConfigured = true
		next.tokenHash = sha256.Sum256([]byte(update.Token))
	}
	return next, nil
}

func (m *Manager) bootstrapOrLoad(bootstrap Config) error {
	var marker string
	err := m.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingBootstrap).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		s := Snapshot{
			Enabled: bootstrap.Enabled, Port: DefaultPort,
			ConnectedPeerThresholdSeconds: int(bootstrap.ConnectedPeerThreshold.Seconds()),
			HistoryEnabled:                bootstrap.HistoryEnabled,
		}
		if bootstrap.Token != "" {
			s.TokenConfigured = true
			s.tokenHash = sha256.Sum256([]byte(bootstrap.Token))
		}
		if err := m.persist(s); err != nil {
			return err
		}
		m.publish(s)
		return nil
	}
	if err != nil {
		return fmt.Errorf("load metrics bootstrap marker: %w", err)
	}
	values := map[string]string{}
	rows, err := m.db.Query(`SELECT key, value FROM settings WHERE key LIKE 'prometheus_metrics_%'`)
	if err != nil {
		return fmt.Errorf("load metrics settings: %w", err)
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return err
		}
		values[key] = value
	}
	threshold, err := strconv.Atoi(values[settingThreshold])
	if err != nil || threshold <= 0 {
		return errors.New("stored metrics connected peer threshold is invalid")
	}
	port := DefaultPort
	if raw := values[settingPort]; raw != "" {
		port, err = strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("stored metrics port is invalid")
		}
	}
	s := Snapshot{
		Enabled: values[settingEnabled] == "true", Port: port,
		ConnectedPeerThresholdSeconds: threshold,
		HistoryEnabled:                values[settingHistory] != "false",
	}
	if encoded := values[settingTokenHash]; encoded != "" {
		decoded, err := hex.DecodeString(encoded)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("stored metrics token hash is invalid")
		}
		copy(s.tokenHash[:], decoded)
		s.TokenConfigured = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := m.persist(s); err != nil {
		return err
	}
	m.publish(s)
	return nil
}

func (m *Manager) persist(s Snapshot) error {
	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("begin metrics settings update: %w", err)
	}
	defer tx.Rollback()
	values := map[string]string{
		settingBootstrap: "1", settingEnabled: strconv.FormatBool(s.Enabled),
		settingPort: strconv.Itoa(s.Port), settingThreshold: strconv.Itoa(s.ConnectedPeerThresholdSeconds),
		settingHistory: strconv.FormatBool(s.HistoryEnabled), settingTokenHash: "",
	}
	if s.TokenConfigured {
		values[settingTokenHash] = hex.EncodeToString(s.tokenHash[:])
	}
	for key, value := range values {
		if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return fmt.Errorf("persist metrics setting %s: %w", key, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, legacySettingPath); err != nil {
		return fmt.Errorf("remove legacy metrics path: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metrics settings update: %w", err)
	}
	return nil
}

func (m *Manager) publish(s Snapshot) {
	copy := s
	m.current.Store(&copy)
	internalmetrics.SetHistoryEnabled(s.HistoryEnabled)
}
