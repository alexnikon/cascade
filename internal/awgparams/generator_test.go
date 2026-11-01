package awgparams

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

//go:embed architect-fixtures.json
var referenceCases []byte

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func boolean(v bool) *bool                    { return &v }
func integer(v int) *int                      { return &v }

func TestArchitectReferenceCases(t *testing.T) {
	var cases []struct {
		Name    string
		Options Options
		Params  Params
	}
	if err := json.Unmarshal(referenceCases, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got, e := generateProfile(tc.Options, zeroReader{}, func() time.Time { return time.Unix(1700000000, 0) })
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(got.Params, tc.Params) {
				a, _ := json.MarshalIndent(got.Params, "", " ")
				b, _ := json.MarshalIndent(tc.Params, "", " ")
				t.Fatalf("Go differs from Architect %s\ngot %s\nwant %s", ArchitectVersion, a, b)
			}
		})
	}
}

func TestGeneratorClientProfileMatrix(t *testing.T) {
	for _, version := range []string{"2.0", "3.1"} {
		for _, client := range catalogue.Clients {
			for _, profile := range Profiles {
				for iteration := 0; iteration < 5; iteration++ {
					o := Options{ProtocolVersion: version, ClientID: client.ID, Profile: profile.ID, MimicAll: true, UseExtremeMax: iteration%2 == 0, RouterMode: iteration%3 == 0, UseDisableH: boolean(false), UseNarrowH: boolean(true)}
					r, e := GenerateProfile(o)
					if e != nil {
						t.Fatalf("%s/%s/%s: %v", version, client.ID, profile.ID, e)
					}
					p := r.Params
					if chainBytes([]string{p.I1, p.I2, p.I3, p.I4, p.I5}) > ChainBudget {
						t.Fatal("kernel budget exceeded")
					}
					if p.Jc > client.Limits.MaxJc {
						t.Fatal("client junk limit exceeded")
					}
					for _, h := range []string{p.H1, p.H2, p.H3, p.H4} {
						v, e := parseUintRange(h)
						if e != nil || v[1] > uint64(client.Limits.MaxHValue) {
							t.Fatal("client H limit exceeded")
						}
					}
					if !client.Limits.SupportsI1I5 && p.I1 != "" {
						t.Fatal("unsupported CPS emitted")
					}
					if o.RouterMode && p.I2 != "" {
						t.Fatal("router mode emitted trailing chain")
					}
				}
			}
		}
	}
}

func TestGeneratorOptionsAndSwitches(t *testing.T) {
	r, e := GenerateProfile(Options{JunkLevel: integer(0), UseHeaderProtection: boolean(false), UseContentPadding: boolean(false), UseRandomTimings: boolean(false), UseRandomTrailers: boolean(false), UseDisableCookies: boolean(false)})
	if e != nil {
		t.Fatal(e)
	}
	if r.Params.Jc != 0 || r.Params.HeaderProtectionKey != "" || r.Params.ContentPaddingAddition != "" || r.Params.RekeyTimeout != "" || *r.Params.RandomTrailers || *r.Params.DisableCookies {
		t.Fatal("explicit off/zero lost")
	}
	if r.Options.UseDisableH || r.Options.UseSameS {
		t.Fatal("dependent switch remained active")
	}
	legacy, e := GenerateProfile(Options{ProtocolVersion: "2.0", Profile: "dtls"})
	if e != nil || legacy.ResolvedProfile != "dtls_1_2" || legacy.Params.HeaderProtectionKey != "" || legacy.Params.RandomTrailers != nil {
		t.Fatal("legacy alias/version broken")
	}
	r, e = GenerateProfile(Options{ClientID: "amneziavpn"})
	if e != nil || r.Params.HeaderProtectionKey == "" || *r.Params.DisableCookies {
		t.Fatal("Cascade key/default cookies policy broken")
	}
	r, e = GenerateProfile(Options{ClientID: "amneziawg-windows", ClientRelease: "<2.0.2", UseDisableH: boolean(false)})
	if e != nil {
		t.Fatal(e)
	}
	h, _ := parseUintRange(r.Params.H4)
	if h[1] > 2147483647 {
		t.Fatal("old Windows limit ignored")
	}
	for n := 0; n < 50; n++ {
		r, e = GenerateProfile(Options{UseSameS: boolean(true)})
		if e != nil || r.Params.S1 != r.Params.S2 || r.Params.S2 != r.Params.S3 || r.Params.S3 != r.Params.S4 {
			t.Fatal("equal S mode broken")
		}
	}
}

