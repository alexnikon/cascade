package dnsalias

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/ipset"
)

// ── Fake ipset backend ────────────────────────────────────────────────────────

// fakeIPSet records what the resolver asks the kernel to do. It deliberately
// implements add-only semantics — there is no flush — so a test can prove that
// entries written in one round survive a later DNS failure.
type fakeIPSet struct {
	mu        sync.Mutex
	created   map[string]bool           // set name → v6?
	entries   map[string]map[string]int // set name → ip → timeout
	destroyed []string
	present   []string // what ListSetNames reports
	addErr    error
}

func newFakeIPSet() *fakeIPSet {
	return &fakeIPSet{
		created: map[string]bool{},
		entries: map[string]map[string]int{},
	}
}

func (f *fakeIPSet) CreateTimeoutSet(name string, v6 bool, timeout int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created[name] = v6
	if f.entries[name] == nil {
		f.entries[name] = map[string]int{}
	}
	return nil
}

func (f *fakeIPSet) AddTimeoutEntries(name string, entries map[string]int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return 0, f.addErr
	}
	if f.entries[name] == nil {
		f.entries[name] = map[string]int{}
	}
	for ip, ttl := range entries {
		f.entries[name][ip] = ttl
	}
	return len(entries), nil
}

func (f *fakeIPSet) DestroySet(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, name)
	delete(f.entries, name)
	delete(f.created, name)
	return nil
}

func (f *fakeIPSet) ListSetNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.present...)
}

func (f *fakeIPSet) GetEntryCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries[name])
}

func (f *fakeIPSet) snapshot(name string) map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for k, v := range f.entries[name] {
		out[k] = v
	}
	return out
}

func (f *fakeIPSet) wasCreated(name string) (v6 bool, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v6, ok = f.created[name]
	return
}

func (f *fakeIPSet) destroyedSets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.destroyed...)
}

// ── Harness ───────────────────────────────────────────────────────────────────

