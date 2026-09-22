package ipset

import (
	"os/exec"
	"strings"
	"testing"
)

func requireIPSet(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ipset"); err != nil {
		t.Skip("ipset binary not found, skipping kernel ipset test")
	}
}

func TestFamily(t *testing.T) {
	if got := family(false); got != "inet" {
		t.Errorf("family(false) = %q, want inet", got)
	}
	if got := family(true); got != "inet6" {
		t.Errorf("family(true) = %q, want inet6", got)
	}
}

func TestCreateTimeoutSet_ValidatesInput(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}

	if err := m.CreateTimeoutSet("9bad-name", false, 3600); err == nil {
		t.Error("want an error for a name the kernel would reject")
	}
	if err := m.CreateTimeoutSet(strings.Repeat("a", 32), false, 3600); err == nil {
		t.Error("want an error for a name over the 31-character limit")
	}
	if err := m.CreateTimeoutSet("good_name", false, 0); err == nil {
		t.Error("want an error for a non-positive default timeout")
	}
}

func TestAddTimeoutEntries_SkipsNothingToDo(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}

	n, err := m.AddTimeoutEntries("some_set", nil)
	if err != nil || n != 0 {
		t.Errorf("empty map: n=%d err=%v, want 0/nil", n, err)
	}

	// Entries that could never be valid addresses are dropped rather than
	// being interpolated into the restore script.
	n, err = m.AddTimeoutEntries("some_set", map[string]int{
		"":                  60,
		"   ":               60,
		"1.2.3.4; rm -rf /": 60,
		"1.2.3.4":           0,  // non-positive timeout
		"1.2.3.5":           -1, // negative timeout
	})
	if err != nil {
		t.Fatalf("AddTimeoutEntries: %v", err)
	}
	if n != 0 {
		t.Errorf("wrote %d entries, want all of them rejected", n)
	}
}

func TestAddTimeoutEntries_ValidatesSetName(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	if _, err := m.AddTimeoutEntries("9bad", map[string]int{"1.2.3.4": 60}); err == nil {
		t.Error("want an error for an invalid set name")
	}
}

// Round-trip against a real kernel: create a set, add entries with distinct
// timeouts, and read them back — including the incremental behaviour that a
// second add must not remove what the first one wrote.
func TestTimeoutSet_KernelRoundTrip(t *testing.T) {
	requireIPSet(t)
	m := &Manager{dataDir: t.TempDir()}

	const setName = "csc_dom_test_roundtrip_v4"
	if err := m.CreateTimeoutSet(setName, false, 3600); err != nil {
		t.Fatalf("CreateTimeoutSet: %v", err)
	}
	t.Cleanup(func() { m.DestroySet(setName) }) //nolint:errcheck

	if _, err := m.AddTimeoutEntries(setName, map[string]int{"203.0.113.1": 300}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := m.AddTimeoutEntries(setName, map[string]int{"203.0.113.2": 600}); err != nil {
		t.Fatalf("second add: %v", err)
	}

	entries := m.ListTimeoutEntries(setName)
	if len(entries) != 2 {
		t.Fatalf("entries = %v, want both addresses (the second add must not flush the first)", entries)
	}
	for _, e := range entries {
		if strings.Contains(e, "timeout") {
			t.Errorf("entry %q still carries the kernel's timeout suffix", e)
		}
	}
	if n := m.GetEntryCount(setName); n != 2 {
		t.Errorf("GetEntryCount = %d, want 2", n)
	}
}

func TestTimeoutSet_KernelIPv6(t *testing.T) {
	requireIPSet(t)
	m := &Manager{dataDir: t.TempDir()}

	const setName = "csc_dom_test_roundtrip_v6"
	if err := m.CreateTimeoutSet(setName, true, 3600); err != nil {
		t.Fatalf("CreateTimeoutSet(v6): %v", err)
	}
	t.Cleanup(func() { m.DestroySet(setName) }) //nolint:errcheck

	if _, err := m.AddTimeoutEntries(setName, map[string]int{"2001:db8::1": 300}); err != nil {
		t.Fatalf("add: %v", err)
	}
	entries := m.ListTimeoutEntries(setName)
	if len(entries) != 1 || entries[0] != "2001:db8::1" {
		t.Fatalf("entries = %v, want the IPv6 address", entries)
	}
}

func TestListSetNames_IncludesCreatedSet(t *testing.T) {
	requireIPSet(t)
	m := &Manager{dataDir: t.TempDir()}

	const setName = "csc_dom_test_listing_v4"
	if err := m.CreateTimeoutSet(setName, false, 3600); err != nil {
		t.Fatalf("CreateTimeoutSet: %v", err)
	}
	t.Cleanup(func() { m.DestroySet(setName) }) //nolint:errcheck

	found := false
	for _, n := range m.ListSetNames() {
		if n == setName {
			found = true
		}
	}
	if !found {
		t.Errorf("ListSetNames did not include %q", setName)
	}
}