func TestGeneratorRejectsInvalidOptionsAndEntropyFailure(t *testing.T) {
	for _, o := range []Options{{ProtocolVersion: "3.0"}, {Profile: "oops"}, {Intensity: "extreme"}, {ClientID: "missing"}, {ClientRelease: "old"}, {HostRegion: "us"}, {Browser: "unknown"}, {MTU: integer(575)}, {MTU: integer(9001)}, {JunkLevel: integer(-1)}, {JunkLevel: integer(129)}, {IterCount: -1}, {Host: "a\r\nInjected"}, {Host: strings.Repeat("a", 64) + ".com"}} {
		if _, e := GenerateProfile(o); !errors.Is(e, ErrInvalidOptions) {
			t.Fatalf("invalid options accepted: %+v, %v", o, e)
		}
	}
	if _, e := generateProfile(Options{}, brokenReader{}); !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Fatalf("entropy failure not propagated: %v", e)
	}
}

// Expand dynamic tags with arbitrary bytes, then parse protocol length fields.
func expand(t *testing.T, chain string) []byte {
	t.Helper()
	re := regexp.MustCompile(`<([^>]+)>`)
	var out []byte
	for _, match := range re.FindAllStringSubmatch(chain, -1) {
		p := strings.Fields(match[1])
		switch p[0] {
		case "b":
			b, e := hex.DecodeString(p[1][2:])
			if e != nil {
				t.Fatal(e)
			}
			out = append(out, b...)
		case "t", "c":
			out = append(out, make([]byte, 4)...)
		default:
			n, e := strconv.Atoi(p[1])
			if e != nil {
				t.Fatal(e)
			}
			out = append(out, make([]byte, n)...)
		}
	}
	return out
}
func readVarint(b []byte) (int, int) {
	n := 1 << uint(b[0]>>6)
	v := int(b[0] & 63)
	for k := 1; k < n; k++ {
		v = v<<8 | int(b[k])
	}
	return v, n
}
func TestPacketFraming(t *testing.T) {
	for _, profile := range []string{"quic_initial", "quic_0rtt", "http3", "tls_client_hello", "dtls_1_2", "dtls_1_3", "dns_query", "sip", "stun"} {
		for _, static := range []bool{false, true} {
			for n := 0; n < 20; n++ {
				r, e := GenerateProfile(Options{ProtocolVersion: "2.0", Profile: profile, ClientID: "awg-kmod", Host: "example.com", UseTagR: boolean(!static), UseTagT: boolean(!static), UseTagC: boolean(!static), UseTagRC: boolean(!static), UseTagRD: boolean(!static)})
				if e != nil {
					t.Fatal(e)
				}
				b := expand(t, r.Params.I1)
				switch profile {
				case "quic_initial", "quic_0rtt", "http3":
					pos := 5
					pos += 1 + int(b[pos])
					pos += 1 + int(b[pos])
					if (b[0]>>4)&3 == 0 {
						token, n := readVarint(b[pos:])
						pos += n + token
					}
					length, n := readVarint(b[pos:])
					pos += n
					if length != len(b)-pos {
						t.Fatalf("QUIC length %d != %d", length, len(b)-pos)
					}
				case "tls_client_hello":
					if int(binary.BigEndian.Uint16(b[3:5])) != len(b)-5 {
						t.Fatal("TLS record length mismatch")
					}
				case "dtls_1_2", "dtls_1_3":
					if int(binary.BigEndian.Uint16(b[11:13])) != len(b)-13 {
						t.Fatal("DTLS record length mismatch")
					}
				case "dns_query":
					pos := 12
					for b[pos] != 0 {
						pos += 1 + int(b[pos])
					}
					pos += 5
					if binary.BigEndian.Uint16(b[10:12]) == 1 {
						if len(b)-pos < 15 || int(binary.BigEndian.Uint16(b[pos+13:pos+15])) != len(b)-pos-15 {
							t.Fatal("DNS EDNS padding length mismatch")
						}
					} else if pos != len(b) {
						t.Fatal("undeclared DNS bytes")
					}
				case "sip":
					parts := bytes.SplitN(b, []byte("\r\n\r\n"), 2)
					if len(parts) != 2 {
						t.Fatal("SIP header terminator missing")
					}
					re := regexp.MustCompile(`(?:Content-Length|l): (\d+)`)
					match := re.FindSubmatch(parts[0])
					size, _ := strconv.Atoi(string(match[1]))
					if size != len(parts[1]) {
						t.Fatal("SIP body length mismatch")
					}
				case "stun":
					if binary.BigEndian.Uint32(b[4:8]) != 0x2112a442 || int(binary.BigEndian.Uint16(b[2:4])) != len(b)-20 {
						t.Fatal("STUN framing mismatch")
					}
					if static {
						last := len(b) - 8
						if binary.BigEndian.Uint16(b[last:last+2]) != 0x8028 || binary.BigEndian.Uint32(b[last+4:]) != crc32.ChecksumIEEE(b[:last])^0x5354554e {
							t.Fatal("STUN fingerprint mismatch")
						}
					}
				}
			}
		}
	}
}

