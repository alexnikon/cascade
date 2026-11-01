package tunnel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/awgparams"
	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/peer"
)

func TestParseKeepaliveRange(t *testing.T) {
	base := "[Interface]\nPrivateKey = key\nAddress = 10.8.0.2/32\n%s\n[Peer]\nPublicKey = pub\nPersistentKeepalive = %s\n"
	for _, tc := range []struct {
		marker, value, want string
		fails               bool
	}{
		{"HeaderProtectionKey = key", "5-25", "5-25", false},
		{"HeaderProtectionKey = key", "25", "", false},
		{"HeaderProtectionKey = key", "25-5", "", true},
		{"Jc = 6", "5-25", "", true},
		{"", "5-25", "", true},
	} {
		parsed, err := ParseWGConf(fmt.Sprintf(base, tc.marker, tc.value))
		if tc.fails {
			if err == nil {
				t.Fatalf("accepted %q / %q", tc.marker, tc.value)
			}
			continue
		}
		if err != nil || parsed.PeerKeepaliveV3 != tc.want || parsed.Peers[0].KeepaliveV3 != tc.want {
			t.Fatalf("parsed=%+v, err=%v", parsed, err)
		}
	}
}

// Run only in the disposable Docker network namespace created by test-awg-keepalive.sh.
func TestKeepaliveRangeRuntime(t *testing.T) {
	if os.Getenv("CASCADE_AWG_KEEPALIVE_TEST") != "1" {
		t.Skip("requires isolated AWG runtime")
	}
	t.Setenv("WG_QUICK_USERSPACE_IMPLEMENTATION", "amneziawg-go")
	// Firewall rules have their own kernel tests. Keep this amd64 AWG check
	// independent of nftables support under Docker's CPU emulation.
	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "iptables-nft"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+":"+os.Getenv("PATH"))
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	aliases.SetInstance(aliases.New(nil))
	params, err := awgparams.GenerateAWG3(awgparams.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManager()
	iface, err := m.CreateInterface(CreateInput{Protocol: "amneziawg-3.1", Address: "10.88.0.1/24", DisableRoutes: true, AWG2: params})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := peer.GenerateKeys("awg")
	if err != nil {
		t.Fatal(err)
	}
	p, err := iface.AddPeer(peer.PeerInput{Name: "range", PublicKey: keys.PublicKey, AllowedIPs: "10.88.0.2/32", PersistentKeepalive: 25, PersistentKeepaliveV3: "5-25"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("amneziawg-go", iface.ID).CombinedOutput(); err != nil {
		t.Fatalf("start AWG: %v: %s", err, out)
	}
	t.Cleanup(func() { _, _ = exec.Command("ip", "link", "del", iface.ID).CombinedOutput() })
	path := filepath.Join(t.TempDir(), "sync.conf")
	if err := os.WriteFile(path, []byte(iface.generateSyncConfig()), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("awg", "setconf", iface.ID, path).CombinedOutput(); err != nil {
		t.Fatalf("setconf: %v: %s", err, out)
	}
	assertRuntime := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			out, err := exec.Command("awg", "show", iface.ID, "persistent-keepalive").Output()
			if err == nil && strings.HasSuffix(strings.TrimSpace(string(out)), "\t"+want) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("runtime did not apply keepalive %s", want)
	}
	assertRuntime("5-25")
	iface.Enabled = true
	for _, value := range []string{"11-17", ""} {
		if _, err := iface.UpdatePeer(p.ID, peer.PeerUpdate{PersistentKeepaliveV3: &value}); err != nil {
			t.Fatal(err)
		}
		want := value
		if want == "" {
			want = "25"
		}
		assertRuntime(want)
	}
	// Check native JSON restoration and both .conf import modes with real tools.
	value := "5-25"
	if _, err := iface.UpdatePeer(p.ID, peer.PeerUpdate{PersistentKeepaliveV3: &value}); err != nil {
		t.Fatal(err)
	}
	assertRuntime("5-25")
	exported, err := peer.GetPeer(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"interface": cascadeBackupIface{PrivateKey: iface.PrivateKey, PublicKey: iface.PublicKey, Address: "10.89.0.1/24", Protocol: iface.Protocol, DisableRoutes: true, AWG2: params},
		"peers":     []*peer.Peer{exported},
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := m.ImportInterface(ImportInterfaceInput{RawJSON: string(payload), ListenPort: 51840})
	if err != nil {
		t.Fatal(err)
	}
	if restored.StartError != nil {
		t.Fatal(restored.StartError)
	}
	t.Cleanup(func() { _ = restored.Interface.Stop() })
	checkImported := func(iface *TunnelInterface) {
		t.Helper()
		loaded, err := LoadInterface(iface.ID)
		if err != nil {
			t.Fatal(err)
		}
		peers := loaded.GetAllPeers()
		if len(peers) != 1 || peers[0].PersistentKeepaliveV3 != "5-25" {
			t.Fatalf("range lost on import: %+v", peers)
		}
		out, err := exec.Command("awg", "show", iface.ID, "persistent-keepalive").Output()
		if err != nil || !strings.HasSuffix(strings.TrimSpace(string(out)), "\t5-25") {
			t.Fatalf("imported runtime keepalive: %s, %v", out, err)
		}
	}
	checkImported(restored.Interface)
	conf := strings.Replace(iface.generateWgConfig(), "10.88.0.1/24", "10.90.0.1/24", 1)
	uplink, err := m.ImportConf("range-uplink", conf)
	if err != nil {
		t.Fatal(err)
	}
	if uplink.StartError != nil {
		t.Fatal(uplink.StartError)
	}
	t.Cleanup(func() { _ = uplink.Interface.Stop() })
	checkImported(uplink.Interface)
	conf = strings.Replace(conf, "10.90.0.1/24", "10.91.0.1/24", 1)
	conf = strings.Replace(conf, "ListenPort = 51830", "ListenPort = 51850", 1)
	server, err := m.ImportConfAsServer("range-server", conf)
	if err != nil {
		t.Fatal(err)
	}
	if server.StartError != nil {
		t.Fatal(server.StartError)
	}
	t.Cleanup(func() { _ = server.Interface.Stop() })
	checkImported(server.Interface)

}

func TestKeepaliveRangeImportRejectsBeforeCreation(t *testing.T) {
	m := newTestManager()
	for _, raw := range []string{
		`{"interface":{"privateKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","publicKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","address":"10.80.0.1/24","protocol":"wireguard-1.0"},"peers":[{"persistentKeepaliveV3":"5-25"}]}`,
		`{"interface":{"privateKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","publicKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","address":"10.80.0.1/24","protocol":"amneziawg-3.1"},"peers":[{"persistentKeepaliveV3":"25-5"}]}`,
	} {
		if _, err := m.ImportInterface(ImportInterfaceInput{RawJSON: raw, ListenPort: 51820}); err == nil || !strings.Contains(err.Error(), "persistentKeepaliveV3") {
			t.Fatalf("expected range rejection, got %v", err)
		}
		if len(m.interfaces) != 0 {
			t.Fatal("invalid import created an interface")
		}
	}
}
