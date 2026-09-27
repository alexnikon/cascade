package dnsalias

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// ── Test DNS server ───────────────────────────────────────────────────────────

// testDNS is a scriptable authoritative-ish DNS server bound to a loopback port.
// Tests register records per (name, qtype) so resolution is deterministic and
// never touches the public DNS.
type testDNS struct {
	addr string
	srv  *dns.Server

	mu      sync.Mutex
	records map[string][]dns.RR // "name./TYPE" → answer section
	rcode   map[string]int      // "name./TYPE" → forced rcode
	fail    bool                // when true every query is dropped (simulates an outage)
	queries int
}

func newTestDNS(t *testing.T) *testDNS {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	d := &testDNS{
		addr:    pc.LocalAddr().String(),
		records: map[string][]dns.RR{},
		rcode:   map[string]int{},
	}
	d.srv = &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(d.handle)}

	started := make(chan struct{})
	d.srv.NotifyStartedFunc = func() { close(started) }
	go d.srv.ActivateAndServe() //nolint:errcheck
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("test DNS server did not start")
	}
	t.Cleanup(func() { d.srv.Shutdown() }) //nolint:errcheck
	return d
}

func (d *testDNS) handle(w dns.ResponseWriter, req *dns.Msg) {
	d.mu.Lock()
	d.queries++
	if d.fail {
		d.mu.Unlock()
		return // no response at all → the client times out
	}
	q := req.Question[0]
	key := q.Name + "/" + dns.TypeToString[q.Qtype]
	answers := d.records[key]
	rcode, forced := d.rcode[key]
	d.mu.Unlock()

	m := new(dns.Msg)
	m.SetReply(req)
	if forced {
		m.Rcode = rcode
	} else {
		m.Answer = answers
	}
	w.WriteMsg(m) //nolint:errcheck
}

func (d *testDNS) set(name string, qtype uint16, rrs ...dns.RR) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.records[dns.Fqdn(name)+"/"+dns.TypeToString[qtype]] = rrs
}

func (d *testDNS) setRcode(name string, qtype uint16, rcode int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rcode[dns.Fqdn(name)+"/"+dns.TypeToString[qtype]] = rcode
}

func (d *testDNS) setFailing(v bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = v
}

func (d *testDNS) queryCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queries
}

// testClient builds a dnsClient with short timeouts so tests that exercise an
// unresponsive server finish quickly instead of waiting out QueryTimeout.
func testClient(servers ...string) *dnsClient {
	c := newDNSClient(servers)
	c.udp.Timeout = 300 * time.Millisecond
	c.tcp.Timeout = 300 * time.Millisecond
	return c
}

// ── RR helpers ────────────────────────────────────────────────────────────────

func mustRR(t *testing.T, s string) dns.RR {
	t.Helper()
	rr, err := dns.NewRR(s)
	if err != nil {
		t.Fatalf("NewRR(%q): %v", s, err)
	}
	return rr
}

func aRR(t *testing.T, name string, ttl uint32, ip string) dns.RR {
	return mustRR(t, fmt.Sprintf("%s %d IN A %s", dns.Fqdn(name), ttl, ip))
}

func aaaaRR(t *testing.T, name string, ttl uint32, ip string) dns.RR {
	return mustRR(t, fmt.Sprintf("%s %d IN AAAA %s", dns.Fqdn(name), ttl, ip))
}

func cnameRR(t *testing.T, name string, ttl uint32, target string) dns.RR {
	return mustRR(t, fmt.Sprintf("%s %d IN CNAME %s", dns.Fqdn(name), ttl, dns.Fqdn(target)))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestLookup_ARecords(t *testing.T) {
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA,
		aRR(t, "example.com", 300, "93.184.216.34"),
		aRR(t, "example.com", 300, "93.184.216.35"),
	)

	res, err := testClient(d.addr).lookup("example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(res.V4) != 2 {
		t.Fatalf("want 2 IPv4 addresses, got %d (%v)", len(res.V4), res.V4)
	}
	if ttl := res.V4["93.184.216.34"]; ttl != 300 {
		t.Errorf("TTL = %d, want 300", ttl)
	}
	if len(res.V6) != 0 {
		t.Errorf("want no IPv6 addresses, got %v", res.V6)
	}
}