func initTestDB(t *testing.T) *aliases.Manager {
	t.Helper()
	dir, err := os.MkdirTemp("", "cascade-dnsalias-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	if err := db.Init(dir); err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	im, err := ipset.New(dir)
	if err != nil {
		t.Fatalf("ipset.New: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})
	return aliases.New(im)
}

// newTestResolver wires a Resolver to a scripted DNS server and a fake kernel.
func newTestResolver(t *testing.T, am *aliases.Manager, f *fakeIPSet, servers ...string) *Resolver {
	t.Helper()
	r := New(am, f)
	r.dc = testClient(servers...)
	t.Cleanup(r.Stop)
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func createDomainAlias(t *testing.T, am *aliases.Manager, name string, entries ...string) *aliases.Alias {
	t.Helper()
	a, err := am.Create(aliases.Alias{Name: name, Type: "domain", Entries: entries})
	if err != nil {
		t.Fatalf("create domain alias: %v", err)
	}
	return a
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestResolver_CreatesBothAddressFamilySets(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 300, "203.0.113.1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "example.com")
	r := newTestResolver(t, am, f, d.addr)
	r.Start()

	v4, v6 := aliases.DomainSetV4(a.ID), aliases.DomainSetV6(a.ID)
	waitFor(t, "both sets to be created", func() bool {
		_, ok4 := f.wasCreated(v4)
		_, ok6 := f.wasCreated(v6)
		return ok4 && ok6
	})

	if isV6, _ := f.wasCreated(v4); isV6 {
		t.Errorf("%s was created as an IPv6 set", v4)
	}
	if isV6, _ := f.wasCreated(v6); !isV6 {
		t.Errorf("%s was not created as an IPv6 set", v6)
	}
}

func TestResolver_WritesResolvedAddressesWithTTLs(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA,
		aRR(t, "example.com", 300, "203.0.113.1"),
		aRR(t, "example.com", 300, "203.0.113.2"),
	)
	d.set("example.com", dns.TypeAAAA, aaaaRR(t, "example.com", 600, "2001:db8::1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "example.com")
	r := newTestResolver(t, am, f, d.addr)
	r.Start()

	v4, v6 := aliases.DomainSetV4(a.ID), aliases.DomainSetV6(a.ID)
	waitFor(t, "addresses to be written", func() bool {
		return len(f.snapshot(v4)) == 2 && len(f.snapshot(v6)) == 1
	})

	if ttl := f.snapshot(v4)["203.0.113.1"]; ttl != 300 {
		t.Errorf("v4 entry timeout = %d, want 300", ttl)
	}
	if ttl := f.snapshot(v6)["2001:db8::1"]; ttl != 600 {
		t.Errorf("v6 entry timeout = %d, want 600", ttl)
	}

	st := r.Status(a.ID)
	if st == nil {
		t.Fatal("Status returned nil for a tracked alias")
	}
	if st.IPv4Count != 2 || st.IPv6Count != 1 {
		t.Errorf("counts = v4:%d v6:%d, want 2/1", st.IPv4Count, st.IPv6Count)
	}
	if st.LastUpdate == "" || st.NextUpdate == "" {
		t.Errorf("want lastUpdate and nextUpdate to be set, got %+v", st)
	}
	if st.LastError != "" {
		t.Errorf("want no error, got %q", st.LastError)
	}
}

// The heart of Step 6: an outage must not destroy the last known-good set.
func TestResolver_DNSFailureKeepsLastKnownGoodEntries(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 300, "203.0.113.1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "example.com")
	v4 := aliases.DomainSetV4(a.ID)
	r := newTestResolver(t, am, f, d.addr)
	r.Start()

	waitFor(t, "the first successful resolution", func() bool {
		return len(f.snapshot(v4)) == 1
	})
	before := f.snapshot(v4)

	// DNS goes dark, and we force a refresh round.
	d.setFailing(true)
	r.Refresh(a.ID)

	waitFor(t, "the failure to be recorded", func() bool {
		st := r.Status(a.ID)
		return st != nil && st.LastError != ""
	})

	after := f.snapshot(v4)
	if len(after) != len(before) {
		t.Fatalf("entries changed during the outage: before %v, after %v", before, after)
	}
	if _, ok := after["203.0.113.1"]; !ok {
		t.Fatalf("last known-good address was lost during the outage: %v", after)
	}
	if destroyed := f.destroyedSets(); len(destroyed) != 0 {
		t.Fatalf("sets were destroyed during a DNS outage: %v", destroyed)
	}

	// And the alias itself is still perfectly valid — a firewall rule matching
	// this set keeps working.
	st := r.Status(a.ID)
	if st.IPv4Count != 1 {
		t.Errorf("IPv4Count = %d during outage, want the retained entry", st.IPv4Count)
	}
}

// A partial failure must still publish the addresses that did resolve.
func TestResolver_PartialFailureStillUpdatesWorkingDomains(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("good.example.com", dns.TypeA, aRR(t, "good.example.com", 300, "203.0.113.1"))
	d.setRcode("bad.example.com", dns.TypeA, dns.RcodeServerFailure)
	d.setRcode("bad.example.com", dns.TypeAAAA, dns.RcodeServerFailure)
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Mixed", "good.example.com", "bad.example.com")
	r := newTestResolver(t, am, f, d.addr)
	r.Start()

	v4 := aliases.DomainSetV4(a.ID)
	waitFor(t, "the working domain to be written", func() bool {
		return len(f.snapshot(v4)) == 1
	})

	st := r.Status(a.ID)
	if st.LastError == "" {
		t.Error("want the failing domain reported in lastError")
	}
	if st.LastUpdate == "" {
		t.Error("want lastUpdate set — the round was a partial success")
	}
}

func TestResolver_RecoversAfterOutage(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 300, "203.0.113.1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "example.com")
	v4 := aliases.DomainSetV4(a.ID)
	r := newTestResolver(t, am, f, d.addr)
	r.Start()
	waitFor(t, "the first resolution", func() bool { return len(f.snapshot(v4)) == 1 })

	d.setFailing(true)
	r.Refresh(a.ID)
	waitFor(t, "the failure", func() bool {
		st := r.Status(a.ID)
		return st != nil && st.LastError != ""
	})

	// DNS comes back with a new address.
	d.setFailing(false)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 300, "203.0.113.2"))
	r.Refresh(a.ID)

	waitFor(t, "the new address", func() bool {
		_, ok := f.snapshot(v4)["203.0.113.2"]
		return ok
	})
	waitFor(t, "the error to clear", func() bool {
		st := r.Status(a.ID)
		return st != nil && st.LastError == ""
	})

	// The previous address is not deleted here — the kernel expires it.
	if _, ok := f.snapshot(v4)["203.0.113.1"]; !ok {
		t.Error("the resolver removed an entry itself; expiry must be left to the kernel")
	}
}

func TestResolver_RefreshIsNonBlockingAndReResolves(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 3600, "203.0.113.1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "example.com")
	r := newTestResolver(t, am, f, d.addr)
	r.Start()
	waitFor(t, "the first resolution", func() bool { return d.queryCount() > 0 })

	before := d.queryCount()

	done := make(chan struct{})
	go func() { r.Refresh(a.ID); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Refresh blocked; it must return immediately")
	}

	waitFor(t, "a second round of queries", func() bool { return d.queryCount() > before })
}

func TestResolver_SyncPicksUpEditedDomainList(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("one.example.com", dns.TypeA, aRR(t, "one.example.com", 3600, "203.0.113.1"))
	d.set("two.example.com", dns.TypeA, aRR(t, "two.example.com", 3600, "203.0.113.2"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "one.example.com")
	v4 := aliases.DomainSetV4(a.ID)
	r := newTestResolver(t, am, f, d.addr)
	r.Start()
	waitFor(t, "the first address", func() bool { return len(f.snapshot(v4)) == 1 })

	if _, err := am.Update(a.ID, aliases.Alias{Entries: []string{"one.example.com", "two.example.com"}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	r.Sync(a.ID)

	waitFor(t, "the newly added domain to resolve", func() bool {
		_, ok := f.snapshot(v4)["203.0.113.2"]
		return ok
	})
}

// A rename must not disturb the kernel objects: the set names come from the ID.
func TestResolver_RenameDoesNotRecreateSets(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 3600, "203.0.113.1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "OldName", "example.com")
	v4 := aliases.DomainSetV4(a.ID)
	r := newTestResolver(t, am, f, d.addr)
	r.Start()
	waitFor(t, "the first resolution", func() bool { return len(f.snapshot(v4)) == 1 })

	renamed, err := am.Update(a.ID, aliases.Alias{Name: "NewName"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	r.Sync(a.ID)

	if got := aliases.DomainSetV4(renamed.ID); got != v4 {
		t.Fatalf("set name changed after rename: %s → %s", v4, got)
	}
	if renamed.IPSetName != v4 {
		t.Errorf("stored ipsetName = %q, want it unchanged at %q", renamed.IPSetName, v4)
	}
	if destroyed := f.destroyedSets(); len(destroyed) != 0 {
		t.Errorf("rename destroyed kernel sets: %v", destroyed)
	}
	if _, ok := f.snapshot(v4)["203.0.113.1"]; !ok {
		t.Error("rename lost the resolved addresses")
	}
}

func TestResolver_ForgetStopsTrackingAlias(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 3600, "203.0.113.1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "example.com")
	r := newTestResolver(t, am, f, d.addr)
	r.Start()
	waitFor(t, "the alias to be tracked", func() bool { return r.Status(a.ID) != nil })

	r.Forget(a.ID)
	if st := r.Status(a.ID); st != nil {
		t.Fatalf("Status = %+v after Forget, want nil", st)
	}
}

// Deleting the alias must retire the resolver state and destroy both sets.
func TestResolver_DeletedAliasIsCleanedUp(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 3600, "203.0.113.1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "example.com")
	r := newTestResolver(t, am, f, d.addr)
	r.Start()
	waitFor(t, "the alias to be tracked", func() bool { return r.Status(a.ID) != nil })

	if err := am.Delete(a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Sync is what the API calls after a delete; it must retire the goroutine.
	r.Sync(a.ID)

	if st := r.Status(a.ID); st != nil {
		t.Errorf("Status = %+v after delete, want nil", st)
	}
}

// Sets whose alias vanished while Cascade was down must be swept on startup.
func TestResolver_StartDestroysOrphanSets(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 3600, "203.0.113.1"))
	f := newFakeIPSet()

	a := createDomainAlias(t, am, "Example", "example.com")
	orphan := aliases.DomainSetV4("dead-beef-0000-0000-000000000000")
	f.present = []string{
		aliases.DomainSetV4(a.ID),
		aliases.DomainSetV6(a.ID),
		orphan,
		"default", // an unrelated client-group set must be left alone
	}

	r := newTestResolver(t, am, f, d.addr)
	r.Start()

	waitFor(t, "the orphan to be destroyed", func() bool {
		for _, n := range f.destroyedSets() {
			if n == orphan {
				return true
			}
		}
		return false
	})

	for _, n := range f.destroyedSets() {
		if n == "default" || n == aliases.DomainSetV4(a.ID) {
			t.Fatalf("destroyed a set that is still in use: %s", n)
		}
	}
}

// Step 10: a restart must recreate the sets and resolve again from SQLite.
func TestResolver_RestartRecreatesSetsAndReResolves(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 3600, "203.0.113.1"))

	a := createDomainAlias(t, am, "Example", "example.com")
	v4, v6 := aliases.DomainSetV4(a.ID), aliases.DomainSetV6(a.ID)

	// First run.
	f1 := newFakeIPSet()
	r1 := New(am, f1)
	r1.dc = testClient(d.addr)
	r1.Start()
	waitFor(t, "the first run to resolve", func() bool { return len(f1.snapshot(v4)) == 1 })
	r1.Stop()

	if st := r1.Status(a.ID); st != nil {
		t.Errorf("Status = %+v after Stop, want nil", st)
	}

	// Second run, fresh kernel — the configuration comes back from SQLite.
	f2 := newFakeIPSet()
	r2 := newTestResolver(t, am, f2, d.addr)
	r2.Start()

	waitFor(t, "the sets to be recreated and repopulated", func() bool {
		_, ok4 := f2.wasCreated(v4)
		_, ok6 := f2.wasCreated(v6)
		return ok4 && ok6 && len(f2.snapshot(v4)) == 1
	})
	if st := r2.Status(a.ID); st == nil || st.LastUpdate == "" {
		t.Errorf("want a fresh lastUpdate after restart, got %+v", st)
	}
}

func TestResolver_StopIsIdempotentAndStopsGoroutines(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	d.set("example.com", dns.TypeA, aRR(t, "example.com", 3600, "203.0.113.1"))
	f := newFakeIPSet()

	createDomainAlias(t, am, "Example", "example.com")
	r := New(am, f)
	r.dc = testClient(d.addr)
	r.Start()
	waitFor(t, "the first resolution", func() bool { return d.queryCount() > 0 })

	done := make(chan struct{})
	go func() { r.Stop(); r.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return; a resolver goroutine is stuck")
	}
}

// Non-domain aliases must be ignored entirely by the resolver.
func TestResolver_IgnoresOtherAliasTypes(t *testing.T) {
	am := initTestDB(t)
	d := newTestDNS(t)
	f := newFakeIPSet()

	host, err := am.Create(aliases.Alias{Name: "Hosts", Type: "host", Entries: []string{"10.0.0.1"}})
	if err != nil {
		t.Fatalf("create host alias: %v", err)
	}

	r := newTestResolver(t, am, f, d.addr)
	r.Start()

	if st := r.Status(host.ID); st != nil {
		t.Errorf("host alias is tracked by the DNS resolver: %+v", st)
	}
	r.Sync(host.ID) // must be a no-op, not a panic
	if d.queryCount() != 0 {
		t.Errorf("resolver issued %d DNS queries for non-domain aliases", d.queryCount())
	}
}

func TestResolver_StatusUnknownAliasIsNil(t *testing.T) {
	am := initTestDB(t)
	f := newFakeIPSet()
	r := newTestResolver(t, am, f, "127.0.0.1:1")
	r.Start()

	if st := r.Status(fmt.Sprintf("no-such-alias")); st != nil {
		t.Errorf("Status for an unknown alias = %+v, want nil", st)
	}
}