func TestBrowserAndRegionalPools(t *testing.T) {
	for _, browser := range catalogue.Browsers {
		for _, region := range []string{"any", "ru", "global", "eu", "cn", "by", "uk"} {
			r, e := GenerateProfile(Options{Browser: browser.ID, HostRegion: region, Profile: "quic_initial"})
			if e != nil {
				t.Fatalf("%s/%s: %v", browser.ID, region, e)
			}
			size, e := PacketSize(r.Params.I1)
			if e != nil || size > r.MTU {
				t.Fatalf("fingerprint packet exceeds MTU: %d, %v", size, e)
			}
		}
	}
}
func TestValidationOptionalFieldsAndCollisions(t *testing.T) {
	r, e := GenerateProfile(Options{})
	if e != nil {
		t.Fatal(e)
	}
	s := r.Params.settings()
	s.HeaderProtectionKey = ""
	s.ContentPaddingAddition = ""
	s.RekeyAfterTime = ""
	s.RekeyTimeout = ""
	s.RejectAfterTime = ""
	s.KeepaliveTimeout = ""
	s.MaxHandshakeAttempts = ""
	s.RandomTrailers = nil
	s.DisableCookies = nil
	s.S1 = 1
	s.S2 = 2
	s.S3 = 3
	s.S4 = 4
	if e := Validate(ProtocolAWG3, s); e != nil {
		t.Fatal(e)
	}
	s.S2 = s.S1 + 56
	if Validate(ProtocolAWG3, s) == nil {
		t.Fatal("S2 collision accepted")
	}
	s.S2 = 2
	s.S3 = s.S1 + 84
	if Validate(ProtocolAWG3, s) == nil {
		t.Fatal("S3 initiation collision accepted")
	}
	s.S3 = s.S2 + 28
	if Validate(ProtocolAWG3, s) == nil {
		t.Fatal("S3 response collision accepted")
	}
	for _, chain := range []string{"<r 1001>", "<b 0x1>", "<d>", "garbage", "<rc -1>"} {
		if _, e := PacketSize(chain); e == nil {
			t.Fatalf("malformed chain accepted: %s", chain)
		}
	}
	if _, e := PacketSize(fmt.Sprintf("<b 0x%s>", strings.Repeat("00", 65536))); e == nil {
		t.Fatal("oversized packet accepted")
	}
}
