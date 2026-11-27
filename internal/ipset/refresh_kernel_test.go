package ipset

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRefreshKernel(t *testing.T) {
	if os.Getenv("CASCADE_TEST_KERNEL_IPSET") != "1" {
		t.Skip("requires an isolated Linux network namespace with ipset")
	}
	run := func(name string, args ...string) {
		t.Helper()
		if output, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, output)
		}
	}
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	const name = "cascade_refresh_test"
	defer m.DestroySet(name)
	prefixes := filepath.Join(dir, "prefixes")
	replace := func(entries string) error {
		t.Helper()
		if err := os.WriteFile(prefixes, []byte(entries), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := m.LoadAndSaveFromFile(name, prefixes)
		return err
	}
	if err := replace("192.0.2.0/24\n"); err != nil {
		t.Fatal(err)
	}
	rule := []string{"OUTPUT", "-m", "set", "--match-set", name, "dst", "-j", "ACCEPT"}
	run("iptables", append([]string{"-A"}, rule...)...)
	defer run("iptables", append([]string{"-D"}, rule...)...)
	if err := replace("198.51.100.0/24\n"); err != nil {
		t.Fatal(err)
	}
	run("iptables", append([]string{"-C"}, rule...)...)
	run("ipset", "test", name, "198.51.100.1")
	if err := replace("invalid-prefix/999\n"); err == nil {
		t.Fatal("accepted invalid prefixes")
	}
	run("ipset", "test", name, "198.51.100.1")
	if err := os.Mkdir(filepath.Join(dir, name+".save.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := replace("203.0.113.0/24\n"); err == nil {
		t.Fatal("accepted a failed snapshot write")
	}
	run("ipset", "test", name, "198.51.100.1")
	run("ipset", "flush", name)
	if _, err := New(dir); err != nil {
		t.Fatal(err)
	}
	run("ipset", "test", name, "198.51.100.1")
}