func TestLookup_AAAARecords(t *testing.T) {
	d := newTestDNS(t)
	d.set("v6.example.com", dns.TypeAAAA,
		aaaaRR(t, "v6.example.com", 600, "2606:2800:220:1:248:1893:25c8:1946"),
	)

	res, err := testClient(d.addr).lookup("v6.example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(res.V6) != 1 {
		t.Fatalf("want 1 IPv6 address, got %v", res.V6)
	}
	if ttl := res.V6["2606:2800:220:1:248:1893:25c8:1946"]; ttl != 600 {
		t.Errorf("TTL = %d, want 600", ttl)
	}
	if len(res.V4) != 0 {
		t.Errorf("want no IPv4 addresses, got %v", res.V4)
	}
}

// A resolver that flattens the chain returns the CNAMEs and the final addresses
// in one answer section. Every address record must be taken, even though its
// owner name differs from the name we asked for.
func TestLookup_CNAMEFlattenedInAnswer(t *testing.T) {
	d := newTestDNS(t)
	d.set("www.example.com", dns.TypeA,
		cnameRR(t, "www.example.com", 300, "cdn.example.net"),
		aRR(t, "cdn.example.net", 120, "203.0.113.10"),
	)

	res, err := testClient(d.addr).lookup("www.example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if ttl, ok := res.V4["203.0.113.10"]; !ok || ttl != 120 {
		t.Fatalf("want 203.0.113.10 with TTL 120, got %v", res.V4)
	}
}

// When the answer carries only a CNAME, the target must be chased explicitly.
func TestLookup_CNAMEChainFollowed(t *testing.T) {
	d := newTestDNS(t)
	d.set("a.example.com", dns.TypeA, cnameRR(t, "a.example.com", 300, "b.example.com"))
	d.set("b.example.com", dns.TypeA, cnameRR(t, "b.example.com", 300, "c.example.com"))
	d.set("c.example.com", dns.TypeA, aRR(t, "c.example.com", 300, "198.51.100.7"))

	res, err := testClient(d.addr).lookup("a.example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if _, ok := res.V4["198.51.100.7"]; !ok {
		t.Fatalf("want 198.51.100.7 via CNAME chain, got %v", res.V4)
	}
}

func TestLookup_CNAMELoopIsBounded(t *testing.T) {
	d := newTestDNS(t)
	d.set("loop.example.com", dns.TypeA, cnameRR(t, "loop.example.com", 300, "loop.example.com"))

	// Must terminate rather than recurse forever.
	done := make(chan struct{})
	go func() {
		defer close(done)
		testClient(d.addr).lookup("loop.example.com") //nolint:errcheck
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("CNAME loop was not bounded")
	}
}

// The same address arriving from several records/names must collapse to one
// entry, keeping the longest TTL so the ipset entry lives as long as permitted.
func TestLookup_DuplicateAddressesDeduplicated(t *testing.T) {
	d := newTestDNS(t)
	d.set("dup.example.com", dns.TypeA,
		aRR(t, "dup.example.com", 100, "203.0.113.1"),
		aRR(t, "dup.example.com", 900, "203.0.113.1"),
		aRR(t, "dup.example.com", 300, "203.0.113.1"),
	)

	res, err := testClient(d.addr).lookup("dup.example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(res.V4) != 1 {
		t.Fatalf("want 1 deduplicated address, got %v", res.V4)
	}
	if ttl := res.V4["203.0.113.1"]; ttl != 900 {
		t.Errorf("TTL = %d, want the longest (900)", ttl)
	}
}

func TestLookup_MergeAcrossDomainsDeduplicates(t *testing.T) {
	d := newTestDNS(t)
	d.set("one.example.com", dns.TypeA, aRR(t, "one.example.com", 300, "203.0.113.5"))
	d.set("two.example.com", dns.TypeA, aRR(t, "two.example.com", 600, "203.0.113.5"))

	c := testClient(d.addr)
	merged := newLookupResult()
	for _, name := range []string{"one.example.com", "two.example.com"} {
		res, err := c.lookup(name)
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		merged.merge(res)
	}
	if len(merged.V4) != 1 {
		t.Fatalf("want 1 shared address, got %v", merged.V4)
	}
	if ttl := merged.V4["203.0.113.5"]; ttl != 600 {
		t.Errorf("merged TTL = %d, want 600", ttl)
	}
}

func TestLookup_ServfailIsAnError(t *testing.T) {
	d := newTestDNS(t)
	d.setRcode("broken.example.com", dns.TypeA, dns.RcodeServerFailure)
	d.setRcode("broken.example.com", dns.TypeAAAA, dns.RcodeServerFailure)

	if _, err := testClient(d.addr).lookup("broken.example.com"); err == nil {
		t.Fatal("want an error for SERVFAIL, got nil")
	}
}

