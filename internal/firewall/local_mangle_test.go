package firewall

import (
	"strings"
	"testing"

	"github.com/alexnikon/cascade/internal/db"
)

// localFlags drops rules that are pinned to an inbound interface: there is no
// input interface in the OUTPUT hook.
func TestLocalFlags_DropsInterfaceBoundRules(t *testing.T) {
	if _, ok := localFlags(&Rule{Interface: "wg10"}, " -i wg10 -d 1.2.3.4"); ok {
		t.Error("a rule bound to wg10 must not be installed in the OUTPUT chain")
	}
	for _, iface := range []string{"", "any"} {
		flags := " -m set --match-set csc_dom_abc_v4 dst"
		got, ok := localFlags(&Rule{Interface: iface}, flags)
		if !ok {
			t.Fatalf("interface %q: rule should be eligible for the OUTPUT chain", iface)
		}
		if got != flags {
			t.Errorf("interface %q: flags = %q, want them passed through unchanged", iface, got)
		}
	}
}

func TestWireGuardListenPorts_ReadsConfiguredInterfaces(t *testing.T) {
	initTestDB(t)

	if _, err := db.DB().Exec(
		`INSERT INTO interfaces (id, name, address, listen_port) VALUES
		 ('wg10','wg10','10.99.0.1/24',51830),
		 ('wg11','wg11','172.16.0.2/32',51831)`); err != nil {
		t.Fatalf("seed interfaces: %v", err)
	}

	got := wireGuardListenPorts()
	if len(got) != 2 {
		t.Fatalf("ports = %v, want both listen ports", got)
	}
	seen := map[int]bool{}
	for _, p := range got {
		seen[p] = true
	}
	if !seen[51830] || !seen[51831] {
		t.Errorf("ports = %v, want 51830 and 51831", got)
	}
}

// The guard prologue is what keeps a PBR rule from routing the tunnel's own
// transport packets back into the tunnel.
func TestLocalGuardCommands_CoverMarkedAndTunnelTraffic(t *testing.T) {
	joined := strings.Join(localGuardCommands([]int{51832, 51830}), "\n")

	if !strings.Contains(joined, "-A "+localMangleChain+" -m mark ! --mark 0 -j RETURN") {
		t.Errorf("missing already-marked guard in:\n%s", joined)
	}
	for _, port := range []string{"51832", "51830"} {
		if !strings.Contains(joined, "-A "+localMangleChain+" -p udp --sport "+port+" -j RETURN") {
			t.Errorf("missing WireGuard transport guard for port %s in:\n%s", port, joined)
		}
	}
	// The mark guard must come first, or a socket that already chose its routing
	// table could be overridden by a rule below it.
	if !strings.HasPrefix(joined, "iptables-nft -t mangle -A "+localMangleChain+" -m mark") {
		t.Errorf("the already-marked guard must be the first command, got:\n%s", joined)
	}
}

// Rules default to PREROUTING-only so an upgrade never starts policy-routing
// traffic that this host generates itself.
func TestApplyToLocal_DefaultsOff(t *testing.T) {
	mgr, _ := initTestDB(t)

	r, err := mgr.AddRule(RuleInput{Name: "no-local", Action: "accept"})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if r.ApplyToLocal {
		t.Error("applyToLocal must default to false")
	}
}

// The flag is meaningless without a gateway and is cleared, exactly like
// fallbackToDefault.
func TestApplyToLocal_RequiresGateway(t *testing.T) {
	mgr, _ := initTestDB(t)

	r, err := mgr.AddRule(RuleInput{Name: "no-gw", Action: "accept", ApplyToLocal: true})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if r.ApplyToLocal {
		t.Error("applyToLocal must be cleared on a rule with no gateway")
	}
}
