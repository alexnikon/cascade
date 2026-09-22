package aliases

import (
	"strings"
	"testing"
)

// ── Validation ────────────────────────────────────────────────────────────────

func TestNormalizeDomainEntries_Valid(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"plain name", []string{"youtube.com"}, []string{"youtube.com"}},
		{"subdomain", []string{"www.youtube.com"}, []string{"www.youtube.com"}},
		{"deep subdomain", []string{"youtubei.googleapis.com"}, []string{"youtubei.googleapis.com"}},
		{"short TLD", []string{"youtu.be"}, []string{"youtu.be"}},
		{"digits and hyphens", []string{"cdn-3.example-site.com"}, []string{"cdn-3.example-site.com"}},
		{"leading digit label", []string{"1st.example.com"}, []string{"1st.example.com"}},
		{"punycode", []string{"xn--80ak6aa92e.com"}, []string{"xn--80ak6aa92e.com"}},
		{"uppercase is lowercased", []string{"YouTube.COM"}, []string{"youtube.com"}},
		{"surrounding space trimmed", []string{"  youtube.com  "}, []string{"youtube.com"}},
		{"trailing dot accepted", []string{"youtube.com."}, []string{"youtube.com"}},
		{"blank entries dropped", []string{"youtube.com", "", "  "}, []string{"youtube.com"}},
		{
			"duplicates collapse, order preserved",
			[]string{"youtube.com", "youtu.be", "YOUTUBE.COM", "youtube.com."},
			[]string{"youtube.com", "youtu.be"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeDomainEntries(tt.in)
			if err != nil {
				t.Fatalf("normalizeDomainEntries(%v): %v", tt.in, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestNormalizeDomainEntries_Invalid(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		wantErrHas string
	}{
		{"wildcard", []string{"*.googlevideo.com"}, "wildcard"},
		{"bare asterisk", []string{"*"}, "wildcard"},
		{"embedded asterisk", []string{"foo.*.com"}, "wildcard"},
		{"IPv4 address", []string{"142.250.185.78"}, "IP address"},
		{"IPv6 address", []string{"2001:db8::1"}, "IP address"},
		{"no dot", []string{"localhost"}, "at least one dot"},
		{"empty label", []string{"foo..com"}, "empty label"},
		{"leading hyphen", []string{"-bad.example.com"}, "hyphen"},
		{"trailing hyphen", []string{"bad-.example.com"}, "hyphen"},
		{"underscore", []string{"bad_name.example.com"}, "invalid character"},
		{"space inside", []string{"bad name.example.com"}, "invalid character"},
		{"non-ASCII", []string{"пример.рф"}, "invalid character"},
		{"label too long", []string{strings.Repeat("a", 64) + ".com"}, "63 characters"},
		{"no entries at all", []string{}, "at least one domain"},
		{"only blanks", []string{"", "   "}, "at least one domain"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := normalizeDomainEntries(tt.in)
			if err == nil {
				t.Fatalf("normalizeDomainEntries(%v) = nil error, want one", tt.in)
			}
			if !strings.Contains(err.Error(), tt.wantErrHas) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErrHas)
			}
		})
	}
}

func TestNormalizeDomainEntries_NameTooLong(t *testing.T) {
	long := strings.Repeat("abcdefghij.", 24) + "com" // > 253 chars, all labels legal
	if _, err := normalizeDomainEntries([]string{long}); err == nil {
		t.Fatal("want an error for a name longer than 253 characters")
	}
}

func TestValidateType_AcceptsDomain(t *testing.T) {
	if err := validateType("domain"); err != nil {
		t.Errorf("validateType(domain) = %v, want nil", err)
	}
}

// ── Kernel set naming ─────────────────────────────────────────────────────────

