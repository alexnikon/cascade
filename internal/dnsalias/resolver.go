// Package dnsalias keeps the kernel ipsets of "domain" firewall aliases in sync
// with DNS.
//
// One goroutine per domain alias (mirroring gateway.Monitor) resolves the
// alias's names, writes the addresses into two timeout-capable ipsets — one per
// address family — and sleeps until the shortest TTL is about to lapse. Entries
// are only ever added or refreshed, never flushed, so the kernel expires stale
// addresses on its own and a DNS outage degrades gradually instead of wiping the
// set.
//
// This is not a DNS proxy and it does not intercept client queries: it resolves
// only the names the operator configured on the alias.
//
// The configured names in SQLite are the source of truth; resolved addresses
// live only in the kernel and the runtime state below lives only in memory.
package dnsalias

import (
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/alexnikon/cascade/internal/aliases"
)

// Resolution policy. Kept together here rather than scattered through the code
// so the bounds are adjustable in one place.
const (
	// MinTTL / MaxTTL bound what a DNS answer can ask for. A very short TTL
	// would churn the ipset; a very long one would pin an address that has
	// since moved.
	MinTTL = 60 * time.Second
	MaxTTL = 3600 * time.Second

	// DefaultTTL is used when a record carries no usable TTL.
	DefaultTTL = 300 * time.Second

	// SetTimeout is the default timeout an entry gets if it is ever added
	// without an explicit one. Per-entry TTLs normally override it.
	SetTimeout = MaxTTL

	// RefreshFraction schedules the next refresh at this fraction of the
	// shortest live TTL, so entries are renewed before the kernel drops them.
	RefreshFraction = 3.0 / 4.0

	// Retry backoff after a failed refresh: RetryBase doubling up to RetryMax.
	RetryBase = 30 * time.Second
	RetryMax  = 15 * time.Minute

	// QueryTimeout bounds a single DNS exchange.
	QueryTimeout = 5 * time.Second

	// MaxCNAMEDepth bounds CNAME chasing when the resolver does not flatten
	// the chain for us.
	MaxCNAMEDepth = 8

	resolvConfPath = "/etc/resolv.conf"
)

// ipsetWriter is the slice of *ipset.Manager this package needs. Narrowing it to
// an interface keeps the resolver testable without a kernel.
type ipsetWriter interface {
	CreateTimeoutSet(name string, v6 bool, defaultTimeout int) error
	AddTimeoutEntries(name string, entries map[string]int) (int, error)
	DestroySet(name string) error
	ListSetNames() []string
	GetEntryCount(name string) int
}

// Resolver owns the background refresh of every domain alias.
type Resolver struct {
	am *aliases.Manager
	im ipsetWriter
	dc *dnsClient

	mu      sync.RWMutex
	states  map[string]*aliasState
	stop    chan struct{}
	wg      sync.WaitGroup
	started bool
}

// aliasState is the per-alias runtime state and control plane.
type aliasState struct {
	id      string
	setV4   string
	setV6   string
	wake    chan struct{} // buffered(1): request an immediate refresh
	stopped chan struct{} // closed when this alias is retired

	mu       sync.Mutex
	name     string
	domains  []string
	status   aliases.DomainStatus
	failures int
}

// New creates a Resolver. Call Start to bring it up.
func New(am *aliases.Manager, im ipsetWriter) *Resolver {
	return &Resolver{
		am:     am,
		im:     im,
		dc:     newDNSClient(systemServers(resolvConfPath)),
		states: make(map[string]*aliasState),
		stop:   make(chan struct{}),
	}
}

// Start reconciles the kernel with the configured domain aliases and begins
// background refreshing. Safe to call once; subsequent calls are no-ops.
func (r *Resolver) Start() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()

	all, err := r.am.GetAll()
	if err != nil {
		log.Printf("dnsalias: start: load aliases: %v", err)
		return
	}

	live := make(map[string]struct{})
	n := 0
	for i := range all {
		a := all[i]
		if a.Type != "domain" {
			continue
		}
		live[aliases.DomainSetV4(a.ID)] = struct{}{}
		live[aliases.DomainSetV6(a.ID)] = struct{}{}
		r.startAlias(&a)
		n++
	}

	r.reconcileOrphans(live)
	log.Printf("dnsalias: started (%d domain aliases, servers %s)", n, strings.Join(r.dc.servers, ", "))
}

