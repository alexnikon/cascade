package dnsalias

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/miekg/dns"
)

func proxyHarness(t *testing.T) (*Resolver, *fakeIPSet, *testDNS, string) {
	t.Helper()
	am := initTestDB(t)
	f := newFakeIPSet()
	upstream := newTestDNS(t)
	r := newTestResolver(t, am, f, upstream.addr)
	r.Start()
	if err := r.configureProxy("wg12", "127.0.0.1:0", func(ip net.IP) bool { return ip.IsLoopback() }); err != nil {
		t.Fatal(err)
	}
	// Port zero is test-only: the production entry point always uses port 53.
	r.proxyMu.Lock()
	addr := r.proxies["wg12"].udp.PacketConn.LocalAddr().String()
	r.proxyMu.Unlock()
	return r, f, upstream, addr
}

func queryProxy(t *testing.T, address, name string, typ uint16) *dns.Msg {
	t.Helper()
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(name), typ)
	resp, _, err := (&dns.Client{Timeout: time.Second}).Exchange(req, address)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestProxyLearnsBeforeReplyForEveryMatchingAlias(t *testing.T) {
	r, f, upstream, addr := proxyHarness(t)
	a := createDomainAlias(t, r.am, "Video", "*.googlevideo.com")
	b := createDomainAlias(t, r.am, "CDN", "*.example.net")
	r.Sync(a.ID)
	r.Sync(b.ID)
	upstream.set("r1.googlevideo.com", dns.TypeA,
		cnameRR(t, "r1.googlevideo.com", 300, "cdn.example.net"),
		aRR(t, "cdn.example.net", 300, "192.0.2.42"),
		aRR(t, "unrelated.example.org", 300, "192.0.2.99"))
	resp := queryProxy(t, addr, "r1.googlevideo.com", dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 3 {
		t.Fatalf("response changed: %v", resp)
	}
	for _, alias := range []*aliases.Alias{a, b} {
		got := f.snapshot(aliases.DomainSetV4(alias.ID))
		if got["192.0.2.42"] != 300 || got["192.0.2.99"] != 0 {
			t.Fatalf("learned entries: %v", got)
		}
	}
	upstream.set("r1.googlevideo.com", dns.TypeAAAA, aaaaRR(t, "r1.googlevideo.com", 2, "2001:db8::42"))
	queryProxy(t, addr, "r1.googlevideo.com", dns.TypeAAAA)
	if got := f.snapshot(aliases.DomainSetV6(a.ID))["2001:db8::42"]; got != int(MinTTL.Seconds()) {
		t.Fatalf("TTL=%d", got)
	}
}

func TestProxyDoesNotLearnApexOrSimilarNames(t *testing.T) {
	r, f, upstream, addr := proxyHarness(t)
	a := createDomainAlias(t, r.am, "Video", "*.googlevideo.com")
	r.Sync(a.ID)
	for _, name := range []string{"googlevideo.com", "notgooglevideo.com", "googlevideo.com.attacker.net"} {
		upstream.set(name, dns.TypeA, aRR(t, name, 300, "192.0.2.42"))
		queryProxy(t, addr, name, dns.TypeA)
	}
	if len(f.snapshot(aliases.DomainSetV4(a.ID))) != 0 {
		t.Fatal("unmatched names learned")
	}
}

func TestProxyPreservesNegativeResponsesAndFailsOnWrite(t *testing.T) {
	r, f, upstream, addr := proxyHarness(t)
	a := createDomainAlias(t, r.am, "Video", "*.googlevideo.com")
	r.Sync(a.ID)
	for _, code := range []int{dns.RcodeNameError, dns.RcodeServerFailure} {
		upstream.setRcode("missing.googlevideo.com", dns.TypeA, code)
		if got := queryProxy(t, addr, "missing.googlevideo.com", dns.TypeA).Rcode; got != code {
			t.Fatalf("rcode=%d want %d", got, code)
		}
	}
	upstream.set("r1.googlevideo.com", dns.TypeA, aRR(t, "r1.googlevideo.com", 300, "192.0.2.42"))
	f.mu.Lock()
	f.addErr = errors.New("ipset write failed")
	f.mu.Unlock()
	if got := queryProxy(t, addr, "r1.googlevideo.com", dns.TypeA).Rcode; got != dns.RcodeServerFailure {
		t.Fatalf("rcode=%d", got)
	}
}

func TestProxyRefusesUnknownClients(t *testing.T) {
	r, _, upstream, _ := proxyHarness(t)
	if err := r.configureProxy("deny", "127.0.0.1:0", func(net.IP) bool { return false }); err != nil {
		t.Fatal(err)
	}
	addr := r.proxies["deny"].udp.PacketConn.LocalAddr().String()
	if got := queryProxy(t, addr, "example.com", dns.TypeA).Rcode; got != dns.RcodeRefused {
		t.Fatalf("rcode=%d", got)
	}
	if upstream.queryCount() != 0 {
		t.Fatal("unauthorized query forwarded")
	}
}

func TestProxyConflictAndRemoval(t *testing.T) {
	r, _, _, _ := proxyHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := r.configureProxy("conflict", ln.Addr().String(), func(net.IP) bool { return true }); err == nil {
		t.Fatal("expected TCP conflict")
	}
	if s := r.ProxyStatus()["conflict"]; s.Ready || s.Error == "" {
		t.Fatalf("status=%+v", s)
	}
	// The UDP socket must have been released after the TCP bind failure.
	pc, err := net.ListenPacket("udp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	pc.Close()
	p := r.proxies["wg12"]
	udpAddr := p.udp.PacketConn.LocalAddr().String()
	tcpAddr := p.tcp.Listener.Addr().String()
	r.RemoveInterface("wg12")
	pc, err = net.ListenPacket("udp", udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	pc.Close()
	listener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if _, ok := r.ProxyStatus()["wg12"]; ok {
		t.Fatal("removed listener still reported")
	}
}

func TestProxyUpstreamFailure(t *testing.T) {
	_, _, upstream, addr := proxyHarness(t)
	upstream.setFailing(true)
	if got := queryProxy(t, addr, "example.com", dns.TypeA).Rcode; got != dns.RcodeServerFailure {
		t.Fatalf("rcode=%d", got)
	}
}

func TestProxyTCPFallbackAndEDNS(t *testing.T) {
	r, f, _, proxyAddr := proxyHarness(t)
	a := createDomainAlias(t, r.am, "Video", "*.googlevideo.com")
	r.Sync(a.ID)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	observed := make(chan bool, 8)
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		observed <- req.CheckingDisabled && req.IsEdns0() != nil && req.IsEdns0().Do()
		resp := new(dns.Msg)
		resp.SetReply(req)
		resp.AuthenticatedData = true
		if w.RemoteAddr().Network() == "udp" {
			resp.Truncated = true
		} else {
			resp.Answer = []dns.RR{aRR(t, "r1.googlevideo.com", 300, "192.0.2.43")}
		}
		resp.SetEdns0(1232, true)
		w.WriteMsg(resp)
	})
	ready := make(chan struct{}, 2)
	udp := &dns.Server{PacketConn: pc, Handler: handler, NotifyStartedFunc: func() { ready <- struct{}{} }}
	tcp := &dns.Server{Listener: ln, Handler: handler, NotifyStartedFunc: func() { ready <- struct{}{} }}
	go udp.ActivateAndServe()
	go tcp.ActivateAndServe()
	<-ready
	<-ready
	defer udp.Shutdown()
	defer tcp.Shutdown()
	r.RemoveInterface("wg12")
	r.dc = testClient(ln.Addr().String())
	if err := r.configureProxy("wg12", "127.0.0.1:0", func(ip net.IP) bool { return ip.IsLoopback() }); err != nil {
		t.Fatal(err)
	}
	proxyAddr = r.proxies["wg12"].udp.PacketConn.LocalAddr().String()
	for _, transport := range []string{"udp", "tcp"} {
		address := proxyAddr
		if transport == "tcp" {
			address = r.proxies["wg12"].tcp.Listener.Addr().String()
		}
		req := new(dns.Msg)
		req.SetQuestion("r1.googlevideo.com.", dns.TypeA)
		req.CheckingDisabled = true
		req.SetEdns0(1232, true)
		resp, _, err := (&dns.Client{Net: transport, Timeout: time.Second}).Exchange(req, address)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Truncated || !resp.AuthenticatedData || len(resp.Answer) != 1 || resp.IsEdns0() == nil || !resp.IsEdns0().Do() {
			t.Fatalf("flags/answer changed: %v", resp)
		}
	}
	for i := 0; i < 3; i++ {
		if !<-observed {
			t.Fatal("request EDNS/DNSSEC flags lost")
		}
	}
	if f.snapshot(aliases.DomainSetV4(a.ID))["192.0.2.43"] != 300 {
		t.Fatal("TCP result not learned")
	}
}

func TestProxyUpstreamLoopExcluded(t *testing.T) {
	r, _, _, addr := proxyHarness(t)
	r.RemoveInterface("wg12")
	r.dc = testClient("127.0.0.1:53")
	if err := r.configureProxy("wg12", "127.0.0.1:0", func(ip net.IP) bool { return ip.IsLoopback() }); err != nil {
		t.Fatal(err)
	}
	addr = r.proxies["wg12"].udp.PacketConn.LocalAddr().String()
	r.proxyMu.Lock()
	r.proxyStatus["self"] = aliases.DNSProxyStatus{Address: "127.0.0.1:53", Ready: true}
	r.proxyMu.Unlock()
	if got := queryProxy(t, addr, "example.com", dns.TypeA).Rcode; got != dns.RcodeServerFailure {
		t.Fatalf("rcode=%d", got)
	}
}

func TestProxyInterfaceReconfigureAndRestart(t *testing.T) {
	r, _, _, _ := proxyHarness(t)
	// Reserve an address, then use it for both transports after releasing it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := r.configureProxy("wg12", addr, func(net.IP) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if s := r.ProxyStatus()["wg12"]; !s.Ready || s.Address != addr {
		t.Fatalf("status=%+v", s)
	}
	r.RemoveInterface("wg12")
	if err := r.configureProxy("wg12", addr, func(net.IP) bool { return true }); err != nil {
		t.Fatal(err)
	}
}