func TestDomainSetNames(t *testing.T) {
	const id = "4ad41442-3622-4d0e-82b6-312b0cdf5e70"

	v4, v6 := DomainSetV4(id), DomainSetV6(id)
	if v4 != "csc_dom_4ad4144236224d0e_v4" {
		t.Errorf("DomainSetV4 = %q", v4)
	}
	if v6 != "csc_dom_4ad4144236224d0e_v6" {
		t.Errorf("DomainSetV6 = %q", v6)
	}
	if v4 == v6 {
		t.Error("the two address families must not share a set name")
	}
}

// The kernel rejects names longer than 31 characters, and ipset.Manager
// validates against exactly that.
func TestDomainSetNames_FitKernelLimit(t *testing.T) {
	for _, id := range []string{
		"4ad41442-3622-4d0e-82b6-312b0cdf5e70",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		"short",
		"",
	} {
		for _, name := range []string{DomainSetV4(id), DomainSetV6(id)} {
			if len(name) > 31 {
				t.Errorf("set name %q is %d chars, over the 31-char kernel limit", name, len(name))
			}
			if !IsDomainSet(name) {
				t.Errorf("IsDomainSet(%q) = false", name)
			}
		}
	}
}

func TestDomainSetNames_AreDeterministic(t *testing.T) {
	const id = "4ad41442-3622-4d0e-82b6-312b0cdf5e70"
	if DomainSetV4(id) != DomainSetV4(id) {
		t.Error("set names must be stable for the same alias ID")
	}
	if DomainSetV4(strings.ToUpper(id)) != DomainSetV4(id) {
		t.Error("set names must not depend on the ID's letter case")
	}
}

