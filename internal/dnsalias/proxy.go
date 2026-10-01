package dnsalias

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/miekg/dns"
)

type proxyListener struct {
	address string
	udp     *dns.Server
	tcp     *dns.Server
}

// ConfigureInterface binds both transports before reporting the listener ready.
// Access is evaluated for each request, so peer changes take effect immediately.
func (r *Resolver) ConfigureInterface(id, address string, allow func(net.IP) bool) error {
	ip, _, err := net.ParseCIDR(address)
	if err == nil && (ip.IsUnspecified() || ip.IsMulticast()) {
		err = fmt.Errorf("a specific unicast tunnel address is required")
	}
	if err != nil {
		err = fmt.Errorf("DNS listener address: %w", err)
		r.proxyMu.Lock()
		r.proxyStatus[id] = aliases.DNSProxyStatus{Address: address, Error: err.Error()}
		r.proxyMu.Unlock()
		return err
	}
	return r.configureProxy(id, net.JoinHostPort(ip.String(), "53"), allow)
}

func (r *Resolver) configureProxy(id, address string, allow func(net.IP) bool) error {
	r.proxyConfigMu.Lock()
	defer r.proxyConfigMu.Unlock()
	r.proxyMu.Lock()
	old := r.proxies[id]
	status := r.proxyStatus[id]
	r.proxyMu.Unlock()
	if old != nil && old.address == address && status.Ready {
		return nil
	}
	if old != nil {
		old.udp.Shutdown()
		old.tcp.Shutdown()
		r.proxyMu.Lock()
		delete(r.proxies, id)
		r.proxyMu.Unlock()
	}
	fail := func(err error) error {
		r.proxyMu.Lock()
		r.proxyStatus[id] = aliases.DNSProxyStatus{Address: address, Error: err.Error()}
		r.proxyMu.Unlock()
		return err
	}
	pc, err := net.ListenPacket("udp", address)
	if err != nil {
		return fail(err)
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		pc.Close()
		return fail(err)
	}
	ready := make(chan struct{}, 2)
	startErrors := make(chan error, 2)
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		host, _, err := net.SplitHostPort(w.RemoteAddr().String())
		if err != nil || allow == nil || !allow(net.ParseIP(host)) {
			reply := new(dns.Msg)
			reply.SetRcode(req, dns.RcodeRefused)
			w.WriteMsg(reply)
			return
		}
		if len(req.Question) != 1 || req.Opcode != dns.OpcodeQuery {
			reply := new(dns.Msg)
			reply.SetRcode(req, dns.RcodeFormatError)
			w.WriteMsg(reply)
			return
		}
		reply, err := r.forward(req, w.RemoteAddr().Network() == "tcp")
		if err == nil && reply.Rcode == dns.RcodeSuccess {
			err = r.learnResponse(req, reply)
		}
		if err != nil {
			reply = new(dns.Msg)
			reply.SetRcode(req, dns.RcodeServerFailure)
		}
		if w.RemoteAddr().Network() != "tcp" {
			size := uint16(512)
			if opt := req.IsEdns0(); opt != nil && opt.UDPSize() > size {
				size = opt.UDPSize()
			}
			reply.Truncate(int(size))
		}
		w.WriteMsg(reply)
	})
	p := &proxyListener{address: address}
	p.udp = &dns.Server{PacketConn: pc, Handler: handler, NotifyStartedFunc: func() { ready <- struct{}{} }}
	p.tcp = &dns.Server{Listener: ln, Handler: handler, NotifyStartedFunc: func() { ready <- struct{}{} }}
	r.proxyMu.Lock()
	r.proxies[id] = p
	r.proxyMu.Unlock()
	serve := func(s *dns.Server) {
		if err := s.ActivateAndServe(); err != nil {
			startErrors <- err
			r.proxyMu.Lock()
			if r.proxies[id] == p {
				r.proxyStatus[id] = aliases.DNSProxyStatus{Address: address, Error: err.Error()}
			}
			r.proxyMu.Unlock()
		}
	}
	go serve(p.udp)
	go serve(p.tcp)
	timer := time.NewTimer(QueryTimeout)
	defer timer.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case err := <-startErrors:
			p.udp.Shutdown()
			p.tcp.Shutdown()
			pc.Close()
			ln.Close()
			r.proxyMu.Lock()
			delete(r.proxies, id)
			r.proxyMu.Unlock()
			return fail(err)
		case <-timer.C:
			p.udp.Shutdown()
			p.tcp.Shutdown()
			pc.Close()
			ln.Close()
			r.proxyMu.Lock()
			delete(r.proxies, id)
			r.proxyMu.Unlock()
			return fail(fmt.Errorf("DNS listener startup timed out"))
		}
	}
	r.proxyMu.Lock()
	r.proxyStatus[id] = aliases.DNSProxyStatus{Address: address, Ready: true}
	r.proxyMu.Unlock()
	return nil
}

