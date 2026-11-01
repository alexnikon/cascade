package awgparams

import (
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

// Packet builders follow Architect v4.5.1. Length fields include dynamic CPS tags.
func (g *generator) chain(profile string, iv int, compact bool) []string {
	out := make([]string, 5)
	for n := range out {
		switch {
		case profile == "tls_to_quic" && n == 0:
			out[n] = g.tls(iv)
		case profile == "tls_to_quic" && n == 1:
			out[n] = g.quic("quic_initial", iv)
		case profile == "quic_burst" && n < 3:
			out[n] = g.quic([]string{"quic_initial", "quic_0rtt", "http3"}[n], iv)
		case profile == "tls_to_quic" || profile == "quic_burst":
			out[n] = g.entropy(n, iv)
		case n > 0 && !g.input.MimicAll:
			out[n] = g.entropy(n, iv)
		case profile == "dns_query":
			out[n] = g.dns(iv + n)
		case profile == "stun":
			variant := "allocate_auth"
			if g.input.MimicAll {
				variant = []string{"binding", "allocate", "allocate_auth", "ice_check", "ice_check"}[n]
			}
			out[n] = g.stun(variant)
		case profile == "tls_client_hello":
			out[n] = g.tls(iv)
		case profile == "dtls_1_2" || profile == "dtls_1_3":
			out[n] = g.dtls(profile, iv)
		case profile == "wireguard_noise":
			out[n] = g.noise(iv)
		case profile == "sip":
			out[n] = g.sip(iv, compact)
		default:
			out[n] = g.quic(profile, iv)
		}
	}
	return out
}
func (g *generator) quic(profile string, iv int) string {
	i := g.input
	host := g.host("quic", "")
	dcid, scid := g.rnd(8, 20), g.rnd(0, 20)
	pn := g.rnd(1, 4)
	initial := profile == "quic_initial" || profile == "http3" && g.rnd(0, 1) == 0
	token := 0
	if initial && g.rnd(0, 1) == 1 {
		token = g.rnd(8, 32)
	}
	first := 0xc0
	slot := "qi"
	rc := min(len(host)+g.rnd(0, 6), 64)
	if profile == "quic_0rtt" {
		first = 0xd0
		slot = "q0"
		rc = min(len(host)+g.rnd(4, 16), 48)
	}
	if profile == "http3" {
		slot = "h3"
		rc = min(len(host)+9+g.rnd(0, 6), 64)
		if !initial {
			first = 0xe0
		}
	}
	prefix := hx(first|(pn-1), 1) + "00000001" + hx(dcid, 1) + g.rh(dcid) + hx(scid, 1) + g.rh(scid)
	if initial {
		prefix += qvar(token) + g.rh(token)
	}
	extra := flagBytes(i.UseTagRC, rc) + flagBytes(i.UseTagC, 4) + flagBytes(i.UseTagT, 4)
	pad := g.padding(len(prefix)/2+2+pn, extra, slot, iv)
	sent := flagBytes(i.UseTagR, pad)
	h := blob(prefix + qvar(pn+sent+extra) + g.rh(pn))
	r := g.tag("r", pad, i.UseTagR)
	rcs := g.tag("rc", rc, i.UseTagRC)
	c := g.tag("c", 0, i.UseTagC)
	t := g.tag("t", 0, i.UseTagT)
	if profile == "quic_0rtt" {
		return h + t + r + rcs + c
	}
	if profile == "http3" {
		return h + rcs + r + c + t
	}
	return h + rcs + c + t + r
}
func (g *generator) tls(iv int) string {
	i := g.input
	host := g.host("tls", "")
	rc := min(9+len(host), 64)
	base := g.rnd(300, 550)
	if i.UseBrowserFp {
		if r, ok := g.browser().Sizes["tls"]; ok {
			base = g.rnd(r[0], r[1])
		}
	}
	if g.browser().Family == "chromium" {
		base = ((base + 127) / 128) * 128
	}
	extra := flagBytes(i.UseTagRC, rc) + flagBytes(i.UseTagC, 4) + flagBytes(i.UseTagT, 4)
	fixed := 5 + 38 + extra
	pad := 0
	if i.UseTagR {
		pad = max(0, min(base-fixed, i.MTU-fixed, g.rnd(20, 60)*iv, 300))
	}
	record := 38 + extra + pad
	return blob("160301"+hx(record, 2)+"01"+hx(record-4, 3)+"0303"+g.rh(32)) + g.tag("rc", rc, i.UseTagRC) + g.tag("r", pad, i.UseTagR) + g.tag("c", 0, i.UseTagC) + g.tag("t", 0, i.UseTagT)
}
func (g *generator) dtls(profile string, iv int) string {
	i := g.input
	body := "fefd" + g.rh(32)
	ext := ""
	if profile == "dtls_1_3" {
		body += "000000061301130213030100"
		ext = "002b000302fefc"
	}
	host := g.host("dtls", "")
	rc := min(len(host)+g.rnd(2, 8), 60)
	extra := flagBytes(i.UseTagRC, rc) + flagBytes(i.UseTagC, 4) + flagBytes(i.UseTagT, 4)
	fixed := 13 + 12 + len(body)/2
	if ext != "" {
		fixed += len(ext)/2 + 6
	}
	pad := 0
	if i.UseTagR {
		pad = g.padding(fixed, extra, "dtls", iv)
	}
	trail := extra + pad
	if ext != "" {
		grease := ""
		if trail > 0 {
			n := g.rnd(0, 15)
			grease = fmt.Sprintf("%xa%xa", n, n) + hx(trail, 2)
		}
		body += hx(len(ext)/2+len(grease)/2+trail, 2) + ext + grease
	}
	bodylen := len(body)/2 + trail
	return blob("16fefd0000"+hx(0, 4)+hx(g.rnd(0, 4), 2)+hx(12+bodylen, 2)+"01"+hx(bodylen, 3)+"0000000000"+hx(bodylen, 3)+body) + g.tag("rc", rc, i.UseTagRC) + g.tag("c", 0, i.UseTagC) + g.tag("t", 0, i.UseTagT) + g.tag("r", pad, i.UseTagR)
}
func (g *generator) noise(iv int) string {
	i := g.input
	rc := g.rnd(4, 12)
	extra := flagBytes(i.UseTagRC, rc) + flagBytes(i.UseTagC, 4) + flagBytes(i.UseTagT, 4)
	pad := min(g.rnd(10, 40)*iv, 200, max(0, i.MTU-148-extra))
	if i.UseBrowserFp {
		pad = g.padding(148, extra, "nx", iv)
	}
	return blob("01000000"+g.rh(4)) + blob(g.rh(32)) + blob(g.rh(48)) + blob(g.rh(28)) + blob(g.rh(32)) + g.tag("r", pad, i.UseTagR) + g.tag("t", 0, i.UseTagT) + g.tag("rc", rc, i.UseTagRC)
}
func (g *generator) entropy(idx, iv int) string {
	i := g.input
	big := g.rnd(1, 10) > 6
	base := g.rnd(4, 20)
	ceiling := 60
	if big {
		base = g.rnd(200, 500)
		ceiling = 500
	}
	rlen := min(base*iv, ceiling, max(0, i.MTU-20-flagBytes(i.UseTagC, 4)-flagBytes(i.UseTagT, 4)))
	rcLen, rdLen := g.rnd(4, 12), g.rnd(4, 8)
	c, t := g.tag("c", 0, i.UseTagC), g.tag("t", 0, i.UseTagT)
	r, rc, rd := g.tag("r", rlen, i.UseTagR), g.tag("rc", rcLen, i.UseTagRC), g.tag("rd", rdLen, i.UseTagRD)
	b, b2 := "", ""
	if iv >= 2 {
		b = blob(g.rh(g.rnd(4, 8*iv)))
	}
	if iv >= 3 {
		b2 = blob(g.rh(g.rnd(2, 4)))
	}
	patterns := []string{b + r + t + rc + c + rd, c + t + b + r + rc + rd, rc + b + r + c + t + rd, t + r + c + rc + b + rd, r + rc + b + t + c + rd, b2 + t + r + b + rc + c + rd, rd + b + rc + r + t + c + b2, c + b + b2 + t + rc + r + rd}
	s := patterns[(idx+g.rnd(0, len(patterns)-1))%len(patterns)]
	if s == "" {
		s = "<r 10>"
	}
	return s
}
func (g *generator) dns(iv int) string {
	i := g.input
	kind, qtype := "A", "0001"
	if iv%2 != 0 {
		kind = "AAAA"
		qtype = "001c"
	}
	host := g.host("dns", kind)
	name := ""
	for _, label := range strings.Split(host, ".") {
		if label != "" {
			name += hx(len(label), 1) + hex.EncodeToString([]byte(label))
		}
	}
	question := name + "00" + qtype + "0001"
	ceiling := min(512, i.MTU-20)
	wanted := max(0, ceiling)
	if ceiling > 64 {
		wanted = g.rnd(64, ceiling)
	}
	tags := flagBytes(i.UseTagT, 4) + flagBytes(i.UseTagC, 4)
	room := i.MTU - 12 - len(question)/2 - 11 - 4 - tags
	pad := 0
	if i.UseTagR {
		pad = max(0, min(wanted-12-len(question)/2, room, 200))
	}
	data := pad + tags
	ar, opt := "0000", ""
	if data > 0 {
		ar = "0001"
		opt = "00002904d000000000" + hx(4+data, 2) + "000c" + hx(data, 2)
	}
	return blob(g.rh(2)+"0100000100000000"+ar+question+opt) + g.tag("r", pad, pad > 0) + g.tag("t", 0, i.UseTagT) + g.tag("c", 0, i.UseTagC)
}
func (g *generator) sip(iv int, compact bool) string {
	i := g.input
	host := g.host("sip", "")
	user := fmt.Sprintf("user%d", g.rnd(1000, 9999))
	rc := min(len(host)+g.rnd(8, 24)*iv, 150)
	tags := flagBytes(i.UseTagRC, rc) + flagBytes(i.UseTagC, 4) + flagBytes(i.UseTagT, 4)
	branch, tag, call := 7, 5, 10
	via, from, to, id, length := "Via", "From", "To", "Call-ID", "Content-Length"
	if compact {
		branch, tag, call = 5, 4, 8
		via, from, to, id, length = "v", "f", "t", "i", "l"
	}
	headers := []string{"REGISTER sip:" + host + " SIP/2.0", via + ": SIP/2.0/UDP " + host + ":5060;branch=z9hG4bK" + g.rh(branch) + ";rport", "Max-Forwards: 70", from + ": <sip:" + user + "@" + host + ">;tag=" + g.rh(tag), to + ": <sip:" + user + "@" + host + ">", id + ": " + g.rh(call) + "@" + host, fmt.Sprintf("CSeq: %d REGISTER", g.rnd(1, 9999))}
	if !compact {
		agent := "PJSUA v2.13"
		if i.MimicAll {
			agent = "Linphone/5.2.5"
		}
		headers = append(headers, "Contact: <sip:"+user+"@"+host+":5060>;expires=3600", "User-Agent: "+agent)
	}
	prefix := strings.Join(headers, "\r\n") + "\r\n"
	room := max(0, i.MTU-len(prefix)-tags-len("Content-Length: 9999\r\n\r\n"))
	pad := 0
	if i.UseTagR {
		pad = min(g.rnd(5, 30)*iv, 120, room)
	}
	message := prefix + fmt.Sprintf("%s: %d\r\n\r\n", length, pad+tags)
	return blob(hex.EncodeToString([]byte(message))) + g.tag("rc", rc, i.UseTagRC) + g.tag("c", 0, i.UseTagC) + g.tag("t", 0, i.UseTagT) + g.tag("r", pad, i.UseTagR)
}

// STUN attributes retain their alignment and FINGERPRINT only for static chains.
type stunItem struct {
	hex, tag string
	size     int
}
type stunChain []stunItem

func (c stunChain) size() int {
	n := 0
	for _, i := range c {
		n += len(i.hex)/2 + i.size
	}
	return n
}
func (c stunChain) static() bool {
	for _, i := range c {
		if i.tag != "" {
			return false
		}
	}
	return true
}
func (c stunChain) render() string {
	s, pending := "", ""
	for _, i := range c {
		if i.tag == "" {
			pending += i.hex
		} else {
			if pending != "" {
				s += blob(pending)
				pending = ""
			}
			s += i.tag
		}
	}
	if pending != "" {
		s += blob(pending)
	}
	return s
}
func (c stunChain) raw() string {
	s := ""
	for _, i := range c {
		s += i.hex
	}
	return s
}
func stHex(s string) stunChain   { return stunChain{{hex: s}} }
func stASCII(s string) stunChain { return stHex(hex.EncodeToString([]byte(s))) }
func stAttr(kind int, c stunChain) stunChain {
	n := c.size()
	out := append(stHex(hx(kind, 2)+hx(n, 2)), c...)
	return append(out, stHex(strings.Repeat("00", (4-n%4)%4))...)
}
func (g *generator) stRandom(n int, letters bool) stunChain {
	if letters && g.input.UseTagRC {
		return stunChain{{tag: g.tag("rc", n, true), size: n}}
	}
	if !letters && g.input.UseTagR {
		return stunChain{{tag: g.tag("r", n, true), size: n}}
	}
	if !letters {
		return stHex(g.rh(n))
	}
	const abc = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	s := ""
	for range n {
		s += string(abc[g.rnd(0, len(abc)-1)])
	}
	return stASCII(s)
}
func (g *generator) stun(variant string) string {
	kind := "0001"
	attrs := stunChain{}
	if variant == "allocate" || variant == "allocate_auth" {
		kind = "0003"
		attrs = append(attrs, stAttr(0x19, stHex("11000000"))...)
	}
	if variant == "allocate_auth" {
		host := g.host("stun", "")
		expiry := g.now().Unix() + int64(g.rnd(3600, 86400))
		user := stASCII(strconv.FormatInt(expiry, 10))
		if g.input.UseTagRD {
			user = stunChain{{tag: "<rd 10>", size: 10}}
		}
		user = append(user, stASCII(":")...)
		user = append(user, g.stRandom(8, true)...)
		attrs = append(attrs, stAttr(6, user)...)
		attrs = append(attrs, stAttr(0x14, stASCII(host))...)
		attrs = append(attrs, stAttr(0x15, g.stRandom(16, true))...)
		attrs = append(attrs, stAttr(8, g.stRandom(20, false))...)
	}
	if variant == "ice_check" {
		ufrag := 8
		if g.input.Browser == "" || g.browser().Family == "chromium" {
			ufrag = 4
		}
		user := append(g.stRandom(ufrag, true), stASCII(":")...)
		user = append(user, g.stRandom(ufrag, true)...)
		attrs = append(attrs, stAttr(6, user)...)
		attrs = append(attrs, stAttr(0x802a, g.stRandom(8, false))...)
		if g.rnd(0, 1) == 1 {
			attrs = append(attrs, stAttr(0x25, nil)...)
		}
		priority := 110*(1<<24) + g.rnd(0x1e00, 0xffff)*256 + 255
		attrs = append(attrs, stAttr(0x24, stHex(hx(priority, 4)))...)
		attrs = append(attrs, stAttr(8, g.stRandom(20, false))...)
	}
	tid := g.stRandom(12, false)
	fingerprint := attrs.static() && tid.static()
	length := attrs.size()
	if fingerprint {
		length += 8
	}
	message := append(stHex(kind+hx(length, 2)+"2112a442"), tid...)
	message = append(message, attrs...)
	if fingerprint {
		raw, _ := hex.DecodeString(message.raw())
		crc := crc32.ChecksumIEEE(raw) ^ 0x5354554e
		message = append(message, stAttr(0x8028, stHex(hx(int(crc), 4)))...)
	}
	return message.render()
}