// A name with an A record but no AAAA record is completely normal and must not
// be reported as a failure.
func TestLookup_MissingAAAAIsNotAnError(t *testing.T) {
	d := newTestDNS(t)
	d.set("v4only.example.com", dns.TypeA, aRR(t, "v4only.example.com", 300, "203.0.113.9"))
	d.setRcode("v4only.example.com", dns.TypeAAAA, dns.RcodeNameError)

	res, err := testClient(d.addr).lookup("v4only.example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(res.V4) != 1 || len(res.V6) != 0 {
		t.Fatalf("want 1 v4 and 0 v6, got v4=%v v6=%v", res.V4, res.V6)
	}
}

func TestLookup_FailsOverToSecondServer(t *testing.T) {
	dead := newTestDNS(t)
	dead.setFailing(true)
	good := newTestDNS(t)
	good.set("ha.example.com", dns.TypeA, aRR(t, "ha.example.com", 300, "203.0.113.20"))

	res, err := testClient(dead.addr, good.addr).lookup("ha.example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if _, ok := res.V4["203.0.113.20"]; !ok {
		t.Fatalf("want the answer from the healthy server, got %v", res.V4)
	}
}

// ── TTL clamping ──────────────────────────────────────────────────────────────

func TestClampTTL(t *testing.T) {
	cases := []struct {
		name string
		in   uint32
		want int
	}{
		{"below minimum is raised", 5, 60},
		{"exactly the minimum", 60, 60},
		{"in range passes through", 300, 300},
		{"exactly the maximum", 3600, 3600},
		{"above maximum is capped", 86400, 3600},
		{"zero becomes the default", 0, 300},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampTTL(tt.in); got != tt.want {
				t.Errorf("clampTTL(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestLookup_TTLClampedAtBothEnds(t *testing.T) {
	d := newTestDNS(t)
	d.set("ttl.example.com", dns.TypeA,
		aRR(t, "ttl.example.com", 5, "203.0.113.30"),     // below MinTTL
		aRR(t, "ttl.example.com", 86400, "203.0.113.31"), // above MaxTTL
	)

	res, err := testClient(d.addr).lookup("ttl.example.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got := res.V4["203.0.113.30"]; got != int(MinTTL.Seconds()) {
		t.Errorf("short TTL = %d, want clamped to %d", got, int(MinTTL.Seconds()))
	}
	if got := res.V4["203.0.113.31"]; got != int(MaxTTL.Seconds()) {
		t.Errorf("long TTL = %d, want clamped to %d", got, int(MaxTTL.Seconds()))
	}
}

func TestMinTTL(t *testing.T) {
	r := newLookupResult()
	if got := r.minTTL(); got != 0 {
		t.Errorf("empty minTTL = %d, want 0", got)
	}
	r.V4["203.0.113.1"] = 600
	r.V6["2001:db8::1"] = 120
	if got := r.minTTL(); got != 120 {
		t.Errorf("minTTL = %d, want 120 (shortest across both families)", got)
	}
}

// ── Backoff ───────────────────────────────────────────────────────────────────

func TestBackoff_GrowsAndIsCapped(t *testing.T) {
	var prev time.Duration
	for failures := 1; failures <= 12; failures++ {
		d := backoff(failures)
		if d > RetryMax {
			t.Fatalf("backoff(%d) = %s, exceeds RetryMax %s", failures, d, RetryMax)
		}
		if failures > 1 && d < prev && prev < RetryMax/2 {
			t.Errorf("backoff(%d) = %s shrank from %s", failures, d, prev)
		}
		prev = d
	}
	// Well past the cap it must have settled at RetryMax (±10% jitter).
	d := backoff(50)
	if d < RetryMax-RetryMax/5 || d > RetryMax {
		t.Errorf("backoff(50) = %s, want ~RetryMax (%s)", d, RetryMax)
	}
}

func TestRefreshInterval_RenewsBeforeExpiry(t *testing.T) {
	// A refresh must be scheduled strictly before the entry's timeout elapses,
	// otherwise the kernel drops the address between rounds.
	for _, ttl := range []int{60, 120, 300, 3600} {
		got := refreshInterval(ttl)
		if got >= time.Duration(ttl)*time.Second {
			t.Errorf("refreshInterval(%d) = %s, must be shorter than the TTL", ttl, got)
		}
		if got <= 0 {
			t.Errorf("refreshInterval(%d) = %s, must be positive", ttl, got)
		}
	}
	if got := refreshInterval(0); got != DefaultTTL {
		t.Errorf("refreshInterval(0) = %s, want DefaultTTL %s", got, DefaultTTL)
	}
}
