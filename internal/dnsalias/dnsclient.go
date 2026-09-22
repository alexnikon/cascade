package dnsalias

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// lookupResult holds the addresses a name resolved to, keyed by address with the
// clamped TTL (seconds) as the value. Duplicates across records, CNAME hops and
// address families collapse naturally; when the same address arrives with
// different TTLs the longest one wins, so the ipset entry lives as long as any
// answer says it may.
type lookupResult struct {
	V4 map[string]int
	V6 map[string]int
}

func newLookupResult() *lookupResult {
	return &lookupResult{V4: map[string]int{}, V6: map[string]int{}}
}

func (r *lookupResult) add(ip net.IP, ttl int) {
	if ip == nil {
		return
	}
	s := ip.String()
	m := r.V6
	if ip.To4() != nil {
		m = r.V4
	}
	if cur, ok := m[s]; !ok || ttl > cur {
		m[s] = ttl
	}
}

func (r *lookupResult) merge(other *lookupResult) {
	for ip, ttl := range other.V4 {
		if cur, ok := r.V4[ip]; !ok || ttl > cur {
			r.V4[ip] = ttl
		}
	}
	for ip, ttl := range other.V6 {
		if cur, ok := r.V6[ip]; !ok || ttl > cur {
			r.V6[ip] = ttl
		}
	}
}

func (r *lookupResult) empty() bool { return len(r.V4) == 0 && len(r.V6) == 0 }

// minTTL returns the shortest TTL across both families, which is what the next
// refresh should be scheduled against. Returns 0 when there is nothing to expire.
func (r *lookupResult) minTTL() int {
	best := 0
	for _, m := range []map[string]int{r.V4, r.V6} {
		for _, ttl := range m {
			if best == 0 || ttl < best {
				best = ttl
			}
		}
	}
	return best
}

// dnsClient queries a fixed list of upstream resolvers.
type dnsClient struct {
	servers []string // "host:port"
	udp     *dns.Client
	tcp     *dns.Client
}

func newDNSClient(servers []string) *dnsClient {
	return &dnsClient{
		servers: servers,
		udp:     &dns.Client{Net: "udp", Timeout: QueryTimeout},
		tcp:     &dns.Client{Net: "tcp", Timeout: QueryTimeout},
	}
}

// systemServers reads the upstream resolvers from resolv.conf, falling back to
// the loopback resolver if the file is unreadable or empty.
func systemServers(resolvConf string) []string {
	cfg, err := dns.ClientConfigFromFile(resolvConf)
	if err != nil || len(cfg.Servers) == 0 {
		return []string{"127.0.0.1:53"}
	}
	port := cfg.Port
	if port == "" {
		port = "53"
	}
	servers := make([]string, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		servers = append(servers, net.JoinHostPort(s, port))
	}
	return servers
}

// lookup resolves one name to its A and AAAA addresses, following CNAME chains.
//
// A recursive resolver normally returns the final address records alongside the
// CNAMEs in a single answer section, so every A/AAAA record is taken regardless
// of its owner name. Only when an answer carries CNAMEs and no addresses at all
// is the target queried explicitly, bounded by MaxCNAMEDepth.
//
// An error is returned only when the name could not be resolved at all; a name
// that legitimately has no AAAA record is a success with no v6 addresses.
func (c *dnsClient) lookup(name string) (*lookupResult, error) {
	res := newLookupResult()

	v4Err := c.lookupType(dns.Fqdn(name), dns.TypeA, res, 0)
	v6Err := c.lookupType(dns.Fqdn(name), dns.TypeAAAA, res, 0)

	// Both query types failing means the name is genuinely unresolvable right
	// now. One failing while the other answers is routine (many names have no
	// AAAA) and must not be reported as an outage.
	if v4Err != nil && v6Err != nil {
		if res.empty() {
			return res, fmt.Errorf("%s: %w", name, v4Err)
		}
	}
	if res.empty() && v4Err == nil && v6Err == nil {
		return res, fmt.Errorf("%s: no A or AAAA records", name)
	}
	return res, nil
}

func (c *dnsClient) lookupType(name string, qtype uint16, res *lookupResult, depth int) error {
	if depth > MaxCNAMEDepth {
		return fmt.Errorf("%s: CNAME chain deeper than %d", name, MaxCNAMEDepth)
	}

	msg, err := c.exchange(name, qtype)
	if err != nil {
		return err
	}

	var cnameTarget string
	found := false
	for _, rr := range msg.Answer {
		ttl := clampTTL(rr.Header().Ttl)
		switch v := rr.(type) {
		case *dns.A:
			res.add(v.A, ttl)
			found = true
		case *dns.AAAA:
			res.add(v.AAAA, ttl)
			found = true
		case *dns.CNAME:
			cnameTarget = v.Target
		}
	}

	// Only chase the alias when the resolver did not already do it for us.
	if !found && cnameTarget != "" {
		return c.lookupType(cnameTarget, qtype, res, depth+1)
	}
	return nil
}

// exchange sends a query to each configured server in turn, returning the first
// usable response. Truncated UDP answers are retried over TCP.
func (c *dnsClient) exchange(name string, qtype uint16) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	m.RecursionDesired = true

	var lastErr error
	for _, server := range c.servers {
		resp, _, err := c.udp.Exchange(m, server)
		if err == nil && resp != nil && resp.Truncated {
			resp, _, err = c.tcp.Exchange(m, server)
		}
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", server, err)
			continue
		}
		if resp.Rcode != dns.RcodeSuccess {
			lastErr = fmt.Errorf("%s: %s", server, dns.RcodeToString[resp.Rcode])
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no DNS servers configured")
	}
	return nil, lastErr
}

// clampTTL bounds a record's TTL to [MinTTL, MaxTTL] and converts it to seconds.
// A zero TTL — which would make the ipset entry expire instantly — is treated as
// "unspecified" and replaced by DefaultTTL before clamping.
func clampTTL(ttl uint32) int {
	d := time.Duration(ttl) * time.Second
	if ttl == 0 {
		d = DefaultTTL
	}
	if d < MinTTL {
		d = MinTTL
	}
	if d > MaxTTL {
		d = MaxTTL
	}
	return int(d / time.Second)
}

// joinErrors renders up to three failures into one human-readable line for the
// runtime lastError field.
func joinErrors(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, e := range errs {
		if len(parts) == 3 {
			parts = append(parts, fmt.Sprintf("(+%d more)", len(errs)-3))
			break
		}
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}
