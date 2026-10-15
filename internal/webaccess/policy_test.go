package webaccess

import (
	"database/sql"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func fixture(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	_, err = d.Exec(`CREATE TABLE web_access(id INTEGER PRIMARY KEY,policy TEXT); INSERT INTO web_access VALUES(1,'{"enabled":false}');
CREATE TABLE interfaces(id TEXT PRIMARY KEY,name TEXT,address TEXT,enabled BOOLEAN);
INSERT INTO interfaces VALUES('wg12','admin','10.8.2.1/24, fd00:12::1/64',1),('wg13','client','10.10.1.1/24',1);`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func policy() Policy {
	return Policy{Enabled: true, InterfaceID: "wg12", Clients: []Client{{Name: "bot", IP: "172.18.0.2", Interface: "br-bot"}, {Name: "remote", IP: "203.0.113.8", Interface: "ens1"}}}
}
func manager(t *testing.T, d *sql.DB, trial time.Duration, apply func([]Rule) error) *Manager {
	t.Helper()
	m, err := New(d, Dependencies{Prepare: func(p Policy) (Prepared, error) { return Prepare(d, p) }, Apply: apply, Trial: trial})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestOriginClassificationAndConfirmation(t *testing.T) {
	d := fixture(t)
	m := manager(t, d, time.Minute, func([]Rule) error { return nil })
	if _, err := m.Trial(policy()); err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]string{"10.8.2.2": "admin", "fd00:12::2": "admin", "::ffff:10.8.2.2": "admin", "172.18.0.2": "machine", "203.0.113.8": "machine", "10.10.1.2": "denied", "10.255.255.1": "denied", "198.51.100.1": "denied"} {
		if got := m.Classify(netip.MustParseAddr(raw)); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	if _, err := m.Confirm(netip.MustParseAddr("172.18.0.2")); err == nil {
		t.Fatal("machine confirmed trial")
	}
	if _, err := m.Confirm(netip.MustParseAddr("10.8.2.2")); err != nil {
		t.Fatal(err)
	}
	m.Close()
	restarted := manager(t, d, time.Minute, func([]Rule) error { return nil })
	if restarted.State().Pending || !restarted.State().Policy.Enabled {
		t.Fatal("confirmed policy was not restored")
	}
}

func TestTrialRestartAndTimeoutRestoreConfirmedPolicy(t *testing.T) {
	d := fixture(t)
	m := manager(t, d, 40*time.Millisecond, func([]Rule) error { return nil })
	if _, err := m.Trial(policy()); err != nil {
		t.Fatal(err)
	}
	// A new process reads only the last confirmed policy.
	restarted := manager(t, d, time.Minute, func([]Rule) error { return nil })
	if restarted.State().Policy.Enabled {
		t.Fatal("unconfirmed policy survived restart")
	}
	deadline := time.Now().Add(time.Second)
	for m.State().Pending && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if m.State().Pending || m.State().Policy.Enabled {
		t.Fatal("trial did not expire")
	}
}

func TestDisableTrialKeepsIngressGuardUntilVPNConfirmation(t *testing.T) {
	d := fixture(t)
	var applied []Rule
	m := manager(t, d, time.Minute, func(r []Rule) error { applied = append([]Rule{}, r...); return nil })
	if _, err := m.Trial(policy()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Confirm(netip.MustParseAddr("10.8.2.2")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Trial(Policy{}); err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("disable trial removed the confirmation VPN ingress guard")
	}
	if m.CanManageOrigin(netip.MustParseAddr("172.18.0.2")) {
		t.Fatal("machine can manage a disabling trial")
	}
	if !m.CanManageOrigin(netip.MustParseAddr("10.8.2.2")) {
		t.Fatal("previous VPN cannot confirm a disabling trial")
	}
	if _, err := m.Confirm(netip.MustParseAddr("10.10.1.2")); err == nil {
		t.Fatal("other VPN confirmed disable")
	}
	if _, err := m.Cancel(); err != nil {
		t.Fatal(err)
	}
	if !m.State().Policy.Enabled {
		t.Fatal("cancel did not restore restricted policy")
	}
	if _, err := m.Trial(Policy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Confirm(netip.MustParseAddr("10.8.2.2")); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatal("confirmed disable left ingress guards active")
	}
}

func TestKernelFailureFailsClosedAndRetriesRollback(t *testing.T) {
	d := fixture(t)
	fail := false
	m := manager(t, d, time.Minute, func([]Rule) error {
		if fail {
			return errors.New("kernel failure")
		}
		return nil
	})
	fail = true
	if _, err := m.Trial(policy()); err == nil {
		t.Fatal("kernel failure accepted")
	}
	if m.Classify(netip.MustParseAddr("10.8.2.2")) != "denied" {
		t.Fatal("partial rollback failure opened HTTP")
	}
	fail = false
	m.expire()
	if m.State().Pending || m.State().Error != "" {
		t.Fatal("rollback retry failed")
	}
}

func TestStartupFailureClosesHTTPWithoutPreventingVPNStartup(t *testing.T) {
	d := fixture(t)
	if _, err := d.Exec(`UPDATE web_access SET policy='{"enabled":true,"interfaceId":"missing-vpn","clients":[]}' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	m, err := New(d, Dependencies{Prepare: func(p Policy) (Prepared, error) { return Prepare(d, p) }, Apply: func([]Rule) error { return errors.New("must not apply") }})
	if err == nil || m == nil {
		t.Fatal("startup must return a closed gate alongside the diagnostic")
	}
	defer m.Close()
	if m.Classify(netip.MustParseAddr("10.8.2.2")) != "denied" || m.State().Error == "" {
		t.Fatal("startup failure opened web access")
	}
}

func TestProtectedInterfaceCannotChange(t *testing.T) {
	d := fixture(t)
	m := manager(t, d, time.Minute, func([]Rule) error { return nil })
	SetManager(m)
	t.Cleanup(func() { SetManager(nil) })
	if _, err := m.Trial(policy()); err != nil {
		t.Fatal(err)
	}
	if unlock, err := LockInterfaceChange("wg12"); err == nil {
		unlock()
		t.Fatal("administrative interface was not protected")
	}
	unlock, err := LockInterfaceChange("wg13")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestInvalidPoliciesAndDualStackGuards(t *testing.T) {
	d := fixture(t)
	for _, client := range []Client{{IP: "127.0.0.1", Interface: "lo"}, {IP: "10.8.2.9", Interface: "ens1"}, {IP: "172.18.0.0/16", Interface: "br-bot"}, {IP: "169.254.169.254", Interface: "ens1"}, {IP: "203.0.113.8", Interface: "ens1;id"}} {
		p := policy()
		p.Clients = []Client{client}
		if _, err := Prepare(d, p); err == nil {
			t.Errorf("accepted invalid client %+v", client)
		}
	}
	p, err := Prepare(d, policy())
	if err != nil {
		t.Fatal(err)
	}
	v4, v6 := RestoreScript(p.Rules, false), RestoreScript(p.Rules, true)
	for _, part := range []string{"10.8.2.0/24 ! -i wg12 -p tcp --dport 443 -j DROP", "172.18.0.2/32 ! -i br-bot", "-p udp --dport 443"} {
		if !strings.Contains(v4, part) {
			t.Errorf("missing guard: %s", part)
		}
	}
	if !strings.Contains(v6, "fd00:12::/64 ! -i wg12") || strings.Contains(v6, "172.18.") {
		t.Fatal("incorrect IPv6 guards")
	}
	if strings.Contains(v4, "ACCEPT") || strings.Contains(v4, "FORWARD") {
		t.Fatal("web guards affect unrelated firewall behavior")
	}
}
