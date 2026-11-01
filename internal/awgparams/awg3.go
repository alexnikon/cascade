package awgparams

import (
	"encoding/base64"
	"encoding/hex"
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
	opts.ProtocolVersion = "3.1"
	result, err := GenerateProfile(opts)
	if err != nil {
		return nil, err
	}
	return result.Params.settings(), nil
}

// Validate checks protocol fields without requiring disabled AWG 3.1 extensions.
func Validate(protocol string, s *peer.AWGSettings) error {
	if protocol != ProtocolAWG1 && protocol != ProtocolAWG2 && protocol != ProtocolAWG3 {
		return fmt.Errorf("unsupported AmneziaWG protocol %q", protocol)
	}
	if s == nil {
		return fmt.Errorf("settings are required for %s", protocol)
	}
	if s.Jc < 0 || s.Jc > 128 || s.Jmin < 0 || s.Jmax > 65535 || s.Jmin > s.Jmax {
		return fmt.Errorf("invalid junk count or size range")
	}
	sizes := []int{s.S1, s.S2, s.S3, s.S4}
	for n, v := range sizes {
		if v < 0 || v > 65535 {
			return fmt.Errorf("S%d must be 0–65535", n+1)
		}
		if protocol == ProtocolAWG3 && s.HeaderProtectionKey != "" && v < 12 {
			return fmt.Errorf("S%d must be at least 12 with header protection", n+1)
		}
	}
	if s.S2 == s.S1+56 || (protocol != ProtocolAWG1 && (s.S3 == s.S1+84 || s.S3 == s.S2+28)) {
		return fmt.Errorf("padded handshake and cookie packet sizes must not collide")
	}
	headers := []string{s.H1, s.H2, s.H3, s.H4}
	ranges := make([][2]uint64, 4)
	for n, h := range headers {
		r, e := parseUintRange(h)
		if e != nil || r[1] > 4294967295 {
			return fmt.Errorf("H%d must be a uint32 value or range", n+1)
		}
		ranges[n] = r
		for j := 0; j < n; j++ {
			if r[0] <= ranges[j][1] && ranges[j][0] <= r[1] {
				return fmt.Errorf("H%d overlaps H%d", n+1, j+1)
			}
		}
	}
	chain := []string{s.I1, s.I2, s.I3, s.I4, s.I5}
	if chainBytes(chain) > ChainBudget {
		return fmt.Errorf("CPS chain exceeds the 3200-byte kernel budget")
	}
	for n, c := range chain {
		if _, e := PacketSize(c); e != nil {
			return fmt.Errorf("I%d: %w", n+1, e)
		}
	}
	if protocol != ProtocolAWG3 {
		if s.HeaderProtectionKey != "" || s.ContentPaddingAddition != "" || s.RekeyAfterTime != "" || s.RekeyTimeout != "" || s.RejectAfterTime != "" || s.KeepaliveTimeout != "" || s.MaxHandshakeAttempts != "" || s.RandomTrailers != nil || s.DisableCookies != nil {
			return fmt.Errorf("AWG 3.1 extensions require protocolVersion 3.1")
		}
		return nil
	}
	if s.HeaderProtectionKey != "" {
		b, e := base64.StdEncoding.DecodeString(s.HeaderProtectionKey)
		if e != nil || len(b) != 32 {
			return fmt.Errorf("HeaderProtectionKey must be a Base64-encoded 32-byte key")
		}
	}
	fields := map[string]string{"ContentPaddingAddition": s.ContentPaddingAddition, "RekeyAfterTime": s.RekeyAfterTime, "RekeyTimeout": s.RekeyTimeout, "RejectAfterTime": s.RejectAfterTime, "KeepaliveTimeout": s.KeepaliveTimeout, "MaxHandshakeAttempts": s.MaxHandshakeAttempts}
	for name, value := range fields {
		if value != "" {
			if e := validateRange(value); e != nil {
				return fmt.Errorf("%s: %w", name, e)
			}
		}
	}
	timing := func(v string, def uint64) [2]uint64 {
		if v == "" {
			return [2]uint64{def, def}
		}
		r, _ := parseUintRange(v)
		return r
	}
	ra := timing(s.RekeyAfterTime, 120)
	rt := timing(s.RekeyTimeout, 5)
	reject := timing(s.RejectAfterTime, 180)
	ka := timing(s.KeepaliveTimeout, 10)
	attempts := timing(s.MaxHandshakeAttempts, 18)
	if rt[0] == 0 || attempts[0] == 0 || ra[1] >= reject[0] || reject[0] <= ka[0]+rt[0] {
		return fmt.Errorf("protocol timers must leave time to refresh keys before rejection")
	}
	return nil
}
func parseUintRange(value string) ([2]uint64, error) {
	var r [2]uint64
	parts := strings.Split(value, "-")
	if len(parts) < 1 || len(parts) > 2 {
		return r, fmt.Errorf("must be a non-negative value or min-max range")
	}
	a, e := strconv.ParseUint(parts[0], 10, 32)
	if e != nil {
		return r, e
	}
	b := a
	if len(parts) == 2 {
		b, e = strconv.ParseUint(parts[1], 10, 32)
		if e != nil {
			return r, e
		}
	}
	if a > b {
		return r, fmt.Errorf("min must not exceed max")
	}
	return [2]uint64{a, b}, nil
}
func validateRange(value string) error { _, e := parseUintRange(value); return e }

// PacketSize parses the CPS grammar and returns its expanded packet size.
func PacketSize(chain string) (int, error) {
	size := 0
	for chain != "" {
		if !strings.HasPrefix(chain, "<") {
			return 0, fmt.Errorf("invalid CPS tag")
		}
		end := strings.IndexByte(chain, '>')
		if end < 0 {
			return 0, fmt.Errorf("unterminated CPS tag")
		}
		parts := strings.Fields(chain[1:end])
		if len(parts) == 0 {
			return 0, fmt.Errorf("empty CPS tag")
		}
		switch parts[0] {
		case "c", "t":
			if len(parts) != 1 {
				return 0, fmt.Errorf("invalid %s tag", parts[0])
			}
			size += 4
		case "b":
			if len(parts) != 2 || !strings.HasPrefix(parts[1], "0x") {
				return 0, fmt.Errorf("invalid byte tag")
			}
			b, e := hex.DecodeString(parts[1][2:])
			if e != nil || len(b) == 0 {
				return 0, fmt.Errorf("invalid hexadecimal bytes")
			}
			size += len(b)
		case "r", "rc", "rd":
			if len(parts) != 2 {
				return 0, fmt.Errorf("invalid random tag")
			}
			n, e := strconv.Atoi(parts[1])
			if e != nil || n < 1 || n > 1000 {
				return 0, fmt.Errorf("random tag size must be 1–1000")
			}
			size += n
		default:
			return 0, fmt.Errorf("unsupported CPS tag %q", parts[0])
		}
		chain = chain[end+1:]
	}
	if size > 65535 {
		return 0, fmt.Errorf("packet exceeds protocol size limit")
	}
	return size, nil
}
