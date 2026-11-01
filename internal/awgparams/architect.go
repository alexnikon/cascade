package awgparams

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexnikon/cascade/internal/peer"
)

var ErrInvalidOptions = errors.New("invalid generator options")

const ArchitectVersion = "4.5.1"
const ArchitectRevision = "0b9d3b7aa6c1661daa29b92010f841a3c04ca9fb"
const ChainBudget = 3200

//go:embed architect-data.json
var architectData []byte

type Limits struct {
	MaxHValue    int  `json:"maxHValue"`
	MaxJc        int  `json:"maxJc"`
	MaxS4        int  `json:"maxS4"`
	SupportsS3S4 bool `json:"supportsS3S4"`
	SupportsI1I5 bool `json:"supportsI1I5"`
	SupportsC    bool `json:"supportsCpsTagC"`
	SupportsRC   bool `json:"supportsCpsTagRC"`
	SupportsRD   bool `json:"supportsCpsTagRD"`
	ManagesKey   bool `json:"managesHeaderProtection"`
}
type Release struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Limits struct {
		MaxHValue int `json:"maxHValue"`
	} `json:"limits"`
	Notes []string `json:"notes"`
}
type Client struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Limits   Limits    `json:"limits"`
	Notes    []string  `json:"notes"`
	Releases []Release `json:"releases"`
}
type Browser struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Family string            `json:"family"`
	Sizes  map[string][2]int `json:"sizes"`
}
type hostPool struct {
	Hosts   []string `json:"hosts"`
	Relaxed bool     `json:"relaxed"`
}

var catalogue struct {
	Clients  []Client            `json:"clients"`
	Browsers []Browser           `json:"browsers"`
	Pools    map[string]hostPool `json:"pools"`
}

func init() {
	if err := json.Unmarshal(architectData, &catalogue); err != nil {
		panic(err)
	}
}

// Input is the effective, version-specific generator configuration.
type Input struct {
	ProtocolVersion     string `json:"protocolVersion"`
	Profile             string `json:"profile"`
	Intensity           string `json:"intensity"`
	Host                string `json:"host"`
	Browser             string `json:"browser"`
	HostRegion          string `json:"hostRegion"`
	ClientID            string `json:"clientId"`
	ClientRelease       string `json:"clientRelease"`
	MTU                 int    `json:"mtu"`
	JunkLevel           int    `json:"junkLevel"`
	IterCount           int    `json:"iterCount"`
	MimicAll            bool   `json:"mimicAll"`
	RouterMode          bool   `json:"routerMode"`
	UseExtremeMax       bool   `json:"useExtremeMax"`
	UseBrowserFp        bool   `json:"useBrowserFp"`
	UseTagC             bool   `json:"useTagC"`
	UseTagT             bool   `json:"useTagT"`
	UseTagR             bool   `json:"useTagR"`
	UseTagRC            bool   `json:"useTagRC"`
	UseTagRD            bool   `json:"useTagRD"`
	UseHeaderProtection bool   `json:"useHeaderProtection"`
	UseContentPadding   bool   `json:"useContentPadding"`
	UseRandomTimings    bool   `json:"useRandomTimings"`
	UseRandomTrailers   bool   `json:"useRandomTrailers"`
	UseDisableCookies   bool   `json:"useDisableCookies"`
	UseNarrowH          bool   `json:"useNarrowH"`
	UseSameS            bool   `json:"useSameS"`
	UseDisableH         bool   `json:"useDisableH"`
}

type Result struct {
	Params          Params    `json:"params"`
	Profiles        []Profile `json:"profiles"`
	ProtocolVersion string    `json:"protocolVersion"`
	MTU             int       `json:"mtu"`
	ResolvedProfile string    `json:"resolvedProfile"`
	Options         Input     `json:"options"`
	Warnings        []string  `json:"warnings"`
}