func TestProxyAliasEditsAndDeletionStopLearning(t *testing.T) {
	r, f, upstream, addr := proxyHarness(t)
	a := createDomainAlias(t, r.am, "Video", "*.googlevideo.com")
	r.Sync(a.ID)
	upstream.set("r1.googlevideo.com", dns.TypeA, aRR(t, "r1.googlevideo.com", 300, "192.0.2.42"))
	queryProxy(t, addr, "r1.googlevideo.com", dns.TypeA)
	if _, err := r.am.Update(a.ID, aliases.Alias{Entries: []string{"*.example.net"}}); err != nil {
		t.Fatal(err)
	}
	r.Sync(a.ID)
	upstream.set("r1.googlevideo.com", dns.TypeA, aRR(t, "r1.googlevideo.com", 300, "192.0.2.43"))
	queryProxy(t, addr, "r1.googlevideo.com", dns.TypeA)
	entries := f.snapshot(aliases.DomainSetV4(a.ID))
	if entries["192.0.2.42"] == 0 || entries["192.0.2.43"] != 0 {
		t.Fatalf("entries after edit: %v", entries)
	}
	r.Forget(a.ID)
	upstream.set("cdn.example.net", dns.TypeA, aRR(t, "cdn.example.net", 300, "192.0.2.44"))
	queryProxy(t, addr, "cdn.example.net", dns.TypeA)
	if f.snapshot(aliases.DomainSetV4(a.ID))["192.0.2.44"] != 0 {
		t.Fatal("retired alias still learns")
	}
}

func TestProxyRejectsUnspecifiedListenAddress(t *testing.T) {
	r := New(nil, newFakeIPSet())
	for _, address := range []string{"0.0.0.0/0", "::/0", "224.0.0.1/24", "invalid"} {
		if err := r.ConfigureInterface("wg12", address, func(net.IP) bool { return true }); err == nil {
			t.Errorf("invalid listen address accepted: %s", address)
		}
		if s := r.ProxyStatus()["wg12"]; s.Ready || s.Error == "" {
			t.Fatalf("status=%+v", s)
		}
	}
}
