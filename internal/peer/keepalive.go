package peer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexnikon/cascade/internal/db"
)

var keepaliveRangePattern = regexp.MustCompile(`^([0-9]+)-([0-9]+)$`)

// NormalizeKeepaliveRange validates an optional AWG 3.1 interval in seconds.
func NormalizeKeepaliveRange(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parts := keepaliveRangePattern.FindStringSubmatch(value)
	if parts != nil {
		low, errLow := strconv.Atoi(parts[1])
		high, errHigh := strconv.Atoi(parts[2])
		if errLow == nil && errHigh == nil && low >= 1 && low <= high && high <= 65535 {
			return fmt.Sprintf("%d-%d", low, high), nil
		}
	}
	return "", fmt.Errorf("persistentKeepaliveV3: expected min-max seconds with 1 <= min <= max <= 65535")
}

func (p *Peer) validateKeepaliveRange() error {
	value, err := NormalizeKeepaliveRange(p.PersistentKeepaliveV3)
	if err != nil {
		return err
	}
	if value != "" {
		var protocol string
		if err := db.DB().QueryRow("SELECT protocol FROM interfaces WHERE id = ?", p.InterfaceID).Scan(&protocol); err != nil {
			return fmt.Errorf("persistentKeepaliveV3: read interface protocol: %w", err)
		}
		if protocol != "amneziawg-3.1" {
			return fmt.Errorf("persistentKeepaliveV3 requires AWG 3.1")
		}
	}
	p.PersistentKeepaliveV3 = value
	return nil
}

// KeepaliveValue selects the range only for AWG 3.1; the numeric fallback stays intact.
func (p *Peer) KeepaliveValue(protocol string) string {
	if protocol == "amneziawg-3.1" && p.PersistentKeepaliveV3 != "" {
		return p.PersistentKeepaliveV3
	}
	return strconv.Itoa(p.PersistentKeepalive)
}

// NormalizeClientKeepaliveTimeout validates the AWG 3.1 [Interface] timer.
func NormalizeClientKeepaliveTimeout(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	low, high, hasRange := strings.Cut(value, "-")
	min, err := strconv.ParseUint(low, 10, 16)
	if err == nil {
		if !hasRange {
			return strconv.FormatUint(min, 10), nil
		}
		max, err := strconv.ParseUint(high, 10, 16)
		if err == nil && min <= max {
			return fmt.Sprintf("%d-%d", min, max), nil
		}
	}
	return "", fmt.Errorf("expected seconds or a min-max range with 0 <= min <= max <= 65535")
}

// ValidateClientKeepaliveTimeout also checks whether a peer can use the override.
func ValidateClientKeepaliveTimeout(value, protocol, peerType string) (string, error) {
	value, err := NormalizeClientKeepaliveTimeout(value)
	if err != nil {
		return "", fmt.Errorf("clientKeepaliveTimeout: %w", err)
	}
	if value != "" && (protocol != "amneziawg-3.1" || (peerType != "" && peerType != "client")) {
		return "", fmt.Errorf("clientKeepaliveTimeout requires an AWG 3.1 client peer")
	}
	return value, nil
}

func (p *Peer) setClientKeepaliveTimeout(value string) error {
	var protocol string
	if strings.TrimSpace(value) != "" {
		if err := db.DB().QueryRow("SELECT protocol FROM interfaces WHERE id = ?", p.InterfaceID).Scan(&protocol); err != nil {
			return fmt.Errorf("clientKeepaliveTimeout: read interface protocol: %w", err)
		}
	}
	timeout, err := ValidateClientKeepaliveTimeout(value, protocol, p.PeerType)
	if err == nil {
		p.ClientKeepaliveTimeout = timeout
	}
	return err
}
