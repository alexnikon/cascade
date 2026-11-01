// Package awgparams generates version-aware AmneziaWG transport parameters.
// Go port of Any-Tech-ARCHITECT v4.5.1; see THIRD_PARTY_NOTICES.md.
//
// Supported CPS profiles for I1 packet:
//
//	quic_initial     — QUIC Initial (RFC 9000, Long Header 0xC0-0xC3)
//	quic_0rtt        — QUIC 0-RTT (Long Header 0xD0-0xD3)
//	tls_client_hello — TLS 1.3 ClientHello
//	dtls_1_2         — DTLS 1.2 ClientHello
//	dtls_1_3         — DTLS 1.3 ClientHello
//	stun             — STUN / TURN and ICE
//	http3            — HTTP/3 over QUIC
//	sip              — SIP REGISTER request (no BFP entry — always uses protocol defaults)
//	wireguard_noise  — WireGuard Noise_IK handshake initiation
//	dns_query        — DNS A/AAAA query (RFC 1035, no BFP entry)
//	tls_to_quic      — composite: TLS ClientHello → QUIC Initial
//	quic_burst       — composite: QUIC Initial → QUIC 0-RTT → HTTP/3
//	random           — pick one of the non-composite profiles at random
package awgparams

// ── Types ─────────────────────────────────────────────────────────────────────

// Options controls the generator behaviour.
type Options struct {
	ProtocolVersion     string `json:"protocolVersion"`
	Profile             string `json:"profile"`
	Intensity           string `json:"intensity"`
	Host                string `json:"host"`
	Browser             string `json:"browser"`
	BrowserProfile      string `json:"browserProfile"`
	IterCount           int    `json:"iterCount"`
	Jc                  *int   `json:"jc"`
	JunkLevel           *int   `json:"junkLevel"`
	MTU                 *int   `json:"mtu"`
	ClientID            string `json:"clientId"`
	ClientRelease       string `json:"clientRelease"`
	HostRegion          string `json:"hostRegion"`
	MimicAll            bool   `json:"mimicAll"`
	RouterMode          bool   `json:"routerMode"`
	UseExtremeMax       bool   `json:"useExtremeMax"`
	UseBrowserFp        *bool  `json:"useBrowserFp"`
	UseTagC             *bool  `json:"useTagC"`
	UseTagT             *bool  `json:"useTagT"`
	UseTagR             *bool  `json:"useTagR"`
	UseTagRC            *bool  `json:"useTagRC"`
	UseTagRD            *bool  `json:"useTagRD"`
	UseHeaderProtection *bool  `json:"useHeaderProtection"`
	UseContentPadding   *bool  `json:"useContentPadding"`
	UseRandomTimings    *bool  `json:"useRandomTimings"`
	UseRandomTrailers   *bool  `json:"useRandomTrailers"`
	UseDisableCookies   *bool  `json:"useDisableCookies"`
	UseNarrowH          *bool  `json:"useNarrowH"`
	UseSameS            *bool  `json:"useSameS"`
	UseDisableH         *bool  `json:"useDisableH"`
}

// Params is the shared AWG 2.0/3.1 obfuscation parameter set.
type Params struct {
	Jc                     int    `json:"jc"`
	Jmin                   int    `json:"jmin"`
	Jmax                   int    `json:"jmax"`
	S1                     int    `json:"s1"`
	S2                     int    `json:"s2"`
	S3                     int    `json:"s3"`
	S4                     int    `json:"s4"`
	H1                     string `json:"h1"`
	H2                     string `json:"h2"`
	H3                     string `json:"h3"`
	H4                     string `json:"h4"`
	I1                     string `json:"i1"`
	I2                     string `json:"i2"`
	I3                     string `json:"i3"`
	I4                     string `json:"i4"`
	I5                     string `json:"i5"`
	Profile                string `json:"profile"` // resolved profile (never "random")
	HeaderProtectionKey    string `json:"headerProtectionKey,omitempty"`
	ContentPaddingAddition string `json:"contentPaddingAddition,omitempty"`
	RekeyAfterTime         string `json:"rekeyAfterTime,omitempty"`
	RekeyTimeout           string `json:"rekeyTimeout,omitempty"`
	RejectAfterTime        string `json:"rejectAfterTime,omitempty"`
	KeepaliveTimeout       string `json:"keepaliveTimeout,omitempty"`
	MaxHandshakeAttempts   string `json:"maxHandshakeAttempts,omitempty"`
	RandomTrailers         *bool  `json:"randomTrailers,omitempty"`
	DisableCookies         *bool  `json:"disableCookies,omitempty"`
}

// Profile is a UI-facing profile descriptor.
type Profile struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Profiles is the list of supported profiles returned to the UI.
var Profiles = []Profile{
	{ID: "random", Label: "Random"},
	{ID: "quic_initial", Label: "QUIC Initial"},
	{ID: "quic_0rtt", Label: "QUIC 0-RTT"},
	{ID: "tls_client_hello", Label: "TLS 1.3"},
	{ID: "dtls_1_2", Label: "DTLS 1.2"},
	{ID: "dtls_1_3", Label: "DTLS 1.3"},
	{ID: "stun", Label: "STUN / TURN"},
	{ID: "http3", Label: "HTTP/3"},
	{ID: "sip", Label: "SIP"},
	{ID: "wireguard_noise", Label: "Noise_IK (WireGuard)"},
	{ID: "dns_query", Label: "DNS Query (RFC 1035)"},
	{ID: "tls_to_quic", Label: "TLS→QUIC (composite)"},
	{ID: "quic_burst", Label: "QUIC Burst (composite)"},
}

// Generate is the legacy AWG 2.0 convenience API. New callers should use
// GenerateProfile so entropy and input errors can be returned to the caller.
func Generate(opts Options) Params {
	opts.ProtocolVersion = "2.0"
	result, err := GenerateProfile(opts)
	if err != nil {
		panic(err)
	}
	return result.Params
}