// Stop retires every alias goroutine and waits for them to finish. The kernel
// sets are left in place: they are recreated and re-resolved on the next start,
// and leaving them means a restart does not blank out live firewall rules.
func (r *Resolver) Stop() {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return
	}
	r.started = false
	close(r.stop)
	r.mu.Unlock()

	r.wg.Wait()

	r.mu.Lock()
	r.states = make(map[string]*aliasState)
	r.mu.Unlock()
	log.Printf("dnsalias: stopped")
}

// Sync brings the resolver in line with the current configuration of one alias:
// it starts tracking a new domain alias, picks up an edited domain list and
// triggers an immediate re-resolution, or retires an alias that is gone or is
// no longer of type domain.
func (r *Resolver) Sync(aliasID string) {
	a, err := r.am.GetByID(aliasID)
	if err != nil || a == nil || a.Type != "domain" {
		r.Forget(aliasID)
		return
	}
	r.startAlias(a)
}

// Refresh requests an immediate out-of-band re-resolution. It never blocks, and
// never waits for the result — the API stays responsive while DNS is in flight.
func (r *Resolver) Refresh(aliasID string) {
	r.mu.RLock()
	st := r.states[aliasID]
	r.mu.RUnlock()
	if st == nil {
		// Not tracked yet (e.g. created while the resolver was down) — adopt it.
		r.Sync(aliasID)
		return
	}
	select {
	case st.wake <- struct{}{}:
	default: // a refresh is already queued
	}
}

// Forget retires an alias's goroutine and drops its runtime state. The kernel
// sets are destroyed by aliases.Manager.Delete, which owns the alias lifecycle.
func (r *Resolver) Forget(aliasID string) {
	r.mu.Lock()
	st, ok := r.states[aliasID]
	if ok {
		delete(r.states, aliasID)
	}
	r.mu.Unlock()

	if ok {
		close(st.stopped)
	}
}

// Status returns a snapshot of an alias's runtime state, or nil if it is not
// tracked.
func (r *Resolver) Status(aliasID string) *aliases.DomainStatus {
	r.mu.RLock()
	st := r.states[aliasID]
	r.mu.RUnlock()
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.status
	return &s
}

// ── Internals ─────────────────────────────────────────────────────────────────

// startAlias (re)starts tracking an alias. If it is already tracked, the domain
// list is updated in place and a refresh is requested — the goroutine and, more
// importantly, the kernel sets are left undisturbed so an edit or a rename never
// interrupts the firewall rules matching them.
func (r *Resolver) startAlias(a *aliases.Alias) {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return
	}
	if st, ok := r.states[a.ID]; ok {
		r.mu.Unlock()
		st.mu.Lock()
		changed := !equalStrings(st.domains, a.Entries)
		st.domains = append([]string(nil), a.Entries...)
		st.name = a.Name
		st.mu.Unlock()
		if changed {
			r.Refresh(a.ID)
		}
		return
	}

	st := &aliasState{
		id:      a.ID,
		name:    a.Name,
		setV4:   aliases.DomainSetV4(a.ID),
		setV6:   aliases.DomainSetV6(a.ID),
		domains: append([]string(nil), a.Entries...),
		wake:    make(chan struct{}, 1),
		stopped: make(chan struct{}),
	}
	r.states[a.ID] = st
	r.wg.Add(1)
	r.mu.Unlock()

	go r.run(st)
}

// run is the per-alias refresh loop: resolve, then sleep until the shortest TTL
// is due (or until woken, retired, or the resolver shuts down).
func (r *Resolver) run(st *aliasState) {
	defer r.wg.Done()

	if err := r.ensureSets(st); err != nil {
		log.Printf("dnsalias: %s: create ipsets: %v", st.name, err)
	}

	for {
		delay := r.resolveOnce(st)

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-st.wake:
			timer.Stop()
		case <-st.stopped:
			timer.Stop()
			return
		case <-r.stop:
			timer.Stop()
			return
		}
	}
}

func (r *Resolver) ensureSets(st *aliasState) error {
	timeout := int(SetTimeout / time.Second)
	if err := r.im.CreateTimeoutSet(st.setV4, false, timeout); err != nil {
		return err
	}
	return r.im.CreateTimeoutSet(st.setV6, true, timeout)
}

