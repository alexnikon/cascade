package nat

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/util"
)

// natKernel models iptables' set-like installation via -C before -A. Docker
// verification also executes each command against its isolated network namespace.
type natKernel struct {
	lines   map[string]bool
	deletes []string
	real    bool
	chain   string
}

type natRuleAbsent struct{}

func (natRuleAbsent) Error() string { return "rule absent" }
func (natRuleAbsent) ExitCode() int { return 1 }

func newLifecycleManager(t *testing.T) (*Manager, *natKernel) {
	t.Helper()
	m := initTestDB(t)
	m.am = aliases.New(nil)
	k := &natKernel{lines: make(map[string]bool), real: os.Getenv("CASCADE_NAT_KERNEL_TEST") == "1"}
	if k.real {
		k.chain = fmt.Sprintf("CNAT%x", time.Now().UnixNano())
		if _, err := util.ExecDefault("iptables-nft -t nat -N " + k.chain); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = util.ExecDefault("iptables-nft -t nat -F " + k.chain)
			_, _ = util.ExecDefault("iptables-nft -t nat -X " + k.chain)
		})
	}
	m.exec = func(cmd string, log bool) (string, error) {
		key := strings.Replace(strings.Replace(cmd, "-C POSTROUTING", "-A POSTROUTING", 1), "-D POSTROUTING", "-A POSTROUTING", 1)
		if k.real {
			if _, err := util.Exec(strings.ReplaceAll(cmd, "POSTROUTING", k.chain), util.DefaultTimeout, log); err != nil {
				return "", err
			}
		}
		switch {
		case strings.Contains(cmd, "-C POSTROUTING"):
			if !k.lines[key] {
				return "", natRuleAbsent{}
			}
		case strings.Contains(cmd, "-A POSTROUTING"):
			k.lines[key] = true
		case strings.Contains(cmd, "-D POSTROUTING"):
			if !k.lines[key] {
				return "", fmt.Errorf("duplicate deletion")
			}
			delete(k.lines, key)
			k.deletes = append(k.deletes, cmd)
		default:
			return "", fmt.Errorf("unexpected command: %s", cmd)
		}
		return "", nil
	}
	return m, k
}