func Defaults(version string) Input {
	i := Input{ProtocolVersion: version, Profile: "random", Intensity: "medium", ClientID: "amneziavpn", HostRegion: "any", Browser: "chrome", MTU: 1500, JunkLevel: 5, UseTagT: true, UseTagR: true, UseTagRC: true, UseTagRD: true}
	if version == "3.1" {
		i.MTU = 1280
		i.UseHeaderProtection = true
		i.UseContentPadding = true
		i.UseRandomTimings = true
		i.UseRandomTrailers = true
		i.UseDisableH = true
	}
	return i
}
func GeneratorOptions() map[string]any {
	return map[string]any{"version": ArchitectVersion, "revision": ArchitectRevision, "profiles": Profiles, "clients": catalogue.Clients, "browsers": catalogue.Browsers, "regions": []string{"any", "ru", "global", "eu", "cn", "by", "uk"}, "defaults": map[string]Input{"2.0": Defaults("2.0"), "3.1": Defaults("3.1")}}
}
func boolOption(p *bool, fallback bool) bool {
	if p != nil {
		return *p
	}
	return fallback
}
func normalize(o Options) (Input, Client, error) {
	v := o.ProtocolVersion
	if v == "" {
		v = "3.1"
	}
	i := Defaults(v)
	if v != "2.0" && v != "3.1" {
		return i, Client{}, fmt.Errorf("protocolVersion must be 2.0 or 3.1")
	}
	if o.Profile != "" {
		i.Profile = o.Profile
	}
	if i.Profile == "dtls" {
		i.Profile = "dtls_1_2"
	}
	found := false
	for _, p := range Profiles {
		if p.ID == i.Profile {
			found = true
		}
	}
	if !found {
		return i, Client{}, fmt.Errorf("unknown profile")
	}
	if o.Intensity != "" {
		i.Intensity = o.Intensity
	}
	if i.Intensity != "low" && i.Intensity != "medium" && i.Intensity != "high" {
		return i, Client{}, fmt.Errorf("unknown intensity")
	}
	if o.ClientID != "" {
		i.ClientID = o.ClientID
	}
	var c Client
	for _, client := range catalogue.Clients {
		if client.ID == i.ClientID {
			c = client
		}
	}
	if c.ID == "" {
		return i, c, fmt.Errorf("unknown clientId")
	}
	i.ClientRelease = o.ClientRelease
	if i.ClientRelease != "" {
		found = false
		for _, r := range c.Releases {
			if r.ID == i.ClientRelease {
				found = true
				if r.Limits.MaxHValue > 0 {
					c.Limits.MaxHValue = r.Limits.MaxHValue
				}
				c.Notes = append(append([]string{}, c.Notes...), r.Notes...)
			}
		}
		if !found {
			return i, c, fmt.Errorf("unknown clientRelease")
		}
	}
	i.Host = strings.TrimSpace(o.Host)
	if i.Host != "" {
		if len(i.Host) > 253 || net.ParseIP(i.Host) != nil || !hostnamePattern.MatchString(i.Host) {
			return i, c, fmt.Errorf("host must be an ASCII hostname")
		}
		for _, label := range strings.Split(i.Host, ".") {
			if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return i, c, fmt.Errorf("invalid hostname label")
			}
		}
	}
	if o.HostRegion != "" {
		i.HostRegion = o.HostRegion
	}
	if !strings.Contains("|any|ru|global|eu|cn|by|uk|", "|"+i.HostRegion+"|") {
		return i, c, fmt.Errorf("unknown hostRegion")
	}
	if o.MTU != nil {
		i.MTU = *o.MTU
	}
	if i.MTU < 576 || i.MTU > 9000 {
		return i, c, fmt.Errorf("mtu must be 576–9000")
	}
	if o.Jc != nil {
		i.JunkLevel = *o.Jc
	}
	if o.JunkLevel != nil {
		i.JunkLevel = *o.JunkLevel
	}
	if i.JunkLevel < 0 || i.JunkLevel > 128 {
		return i, c, fmt.Errorf("junkLevel must be 0–128")
	}
	i.IterCount = o.IterCount
	if i.IterCount < 0 || i.IterCount > 1000 {
		return i, c, fmt.Errorf("iterCount must be 0–1000")
	}
	if o.Browser != "" {
		i.Browser = o.Browser
	}
	if o.BrowserProfile != "" {
		i.Browser = o.BrowserProfile
	}
	found = false
	for _, b := range catalogue.Browsers {
		if b.ID == i.Browser {
			found = true
		}
	}
	if !found {
		return i, c, fmt.Errorf("unknown browser fingerprint")
	}
	i.UseBrowserFp = boolOption(o.UseBrowserFp, o.Browser != "" || o.BrowserProfile != "")
	i.MimicAll = o.MimicAll
	i.RouterMode = o.RouterMode
	i.UseExtremeMax = o.UseExtremeMax
	i.UseTagC = boolOption(o.UseTagC, i.UseTagC)
	i.UseTagT = boolOption(o.UseTagT, i.UseTagT)
	i.UseTagR = boolOption(o.UseTagR, i.UseTagR)
	i.UseTagRC = boolOption(o.UseTagRC, i.UseTagRC)
	i.UseTagRD = boolOption(o.UseTagRD, i.UseTagRD)
	i.UseHeaderProtection = boolOption(o.UseHeaderProtection, i.UseHeaderProtection)
	i.UseContentPadding = boolOption(o.UseContentPadding, i.UseContentPadding)
	i.UseRandomTimings = boolOption(o.UseRandomTimings, i.UseRandomTimings)
	i.UseRandomTrailers = boolOption(o.UseRandomTrailers, i.UseRandomTrailers)
	i.UseDisableCookies = boolOption(o.UseDisableCookies, i.UseDisableCookies)
	i.UseNarrowH = boolOption(o.UseNarrowH, i.UseNarrowH)
	i.UseSameS = boolOption(o.UseSameS, i.UseSameS)
	i.UseDisableH = boolOption(o.UseDisableH, i.UseDisableH)
	if v == "2.0" {
		i.UseHeaderProtection = false
		i.UseContentPadding = false
		i.UseRandomTimings = false
		i.UseRandomTrailers = false
		i.UseDisableCookies = false
		i.UseNarrowH = false
	}
	linked := v == "3.1" && i.UseHeaderProtection && i.UseRandomTrailers
	i.UseSameS = i.UseSameS && linked
	i.UseDisableH = i.UseDisableH && linked
	return i, c, nil
}

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.?$`)

type generator struct {
	input    Input
	client   Client
	reader   io.Reader
	err      error
	warnings []string
	now      func() time.Time
}

func (g *generator) rnd(a, b int) int {
	if b <= a {
		return a
	}
	n, e := rand.Int(g.reader, big.NewInt(int64(b-a+1)))
	if e != nil {
		g.err = e
		return a
	}
	return a + int(n.Int64())
}
func (g *generator) bytes(n int) []byte {
	b := make([]byte, max(0, n))
	if _, e := io.ReadFull(g.reader, b); e != nil {
		g.err = e
	}
	return b
}
func (g *generator) rh(n int) string { return hex.EncodeToString(g.bytes(n)) }
func (g *generator) warning(s string) {
	for _, w := range g.warnings {
		if s == w {
			return
		}
	}
	g.warnings = append(g.warnings, s)
}
func (g *generator) host(role, dns string) string {
	if g.input.Host != "" {
		return g.input.Host
	}
	p := catalogue.Pools[g.input.HostRegion+"|"+role+"|"+dns]
	if p.Relaxed {
		g.warning("The selected region has no verified host for this protocol; the Architect fallback pool was used.")
	}
	if len(p.Hosts) == 0 {
		g.err = fmt.Errorf("empty host pool for %s", role)
		return "example.com"
	}
	return p.Hosts[g.rnd(0, len(p.Hosts)-1)]
}
func (g *generator) browser() Browser {
	for _, b := range catalogue.Browsers {
		if b.ID == g.input.Browser {
			return b
		}
	}
	return Browser{}
}
func (g *generator) padding(header, extra int, slot string, iv int) int {
	room := max(0, g.input.MTU-header-extra)
	if !g.input.UseBrowserFp {
		return min(g.rnd(20, 80)*iv, 500, room)
	}
	r, ok := g.browser().Sizes[slot]
	if !ok {
		return min(g.rnd(20, 80)*iv, 500, room)
	}
	lo, hi := min(r[0], g.input.MTU), min(r[1], g.input.MTU)
	needed := max(0, lo-header-extra)
	jitter := max(0, min(hi-lo, hi-header-extra-needed, 20))
	return min(needed+g.rnd(0, jitter), room)
}
func (g *generator) tag(name string, n int, on bool) string {
	if !on {
		return ""
	}
	if name == "c" || name == "t" {
		return "<" + name + ">"
	}
	if n <= 0 {
		return ""
	}
	var s string
	for n > 1000 {
		s += fmt.Sprintf("<%s 1000>", name)
		n -= 1000
	}
	return s + fmt.Sprintf("<%s %d>", name, n)
}
func blob(h string) string { return "<b 0x" + h + ">" }
func hx(v, n int) string   { return fmt.Sprintf("%0*x", n*2, v) }
func qvar(v int) string {
	if v < 64 {
		return hx(v, 1)
	}
	if v < 16384 {
		return hx(v+0x4000, 2)
	}
	return hx(v+0x80000000, 4)
}
func flagBytes(on bool, n int) int {
	if on {
		return n
	}
	return 0
}
func rangeText(lo, hi int) string {
	if lo == hi {
		return strconv.Itoa(lo)
	}
	return fmt.Sprintf("%d-%d", lo, hi)
}
func chainBytes(chain []string) int {
	n := 0
	for _, s := range chain {
		if s != "" {
			n += 4 + ((len(s)+1+3)/4)*4
		}
	}
	return n
}

// GenerateProfile uses Architect's algorithms, with a Cascade-owned shared key.
func GenerateProfile(o Options) (*Result, error) { return generateProfile(o, rand.Reader) }
func generateProfile(o Options, reader io.Reader, clocks ...func() time.Time) (*Result, error) {
	i, c, e := normalize(o)
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidOptions, e)
	}
	g := &generator{input: i, client: c, reader: reader, warnings: []string{}, now: time.Now}
	if len(clocks) > 0 {
		g.now = clocks[0]
	}
	for _, note := range c.Notes {
		if !(c.Limits.ManagesKey && strings.Contains(note, "HeaderProtectionKey")) {
			g.warning(note)
		}
	}
	if c.Limits.ManagesKey && i.UseHeaderProtection {
		g.warning("Cascade owns the shared HeaderProtectionKey for this template. Configure Amnezia VPN header protection with the same key; an independently generated app key will not match the server.")
	}
	if i.UseTagC && !c.Limits.SupportsC {
		g.warning("The selected client does not support <c>; the tag was disabled.")
	}
	g.input.UseTagC = i.UseTagC && c.Limits.SupportsC
	g.input.UseTagRC = i.UseTagRC && c.Limits.SupportsRC
	g.input.UseTagRD = i.UseTagRD && c.Limits.SupportsRD
	if i.UseRandomTrailers && !i.UseDisableH && !i.UseNarrowH {
		g.warning("Wide H ranges with RandomTrailers can drop transport packets. Use narrow ranges or disable H ranges with header protection enabled.")
	}
	if i.UseDisableCookies {
		g.warning("DisableCookies can disrupt NAT keepalive under load.")
	}
	if i.ProtocolVersion == "3.1" && i.ClientID != "amneziavpn" && i.ClientID != "amneziawg-android" && i.ClientID != "amneziawg-ios" && i.ClientID != "amneziawg-windows" && i.ClientID != "awg-kmod" && i.ClientID != "openwrt" {
		g.warning("Client limits do not establish AWG 3.1 support. Verify that this client build accepts the selected 3.1 extensions.")
	}
	p := Params{Profile: i.Profile}
	if p.Profile == "random" {
		pool := Profiles[1:]
		p.Profile = pool[g.rnd(0, len(pool)-1)].ID
		for p.Profile == "tls_to_quic" || p.Profile == "quic_burst" {
			p.Profile = pool[g.rnd(0, len(pool)-1)].ID
		}
	}
	maxS := 150
	if i.RouterMode {
		maxS = 20
	}
	p.S1 = g.rnd(1, maxS)
	p.S2 = g.rnd(1, maxS)
	s3max := 64
	if i.UseExtremeMax {
		s3max = 256
		p.S3 = g.rnd(65, 256)
	} else {
		p.S3 = g.rnd(1, 64)
	}
	p.S4 = g.rnd(1, min(32, c.Limits.MaxS4))
	if !c.Limits.SupportsS3S4 {
		p.S3 = 0
		p.S4 = 0
	}
	if i.UseHeaderProtection {
		p.S1 = g.lift(p.S1, maxS)
		p.S2 = g.lift(p.S2, maxS)
		if c.Limits.SupportsS3S4 {
			p.S3 = g.lift(p.S3, s3max)
			p.S4 = g.lift(p.S4, min(32, c.Limits.MaxS4))
		}
	}
	p.S2 = avoidSize(p.S2, maxS, func(n int) bool { return n == p.S1+56 })
	if c.Limits.SupportsS3S4 {
		p.S3 = avoidSize(p.S3, s3max, func(n int) bool { return n == p.S1+84 || n == p.S2+28 })
	}
	if i.UseSameS {
		lo := 1
		if i.UseHeaderProtection {
			lo = 12
		}
		v := g.rnd(lo, min(maxS, c.Limits.MaxS4, 32))
		p.S1 = v
		p.S2 = v
		if c.Limits.SupportsS3S4 {
			p.S3 = v
			p.S4 = v
		}
	}
	p.Jc = i.JunkLevel
	ceiling := min(15, c.Limits.MaxJc)
	if i.UseExtremeMax {
		ceiling = min(128, c.Limits.MaxJc)
	}
	if p.Jc > 0 {
		p.Jc = max(1, min(ceiling, p.Jc+g.rnd(-1, 1)))
	}
	if i.UseExtremeMax && i.JunkLevel == 0 {
		p.Jc = g.rnd(1, min(8, ceiling))
	}
	if i.RouterMode && p.Jc > 0 {
		p.Jc = min(p.Jc, 3, c.Limits.MaxJc)
	}
	jlo := map[string][2]int{"low": {64, 256}, "medium": {128, 512}, "high": {256, 768}}[i.Intensity]
	jhi := map[string][2]int{"low": {256, 512}, "medium": {512, 1024}, "high": {768, 1280}}[i.Intensity]
	if i.RouterMode {
		jlo = [2]int{16, 31}
		jhi = [2]int{96, 128}
	}
	p.Jmin = g.rnd(jlo[0], jlo[1])
	p.Jmax = g.rnd(jhi[0], jhi[1])
	if p.Jmax <= p.Jmin+64 {
		p.Jmax = p.Jmin + 64 + g.rnd(64, 256)
	}
	headers := []*string{&p.H1, &p.H2, &p.H3, &p.H4}
	for n, h := range headers {
		if i.UseDisableH {
			*h = strconv.Itoa(n + 1)
		} else {
			*h = g.header(n)
		}
	}
	iv := map[string]int{"low": 1, "medium": 2, "high": 3}[i.Intensity]
	if i.IterCount > 3 {
		iv++
	}
	chain := make([]string, 5)
	if c.Limits.SupportsI1I5 {
		chain = g.chain(p.Profile, iv, false)
		if chainBytes(chain) > ChainBudget {
			chain = g.chain(p.Profile, iv, true)
		}
		for n := 4; n >= 1 && chainBytes(chain) > ChainBudget; n-- {
			replacement := g.entropy(n, iv)
			if len(replacement) < len(chain[n]) {
				chain[n] = replacement
			}
		}
		for n := 4; n >= 1 && chainBytes(chain) > ChainBudget; n-- {
			chain[n] = ""
		}
		if i.RouterMode {
			for n := 1; n < 5; n++ {
				chain[n] = ""
			}
		}
	} else {
		g.warning("This client does not send I1–I5; CPS mimicry is unavailable.")
	}
	if chainBytes(chain) > ChainBudget {
		return nil, fmt.Errorf("CPS chain exceeds kernel budget")
	}
	p.I1 = chain[0]
	p.I2 = chain[1]
	p.I3 = chain[2]
	p.I4 = chain[3]
	p.I5 = chain[4]
	if i.ProtocolVersion == "3.1" {
		g.awg3(&p)
	}
	if g.err != nil {
		return nil, fmt.Errorf("generate profile: %w", g.err)
	}
	protocol := ProtocolAWG2
	if i.ProtocolVersion == "3.1" {
		protocol = ProtocolAWG3
	}
	if e := Validate(protocol, p.settings()); e != nil {
		return nil, fmt.Errorf("generated profile: %w", e)
	}
	return &Result{Params: p, Profiles: Profiles, ProtocolVersion: i.ProtocolVersion, MTU: i.MTU, ResolvedProfile: p.Profile, Options: g.input, Warnings: g.warnings}, nil
}
func (g *generator) lift(v, ceiling int) int {
	if v < 12 {
		return g.rnd(12, ceiling)
	}
	return v
}
func avoidSize(v, ceiling int, collides func(int) bool) int {
	step := 1
	if v >= ceiling {
		step = -1
	}
	for collides(v) {
		v += step
	}
	return v
}
func (g *generator) header(n int) string {
	lo := []int{100000000, 1200000000, 2400000000, 3600000000}[n]
	hi := []int{900000000, 2000000000, 3200000000, 4000000000}[n]
	spread := 100000000
	if n == 3 {
		spread = 150000000
	}
	if g.input.UseExtremeMax {
		spread = 10000000
		if n == 3 {
			spread = 15000000
		}
	}
	width := 50000
	if g.input.UseNarrowH {
		spread = 20000
		if n == 3 {
			spread = 30000
		}
		width = 20000
	}
	if g.client.Limits.MaxHValue < 4294967295 {
		zone := g.client.Limits.MaxHValue / 5
		lo = zone * (n + 1)
		hi = zone*(n+2) - 10000
		if n == 3 {
			hi = g.client.Limits.MaxHValue
		}
		spread = min(spread, zone-10000)
	}
	base := g.rnd(lo, max(lo, hi-spread-width))
	w := g.rnd(1000, width)
	start := min(base+g.rnd(0, spread), g.client.Limits.MaxHValue-w)
	return rangeText(start, start+w)
}
func (g *generator) awg3(p *Params) {
	i := g.input
	if i.UseHeaderProtection {
		p.HeaderProtectionKey = base64.StdEncoding.EncodeToString(g.bytes(32))
	}
	if i.UseContentPadding {
		r := map[string][2]int{"low": {8, 64}, "medium": {16, 128}, "high": {24, 200}}[i.Intensity]
		if i.RouterMode {
			p.ContentPaddingAddition = "4-32"
		} else {
			lo := g.rnd(r[0], (r[0]+r[1])/2)
			p.ContentPaddingAddition = rangeText(lo, g.rnd(lo+8, r[1]))
		}
	}
	if i.UseRandomTimings {
		j := map[string]int{"low": 10, "medium": 25, "high": 45}[i.Intensity]
		rt := g.rnd(4, 6)
		rth := rt + g.rnd(1, 4)
		ka := g.rnd(8, 14)
		kah := ka + g.rnd(2, 8)
		ra := g.rnd(100, 120)
		rah := ra + g.rnd(10, j)
		reject := max(170, rah+kah+rth+15)
		attempts := g.rnd(12, 18)
		p.RekeyTimeout = rangeText(rt, rth)
		p.KeepaliveTimeout = rangeText(ka, kah)
		p.RekeyAfterTime = rangeText(ra, rah)
		p.RejectAfterTime = rangeText(reject, reject+g.rnd(10, j))
		p.MaxHandshakeAttempts = rangeText(attempts, attempts+g.rnd(2, 10))
	}
	p.RandomTrailers = &i.UseRandomTrailers
	p.DisableCookies = &i.UseDisableCookies
}
func (p Params) settings() *peer.AWGSettings {
	return &peer.AWGSettings{Jc: p.Jc, Jmin: p.Jmin, Jmax: p.Jmax, S1: p.S1, S2: p.S2, S3: p.S3, S4: p.S4, H1: p.H1, H2: p.H2, H3: p.H3, H4: p.H4, I1: p.I1, I2: p.I2, I3: p.I3, I4: p.I4, I5: p.I5, HeaderProtectionKey: p.HeaderProtectionKey, ContentPaddingAddition: p.ContentPaddingAddition, RekeyAfterTime: p.RekeyAfterTime, RekeyTimeout: p.RekeyTimeout, RejectAfterTime: p.RejectAfterTime, KeepaliveTimeout: p.KeepaliveTimeout, MaxHandshakeAttempts: p.MaxHandshakeAttempts, RandomTrailers: p.RandomTrailers, DisableCookies: p.DisableCookies}
}