func (r *Resolver) RemoveInterface(id string) {
	r.proxyConfigMu.Lock()
	defer r.proxyConfigMu.Unlock()
	r.proxyMu.Lock()
	p := r.proxies[id]
	delete(r.proxies, id)
	delete(r.proxyStatus, id)
	r.proxyMu.Unlock()
	if p != nil {
		p.udp.Shutdown()
		p.tcp.Shutdown()
	}
}

func (r *Resolver) stopProxies() {
	r.proxyMu.Lock()
	ids := make([]string, 0, len(r.proxies))
	for id := range r.proxies {
		ids = append(ids, id)
	}
	r.proxyMu.Unlock()
	for _, id := range ids {
		r.RemoveInterface(id)
	}
}

func (r *Resolver) ProxyStatus() map[string]aliases.DNSProxyStatus {
	r.proxyMu.Lock()
	defer r.proxyMu.Unlock()
	out := make(map[string]aliases.DNSProxyStatus, len(r.proxyStatus))
	for id, s := range r.proxyStatus {
		out[id] = s
	}
	return out
}

// forward preserves the client's message including EDNS and DNSSEC flags.
func (r *Resolver) forward(req *dns.Msg, tcp bool) (*dns.Msg, error) {
	statuses := r.ProxyStatus()
	var last error
	for _, server := range r.dc.servers {
		host, port, err := net.SplitHostPort(server)
		if err != nil {
			continue
		}
		self := false
		for _, s := range statuses {
			local, _, _ := net.SplitHostPort(s.Address)
			if net.ParseIP(host).Equal(net.ParseIP(local)) && port == "53" {
				self = true
			}
		}
		if self {
			continue
		}
		client := r.dc.udp
		if tcp {
			client = r.dc.tcp
		}
		resp, _, err := client.Exchange(req.Copy(), server)
		if err == nil && resp != nil && resp.Truncated && !tcp {
			resp, _, err = r.dc.tcp.Exchange(req.Copy(), server)
		}
		if err == nil && resp != nil {
			return resp, nil
		}
		last = err
	}
	return nil, fmt.Errorf("DNS upstream unavailable: %v", last)
}

// learnResponse accepts only addresses on the query's answer CNAME chain.
func (r *Resolver) learnResponse(req, resp *dns.Msg) error {
	names := map[string]bool{strings.ToLower(dns.Fqdn(req.Question[0].Name)): true}
	for depth := 0; depth < MaxCNAMEDepth; depth++ {
		changed := false
		for _, rr := range resp.Answer {
			if c, ok := rr.(*dns.CNAME); ok && names[strings.ToLower(c.Hdr.Name)] {
				target := strings.ToLower(dns.Fqdn(c.Target))
				if !names[target] {
					names[target] = true
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	result := newLookupResult()
	for _, rr := range resp.Answer {
		if !names[strings.ToLower(rr.Header().Name)] {
			continue
		}
		switch v := rr.(type) {
		case *dns.A:
			result.add(v.A, clampTTL(v.Hdr.Ttl))
		case *dns.AAAA:
			result.add(v.AAAA, clampTTL(v.Hdr.Ttl))
		}
	}
	if result.empty() {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, st := range r.states {
		st.mu.Lock()
		match := false
		for _, pattern := range st.domains {
			for name := range names {
				if aliases.DomainMatches(pattern, name) {
					match = true
				}
			}
		}
		if !match {
			st.mu.Unlock()
			continue
		}
		// Hold the alias lock through writes so an edit cannot race with stale matching.
		err := r.ensureSets(st)
		if err == nil {
			_, err = r.im.AddTimeoutEntries(st.setV4, result.V4)
		}
		if err == nil {
			_, err = r.im.AddTimeoutEntries(st.setV6, result.V6)
		}
		if err != nil {
			st.status.LastError = err.Error()
			st.mu.Unlock()
			return err
		}
		st.status.LastUpdate = time.Now().UTC().Format(time.RFC3339)
		st.status.LastError = ""
		st.mu.Unlock()
	}
	return nil
}
