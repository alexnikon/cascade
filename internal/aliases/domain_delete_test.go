package aliases

// Deleting an alias that firewall rules still reference.
//
// The kernel refuses "ipset destroy" while an iptables rule matches on the set,
// so deletion has to happen in one specific order: drop the row, re-emit the
// kernel rules without it, then destroy the sets. Before that ordering existed,
// destroy was attempted first, failed silently, and left the set orphaned in the
// kernel until the next restart swept it up.

import (
	"os"
	"path/filepath"
	"testing"
)

// saveFilesFor returns the .save paths DestroySet removes for these sets, which
// is how the test observes that destroy was actually attempted for each one —
// no kernel required.
func seedSaveFiles(t *testing.T, dir string, names ...string) []string {
	t.Helper()
	var paths []string
	for _, n := range names {
		p := filepath.Join(dir, n+".save")
		if err := os.WriteFile(p, []byte("# placeholder\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
		paths = append(paths, p)
	}
	return paths
}

func TestDeleteDomainAlias_DestroysBothFamiliesAfterDroppingKernelRefs(t *testing.T) {
	m, dir := initTestDBWithDir(t)

	a, err := m.Create(Alias{Name: "TEST-domains", Type: "domain", Entries: []string{"example.com"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v4, v6 := DomainSetV4(a.ID), DomainSetV6(a.ID)
	saves := seedSaveFiles(t, dir, v4, v6)

	// The rebuild hook stands in for the firewall manager. It records when it
	// ran and what the alias table looked like at that moment.
	var rebuilt bool
	var aliasStillPresent bool
	var savesAtRebuild int
	m.SetKernelRefsRebuilder(func() error {
		rebuilt = true
		got, err := m.GetByID(a.ID)
		if err == nil && got != nil {
			aliasStillPresent = true
		}
		for _, p := range saves {
			if _, err := os.Stat(p); err == nil {
				savesAtRebuild++
			}
		}
		return nil
	})

	if err := m.Delete(a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if !rebuilt {
		t.Fatal("kernel rules were never re-emitted — the sets would still be referenced")
	}
	if aliasStillPresent {
		t.Error("the alias row still existed during the rebuild — the rebuild could re-emit a match on its sets")
	}
	if savesAtRebuild != len(saves) {
		t.Errorf("%d of %d sets were already destroyed when the rebuild ran; "+
			"destroy must come after the kernel stops referencing them", len(saves)-savesAtRebuild, len(saves))
	}
	for _, p := range saves {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still exists — the set was not destroyed", filepath.Base(p))
		}
	}
	if got, _ := m.GetByID(a.ID); got != nil {
		t.Error("the alias row survived deletion")
	}
}

// An ipset alias owns one set and takes the same path.
func TestDeleteIPSetAlias_DropsKernelRefsBeforeDestroying(t *testing.T) {
	m, dir := initTestDBWithDir(t)

	a, err := m.Create(Alias{Name: "TEST-bignets", Type: "ipset"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saves := seedSaveFiles(t, dir, a.IPSetName)

	order := []string{}
	m.SetKernelRefsRebuilder(func() error {
		order = append(order, "rebuild")
		if _, err := os.Stat(saves[0]); err != nil {
			order = append(order, "destroy-happened-first")
		}
		return nil
	})

	if err := m.Delete(a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(order) != 1 || order[0] != "rebuild" {
		t.Errorf("ordering = %v, want the rebuild alone, before any destroy", order)
	}
	if _, err := os.Stat(saves[0]); err == nil {
		t.Error("the set was not destroyed")
	}
}

// Aliases with no kernel objects must not trigger a rebuild: nothing references
// a kernel set, so there is nothing to strip.
func TestDeleteCIDRAlias_DoesNotRebuildKernelRules(t *testing.T) {
	m, _ := initTestDBWithDir(t)

	a, err := m.Create(Alias{Name: "TEST-plain", Type: "network", Entries: []string{"10.0.0.0/8"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rebuilt := false
	m.SetKernelRefsRebuilder(func() error { rebuilt = true; return nil })

	if err := m.Delete(a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rebuilt {
		t.Error("a CIDR alias owns no kernel sets; deleting it should not rebuild the chains")
	}
}

// Deletion must still work when no firewall manager is wired up (tests, and the
// window before main.go finishes starting).
func TestDeleteDomainAlias_WithoutRebuilderStillDestroysSets(t *testing.T) {
	m, dir := initTestDBWithDir(t)

	a, err := m.Create(Alias{Name: "TEST-norebuild", Type: "domain", Entries: []string{"example.net"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saves := seedSaveFiles(t, dir, DomainSetV4(a.ID), DomainSetV6(a.ID))

	if err := m.Delete(a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, p := range saves {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still exists", filepath.Base(p))
		}
	}
}