func (k *natKernel) assertSources(t *testing.T, sources ...string) {
	t.Helper()
	if len(k.lines) != len(sources) {
		t.Fatalf("kernel lines = %v, want sources %v", k.lines, sources)
	}
	for _, source := range sources {
		found := false
		for line := range k.lines {
			if strings.Contains(line, "-s "+source+" ") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing source %s in %v", source, k.lines)
		}
	}
	if k.real {
		out, err := util.ExecSilent("iptables-nft -t nat -S " + k.chain)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "-A "+k.chain+" ") {
				count++
			}
		}
		if count != len(k.lines) {
			t.Fatalf("real kernel has %d lines, model has %d: %s", count, len(k.lines), out)
		}
		for line := range k.lines {
			if _, err := util.ExecSilent(strings.ReplaceAll(strings.Replace(line, "-A POSTROUTING", "-C POSTROUTING", 1), "POSTROUTING", k.chain)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func addLifecycleRule(t *testing.T, m *Manager, name, source, aliasID, kind string) *NatRule {
	t.Helper()
	inp := NatRuleInput{Name: name, Source: source, SourceAliasID: aliasID, OutInterface: "eth0", Type: kind}
	if kind == "SNAT" {
		inp.ToSource = "192.0.2.10"
	}
	r, err := m.AddRule(inp)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNATSharedLineLifecycle(t *testing.T) {
	for _, kind := range []string{"MASQUERADE", "SNAT"} {
		for _, operation := range []string{"delete", "disable", "update"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				m, k := newLifecycleManager(t)
				first := addLifecycleRule(t, m, "first", "10.80.0.0/24", "", kind)
				second := addLifecycleRule(t, m, "second", "10.80.0.0/24", "", kind)
				var err error
				switch operation {
				case "delete":
					err = m.DeleteRule(first.ID)
				case "disable":
					_, err = m.ToggleRule(first.ID, false)
				case "update":
					_, err = m.UpdateRule(first.ID, NatRuleInput{Name: "updated", Source: "10.81.0.0/24", OutInterface: "eth0", Type: kind, ToSource: first.ToSource})
				}
				if err != nil {
					t.Fatal(err)
				}
				if operation == "update" {
					k.assertSources(t, "10.80.0.0/24", "10.81.0.0/24")
				} else {
					k.assertSources(t, "10.80.0.0/24")
				}
				if len(k.deletes) != 0 {
					t.Fatalf("shared line was deleted: %v", k.deletes)
				}
				if err := m.DeleteRule(second.ID); err != nil {
					t.Fatal(err)
				}
				if operation == "update" {
					k.assertSources(t, "10.81.0.0/24")
				} else {
					k.assertSources(t)
				}
				if len(k.deletes) != 1 {
					t.Fatalf("last owner should delete exactly once: %v", k.deletes)
				}
			})
		}
	}
}

func createLifecycleAlias(t *testing.T, m *Manager, a aliases.Alias) *aliases.Alias {
	t.Helper()
	got, err := m.am.Create(a)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestNATNestedAliasBatchLifecycle(t *testing.T) {
	for _, externalOwner := range []bool{false, true} {
		t.Run(fmt.Sprintf("externalOwner=%v", externalOwner), func(t *testing.T) {
			m, k := newLifecycleManager(t)
			leaf := createLifecycleAlias(t, m, aliases.Alias{Name: "leaf", Type: "network", Entries: []string{"10.80.0.0/24", "10.82.0.0/24"}})
			inner := createLifecycleAlias(t, m, aliases.Alias{Name: "inner", Type: "group", MemberIDs: []string{leaf.ID}})
			outer := createLifecycleAlias(t, m, aliases.Alias{Name: "outer", Type: "group", MemberIDs: []string{leaf.ID}})
			// Current validation disallows nested groups; seed a legacy/imported tree.
			if _, err := db.DB().Exec("UPDATE aliases SET member_ids = ? WHERE id = ?", `["`+inner.ID+`"]`, outer.ID); err != nil {
				t.Fatal(err)
			}
			addLifecycleRule(t, m, "first", "", outer.ID, "MASQUERADE")
			addLifecycleRule(t, m, "second", "", inner.ID, "MASQUERADE")
			unrelated := addLifecycleRule(t, m, "unrelated", "10.83.0.0/24", "", "MASQUERADE")
			if externalOwner {
				addLifecycleRule(t, m, "external", "10.80.0.0/24", "", "MASQUERADE")
			}
			disabled := addLifecycleRule(t, m, "disabled", "10.82.0.0/24", "", "MASQUERADE")
			if _, err := m.ToggleRule(disabled.ID, false); err != nil {
				t.Fatal(err)
			}
			k.deletes = nil
			matched, err := m.rulesReferencingAlias(leaf.ID)
			if err != nil || len(matched) != 2 {
				t.Fatalf("matched = %v, error = %v", matched, err)
			}
			m.RemoveForAlias(leaf.ID)
			if externalOwner {
				k.assertSources(t, "10.80.0.0/24", unrelated.Source)
			} else {
				k.assertSources(t, unrelated.Source)
			}
			wantDeletes := 2
			if externalOwner {
				wantDeletes = 1
			}
			if len(k.deletes) != wantDeletes {
				t.Fatalf("deletes = %v, want %d unique deletions", k.deletes, wantDeletes)
			}
			if _, err := m.am.Update(leaf.ID, aliases.Alias{Entries: []string{"10.81.0.0/24"}}); err != nil {
				t.Fatal(err)
			}
			m.RestoreForAlias(leaf.ID)
			if externalOwner {
				k.assertSources(t, "10.80.0.0/24", "10.81.0.0/24", unrelated.Source)
			} else {
				k.assertSources(t, "10.81.0.0/24", unrelated.Source)
			}
			before := len(k.deletes)
			m.ReapplyAll()
			if len(k.deletes)-before != len(k.lines) {
				t.Fatalf("ReapplyAll did not delete each shared line once: %v", k.deletes[before:])
			}
			if externalOwner {
				k.assertSources(t, "10.80.0.0/24", "10.81.0.0/24", unrelated.Source)
			} else {
				k.assertSources(t, "10.81.0.0/24", unrelated.Source)
			}
		})
	}
}

func TestNATRemovalPlansBeforeExecuting(t *testing.T) {
	for _, failure := range []string{"database", "alias", "cycle"} {
		t.Run(failure, func(t *testing.T) {
			m, k := newLifecycleManager(t)
			rule := addLifecycleRule(t, m, "first", "10.80.0.0/24", "", "MASQUERADE")
			switch failure {
			case "database":
				if _, err := db.DB().Exec("DROP TABLE nat_rules"); err != nil {
					t.Fatal(err)
				}
			case "alias":
				leaf := createLifecycleAlias(t, m, aliases.Alias{Name: "leaf", Type: "network", Entries: []string{"10.81.0.0/24"}})
				addLifecycleRule(t, m, "protected", "", leaf.ID, "MASQUERADE")
				if _, err := db.DB().Exec("DROP TABLE aliases"); err != nil {
					t.Fatal(err)
				}
			case "cycle":
				leaf := createLifecycleAlias(t, m, aliases.Alias{Name: "leaf", Type: "network", Entries: []string{"10.81.0.0/24"}})
				group := createLifecycleAlias(t, m, aliases.Alias{Name: "cycle", Type: "group", MemberIDs: []string{leaf.ID}})
				if _, err := db.DB().Exec("UPDATE aliases SET member_ids = ? WHERE id = ?", `["`+group.ID+`"]`, group.ID); err != nil {
					t.Fatal(err)
				}
				found, err := m.aliasTreeContains(group.ID, "unrelated", make(map[string]bool))
				if err != nil || found {
					t.Fatalf("cyclic traversal = %v, %v", found, err)
				}
				if _, err := db.DB().Exec("UPDATE nat_rules SET source_alias_id = ? WHERE id = ?", group.ID, rule.ID); err != nil {
					t.Fatal(err)
				}
				rule.SourceAliasID = group.ID
			}
			if err := m.removeRule(rule); err == nil {
				t.Fatal("expected planning failure")
			}
			if len(k.deletes) != 0 {
				t.Fatalf("planning failure deleted kernel lines: %v", k.deletes)
			}
		})
	}
}

func TestNATCommandFailurePreservesConfiguration(t *testing.T) {
	for _, operation := range []string{"delete", "disable", "update"} {
		t.Run(operation, func(t *testing.T) {
			m, k := newLifecycleManager(t)
			rule := addLifecycleRule(t, m, "first", "10.80.0.0/24", "", "MASQUERADE")
			runner := m.exec
			m.exec = func(cmd string, log bool) (string, error) {
				if strings.Contains(cmd, "-C POSTROUTING") {
					return "", fmt.Errorf("iptables unavailable")
				}
				return runner(cmd, log)
			}
			var err error
			switch operation {
			case "delete":
				err = m.DeleteRule(rule.ID)
			case "disable":
				_, err = m.ToggleRule(rule.ID, false)
			case "update":
				_, err = m.UpdateRule(rule.ID, NatRuleInput{Name: "new", Source: "10.81.0.0/24", OutInterface: "eth0", Type: "MASQUERADE"})
			}
			if err == nil {
				t.Fatal("expected command failure")
			}
			saved, err := m.GetRule(rule.ID)
			if err != nil || saved == nil || !saved.Enabled || saved.Source != rule.Source {
				t.Fatalf("configuration changed after failure: %+v, %v", saved, err)
			}
			if len(k.deletes) != 0 {
				t.Fatalf("deleted after failed check: %v", k.deletes)
			}
			k.assertSources(t, rule.Source)
		})
	}
}