func TestIsDomainSet_IgnoresOtherSets(t *testing.T) {
	for _, name := range []string{"default", "premium", "ru_nets", "basic"} {
		if IsDomainSet(name) {
			t.Errorf("IsDomainSet(%q) = true, want false", name)
		}
	}
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

func TestDomainAlias_Create(t *testing.T) {
	m := initTestDB(t)

	a, err := m.Create(Alias{
		Name:    "YouTube",
		Type:    "domain",
		Entries: []string{"youtube.com", "www.youtube.com", "youtu.be"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if a.Type != "domain" {
		t.Errorf("Type = %q", a.Type)
	}
	if len(a.Entries) != 3 {
		t.Errorf("Entries = %v", a.Entries)
	}
	if a.EntryCount != 3 {
		t.Errorf("EntryCount = %d, want the number of configured domains", a.EntryCount)
	}
	if a.IPSetName != DomainSetV4(a.ID) {
		t.Errorf("IPSetName = %q, want %q", a.IPSetName, DomainSetV4(a.ID))
	}

	// And it round-trips through SQLite.
	got, err := m.GetByID(a.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Type != "domain" || len(got.Entries) != 3 || got.IPSetName != a.IPSetName {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestDomainAlias_CreateRejectsInvalidEntries(t *testing.T) {
	m := initTestDB(t)

	if _, err := m.Create(Alias{Name: "Bad", Type: "domain", Entries: []string{"*.example.com"}}); err == nil {
		t.Error("want Create to reject a wildcard entry")
	}
	if _, err := m.Create(Alias{Name: "Empty", Type: "domain", Entries: nil}); err == nil {
		t.Error("want Create to reject a domain alias with no entries")
	}
}

func TestDomainAlias_UpdateEntries(t *testing.T) {
	m := initTestDB(t)

	a, err := m.Create(Alias{Name: "YouTube", Type: "domain", Entries: []string{"youtube.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := m.Update(a.ID, Alias{Entries: []string{"youtube.com", "YOUTU.BE", "youtube.com"}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Entries) != 2 || updated.Entries[1] != "youtu.be" {
		t.Errorf("Entries = %v, want normalised and deduplicated", updated.Entries)
	}
	if updated.EntryCount != 2 {
		t.Errorf("EntryCount = %d, want 2", updated.EntryCount)
	}
}

func TestDomainAlias_UpdateRejectsInvalidEntries(t *testing.T) {
	m := initTestDB(t)

	a, err := m.Create(Alias{Name: "YouTube", Type: "domain", Entries: []string{"youtube.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.Update(a.ID, Alias{Entries: []string{"*.googlevideo.com"}}); err == nil {
		t.Fatal("want Update to reject a wildcard entry")
	}

	// The stored configuration must be untouched after a rejected update.
	got, _ := m.GetByID(a.ID)
	if len(got.Entries) != 1 || got.Entries[0] != "youtube.com" {
		t.Errorf("entries changed despite the rejected update: %v", got.Entries)
	}
}

// Step 10: renaming must not disturb the kernel object name.
func TestDomainAlias_RenameKeepsIPSetName(t *testing.T) {
	m := initTestDB(t)

	a, err := m.Create(Alias{Name: "OldName", Type: "domain", Entries: []string{"example.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	original := a.IPSetName

	renamed, err := m.Update(a.ID, Alias{Name: "NewName"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if renamed.Name != "NewName" {
		t.Errorf("Name = %q, want NewName", renamed.Name)
	}
	if renamed.IPSetName != original {
		t.Errorf("IPSetName changed on rename: %q → %q", original, renamed.IPSetName)
	}
	if renamed.IPSetName != DomainSetV4(a.ID) {
		t.Errorf("IPSetName no longer derives from the alias ID: %q", renamed.IPSetName)
	}
	// Entries survive a name-only update.
	if len(renamed.Entries) != 1 {
		t.Errorf("Entries = %v, want them preserved by a rename", renamed.Entries)
	}
}

func TestDomainAlias_Delete(t *testing.T) {
	m := initTestDB(t)

	a, err := m.Create(Alias{Name: "YouTube", Type: "domain", Entries: []string{"youtube.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Delete(a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := m.GetByID(a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got != nil {
		t.Errorf("alias still present after delete: %+v", got)
	}
}

// With a real kernel available, deletion must actually remove both sets.
func TestDomainAlias_DeleteDestroysBothKernelSets(t *testing.T) {
	requireIPSet(t)
	m := initTestDB(t)

	a, err := m.Create(Alias{Name: "YouTube", Type: "domain", Entries: []string{"youtube.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v4, v6 := DomainSetV4(a.ID), DomainSetV6(a.ID)
	timeout := 3600
	if err := m.ipsetMgr.CreateTimeoutSet(v4, false, timeout); err != nil {
		t.Fatalf("create v4 set: %v", err)
	}
	if err := m.ipsetMgr.CreateTimeoutSet(v6, true, timeout); err != nil {
		t.Fatalf("create v6 set: %v", err)
	}

	if err := m.Delete(a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, name := range m.ipsetMgr.ListSetNames() {
		if name == v4 || name == v6 {
			t.Errorf("kernel set %q survived alias deletion", name)
		}
	}
}

// ── Firewall integration ──────────────────────────────────────────────────────

// This is the whole Firewall/PBR integration: a domain alias must present
// itself to the rule compiler exactly like any other ipset alias.
func TestDomainAlias_GetMatchSpecIsAnIPSetMatch(t *testing.T) {
	m := initTestDB(t)

	a, err := m.Create(Alias{Name: "YouTube", Type: "domain", Entries: []string{"youtube.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	spec, err := m.GetMatchSpec(a.ID)
	if err != nil {
		t.Fatalf("GetMatchSpec: %v", err)
	}
	if spec.Type != "ipset" {
		t.Fatalf("MatchSpec.Type = %q, want ipset", spec.Type)
	}
	if spec.Name != DomainSetV4(a.ID) {
		t.Errorf("MatchSpec.Name = %q, want the v4 set %q", spec.Name, DomainSetV4(a.ID))
	}
	if len(spec.Entries) != 0 {
		t.Errorf("MatchSpec.Entries = %v, want empty for an ipset match", spec.Entries)
	}
}

// A rename must not change what the compiler matches on.
func TestDomainAlias_MatchSpecSurvivesRename(t *testing.T) {
	m := initTestDB(t)

	a, _ := m.Create(Alias{Name: "OldName", Type: "domain", Entries: []string{"example.com"}})
	before, err := m.GetMatchSpec(a.ID)
	if err != nil {
		t.Fatalf("GetMatchSpec: %v", err)
	}
	if _, err := m.Update(a.ID, Alias{Name: "NewName"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, err := m.GetMatchSpec(a.ID)
	if err != nil {
		t.Fatalf("GetMatchSpec: %v", err)
	}
	if before.Name != after.Name {
		t.Errorf("match set changed on rename: %q → %q", before.Name, after.Name)
	}
}

// ── Regression: the existing alias types are untouched ────────────────────────

func TestExistingAliasTypes_MatchSpecUnchanged(t *testing.T) {
	m := initTestDB(t)

	host, err := m.Create(Alias{Name: "Hosts", Type: "host", Entries: []string{"10.0.0.1", "10.0.0.2"}})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	network, err := m.Create(Alias{Name: "Nets", Type: "network", Entries: []string{"192.168.0.0/16"}})
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	set, err := m.Create(Alias{Name: "RuNets", Type: "ipset"})
	if err != nil {
		t.Fatalf("create ipset: %v", err)
	}
	group, err := m.Create(Alias{Name: "Combined", Type: "group", MemberIDs: []string{host.ID, network.ID}})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	cases := []struct {
		name        string
		id          string
		wantType    string
		wantName    string
		wantEntries []string
	}{
		{"host stays a CIDR match", host.ID, "cidr", "", []string{"10.0.0.1", "10.0.0.2"}},
		{"network stays a CIDR match", network.ID, "cidr", "", []string{"192.168.0.0/16"}},
		{"ipset still uses its display-derived name", set.ID, "ipset", "runets", nil},
		{"group still merges member entries", group.ID, "cidr", "", []string{"10.0.0.1", "10.0.0.2", "192.168.0.0/16"}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := m.GetMatchSpec(tt.id)
			if err != nil {
				t.Fatalf("GetMatchSpec: %v", err)
			}
			if spec.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", spec.Type, tt.wantType)
			}
			if spec.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", spec.Name, tt.wantName)
			}
			if len(spec.Entries) != len(tt.wantEntries) {
				t.Fatalf("Entries = %v, want %v", spec.Entries, tt.wantEntries)
			}
			for i := range spec.Entries {
				if spec.Entries[i] != tt.wantEntries[i] {
					t.Fatalf("Entries = %v, want %v", spec.Entries, tt.wantEntries)
				}
			}
		})
	}
}

func TestExistingAliasTypes_PortMatchSpecUnchanged(t *testing.T) {
	m := initTestDB(t)

	p, err := m.Create(Alias{Name: "WebPorts", Type: "port", Entries: []string{"tcp:80", "tcp:443"}})
	if err != nil {
		t.Fatalf("create port alias: %v", err)
	}
	specs, err := m.GetPortMatchSpec(p.ID)
	if err != nil {
		t.Fatalf("GetPortMatchSpec: %v", err)
	}
	if len(specs) != 1 || specs[0].Proto != "tcp" || specs[0].Ports != "80,443" || !specs[0].Multiport {
		t.Errorf("port match spec = %+v, want a single multiport tcp 80,443", specs)
	}
}

func TestExistingAliasTypes_StillValidate(t *testing.T) {
	for _, typ := range []string{"host", "network", "ipset", "group", "port", "port-group", "client-group"} {
		if err := validateType(typ); err != nil {
			t.Errorf("validateType(%q) = %v, want nil", typ, err)
		}
	}
	if err := validateType("bogus"); err == nil {
		t.Error("validateType(bogus) = nil, want an error")
	}
}

// A domain alias must not be mistaken for an ipset alias by the upload/generate
// paths, which only understand CIDR content.
func TestDomainAlias_RejectedByIPSetOnlyOperations(t *testing.T) {
	m := initTestDB(t)

	a, err := m.Create(Alias{Name: "YouTube", Type: "domain", Entries: []string{"youtube.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.UploadFromFile(a.ID, "/nonexistent"); err == nil {
		t.Error("want UploadFromFile to reject a domain alias")
	}
}
