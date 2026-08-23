package awgparams

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/alexnikon/cascade/internal/peer"
)

const (
	ProtocolAWG1 = "amneziawg-1.0"
	ProtocolAWG2 = "amneziawg-2.0"
	ProtocolAWG3 = "amneziawg-3.1"
)

// GenerateAWG3 returns the current AWG 3.1 defaults plus a unique shared key.
func GenerateAWG3(opts Options) (*peer.AWGSettings, error) {
	p := Generate(opts)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate header protection key: %w", err)
	}
	// Header protection requires every special packet to contain at least 12 bytes.
	p.S1 = max(p.S1, 12)
	p.S2 = max(p.S2, 12)
	p.S3 = max(p.S3, 12)
	p.S4 = max(p.S4, 12)
	on := true
	return &peer.AWGSettings{
		Jc: p.Jc, Jmin: p.Jmin, Jmax: p.Jmax,
		S1: p.S1, S2: p.S2, S3: p.S3, S4: p.S4,
		H1: p.H1, H2: p.H2, H3: p.H3, H4: p.H4,
		I1: p.I1, I2: p.I2, I3: p.I3, I4: p.I4, I5: p.I5,
		HeaderProtectionKey:    base64.StdEncoding.EncodeToString(key),
		ContentPaddingAddition: "10-100",
		RekeyAfterTime:         "100-120",
		RekeyTimeout:           "3-7",
		RejectAfterTime:        "150-180",
		KeepaliveTimeout:       "5-15",
		MaxHandshakeAttempts:   "15-20",
		RandomTrailers:         &on,
		DisableCookies:         &on,
	}, nil
}

// Validate checks the shared AWG fields and AWG 3.1 extensions.
func Validate(protocol string, s *peer.AWGSettings) error {
	if protocol != ProtocolAWG1 && protocol != ProtocolAWG2 && protocol != ProtocolAWG3 {
		return fmt.Errorf("unsupported AmneziaWG protocol %q", protocol)
	}
	if s == nil {
		return fmt.Errorf("settings are required for %s", protocol)
	}
	if s.Jmin > s.Jmax {
		return fmt.Errorf("Jmin must be less than or equal to Jmax")
	}
	sizes := []int{s.S1, s.S2, s.S3, s.S4}
	for i, size := range sizes {
		minimum := 0
		if protocol == ProtocolAWG3 {
			minimum = 12
		}
		if size < minimum {
			return fmt.Errorf("S%d must be at least %d", i+1, minimum)
		}
	}
	if s.S1+56 == s.S2 || s.S2+56 == s.S1 || s.S3+56 == s.S4 || s.S4+56 == s.S3 {
		return fmt.Errorf("special packet sizes must not overlap after the 56-byte offset")
	}
	if protocol == ProtocolAWG1 || protocol == ProtocolAWG2 {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(s.HeaderProtectionKey)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("HeaderProtectionKey must be a Base64-encoded 32-byte key")
	}
	for name, value := range map[string]string{
		"ContentPaddingAddition": s.ContentPaddingAddition,
		"RekeyAfterTime":         s.RekeyAfterTime,
		"RekeyTimeout":           s.RekeyTimeout,
		"RejectAfterTime":        s.RejectAfterTime,
		"KeepaliveTimeout":       s.KeepaliveTimeout,
		"MaxHandshakeAttempts":   s.MaxHandshakeAttempts,
	} {
		if err := validateRange(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if s.RandomTrailers == nil || s.DisableCookies == nil {
		return fmt.Errorf("RandomTrailers and DisableCookies are required for AWG 3.1")
	}
	return nil
}

func validateRange(value string) error {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return fmt.Errorf("must be an inclusive min-max range")
	}
	minValue, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	maxValue, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || minValue < 0 || minValue > maxValue {
		return fmt.Errorf("must be a non-negative range with min <= max")
	}
	return nil
}