// resolveOnce resolves every domain of the alias and writes the results into the
// kernel sets. It returns how long to wait before the next attempt.
//
// Failure handling is the important part: a domain that fails is skipped and the
// addresses that did resolve are still written, and if *every* domain fails the
// sets are not touched at all. Nothing is ever removed here — expiry is the
// kernel's job — so the last known-good addresses survive an outage and simply
// age out if it persists.
func (r *Resolver) resolveOnce(st *aliasState) time.Duration {
	st.mu.Lock()
	domains := append([]string(nil), st.domains...)
	st.status.Resolving = true
	st.mu.Unlock()

	merged := newLookupResult()
	var errs []error
	for _, d := range domains {
		res, err := r.dc.lookup(d)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		merged.merge(res)
	}

	allFailed := len(errs) > 0 && len(errs) == len(domains)
	now := time.Now().UTC()

	if !allFailed {
		if err := r.ensureSets(st); err != nil {
			errs = append(errs, err)
		}
		if _, err := r.im.AddTimeoutEntries(st.setV4, merged.V4); err != nil {
			errs = append(errs, err)
		}
		if _, err := r.im.AddTimeoutEntries(st.setV6, merged.V6); err != nil {
			errs = append(errs, err)
		}
	}

	var delay time.Duration
	st.mu.Lock()
	st.status.Resolving = false
	st.status.LastError = joinErrors(errs)
	if allFailed {
		st.failures++
		delay = backoff(st.failures)
	} else {
		st.failures = 0
		st.status.LastUpdate = now.Format(time.RFC3339)
		delay = refreshInterval(merged.minTTL())
	}
	// Counts come from the kernel, not from this round's answers, so they
	// reflect what rules will actually match — including entries kept from
	// earlier rounds that have not expired yet.
	st.status.IPv4Count = r.im.GetEntryCount(st.setV4)
	st.status.IPv6Count = r.im.GetEntryCount(st.setV6)
	st.status.NextUpdate = now.Add(delay).Format(time.RFC3339)
	name := st.name
	st.mu.Unlock()

	if allFailed {
		log.Printf("dnsalias: %s: all %d domains failed, keeping last known-good entries, retrying in %s: %s",
			name, len(domains), delay.Round(time.Second), joinErrors(errs))
	} else if len(errs) > 0 {
		log.Printf("dnsalias: %s: %d/%d domains failed: %s", name, len(errs), len(domains), joinErrors(errs))
	}
	return delay
}

// reconcileOrphans destroys domain ipsets left behind by aliases that no longer
// exist — a restore from backup, or a deletion while Cascade was down.
func (r *Resolver) reconcileOrphans(live map[string]struct{}) {
	for _, name := range r.im.ListSetNames() {
		if !aliases.IsDomainSet(name) {
			continue
		}
		if _, ok := live[name]; ok {
			continue
		}
		if err := r.im.DestroySet(name); err != nil {
			log.Printf("dnsalias: destroy orphan set %s: %v", name, err)
			continue
		}
		log.Printf("dnsalias: destroyed orphan set %s", name)
	}
}

// refreshInterval converts the shortest live TTL into a refresh delay, renewing
// entries before the kernel expires them.
func refreshInterval(minTTLSeconds int) time.Duration {
	if minTTLSeconds <= 0 {
		return DefaultTTL
	}
	d := time.Duration(float64(minTTLSeconds)*RefreshFraction) * time.Second
	if floor := time.Duration(float64(MinTTL) * RefreshFraction); d < floor {
		d = floor
	}
	return d
}

// backoff returns an exponentially increasing, jittered retry delay.
func backoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := RetryBase
	for i := 1; i < failures && d < RetryMax; i++ {
		d *= 2
	}
	if d > RetryMax {
		d = RetryMax
	}
	// ±10% jitter so many aliases failing at once do not retry in lockstep.
	jitter := time.Duration(rand.Int63n(int64(d/5) + 1)) //nolint:gosec // not security-sensitive
	d = d - d/10 + jitter
	if d > RetryMax {
		d = RetryMax
	}
	return d
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── Singleton accessor ────────────────────────────────────────────────────────

var instance *Resolver

// SetInstance stores the initialized Resolver for package-level access.
func SetInstance(r *Resolver) { instance = r }

// Get returns the package-level Resolver, or nil if the resolver was never
// started. Callers must tolerate nil: the API layer runs in tests and in
// non-Linux development where no resolver exists.
func Get() *Resolver { return instance }
