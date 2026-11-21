package dnsalias

import (
	"testing"

	"github.com/miekg/dns"

	"github.com/alexnikon/cascade/internal/aliases"
)

func TestCustomAliasDNSLeavesProxyOnSystemUpstreams(t *testing.T) {
	const domain = "example.test"
	system := newTestDNS(t)
	system.set(domain, dns.TypeA, aRR(t, domain, 300, "192.0.2.1"))
	failed := newTestDNS(t)
	failed.setRcode(domain, dns.TypeA, dns.RcodeServerFailure)
	custom := newTestDNS(t)
	custom.set(domain, dns.TypeA, aRR(t, domain, 300, "192.0.2.2"))
	kernel := newFakeIPSet()
	r := newTestResolver(t, nil, kernel, system.addr)
	r.customUpstreams.Store(testClient(failed.addr, custom.addr))
	state := &aliasState{domains: []string{domain}, setV4: aliases.DomainSetV4("test"), setV6: aliases.DomainSetV6("test")}
	r.resolveOnce(state)
	if entries := kernel.snapshot(state.setV4); len(entries) != 1 || entries["192.0.2.2"] != 300 {
		t.Fatalf("alias did not use custom DNS fallback: %v", entries)
	}

	req := new(dns.Msg).SetQuestion(dns.Fqdn(domain), dns.TypeA)
	resp, err := r.forward(req, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "192.0.2.1" {
		t.Fatalf("proxy did not use system DNS: %v", resp.Answer)
	}
}
