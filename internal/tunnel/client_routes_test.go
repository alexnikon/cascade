package tunnel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/ipset"
	"github.com/alexnikon/cascade/internal/peer"
	"github.com/alexnikon/cascade/internal/settings"
)

func TestClientAliasExportRefresh(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel command runner only executes on Linux")
	}
	dir := t.TempDir()
	// Substitute only the ipset executable; exercise the production command parser,
	// database bindings and config builder without changing the host kernel.
	snapshot := filepath.Join(dir, "snapshot")
	binary := filepath.Join(dir, "ipset")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = save ]; then cat \"$CASCADE_TEST_IPSET_SNAPSHOT\"; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CASCADE_TEST_IPSET_SNAPSHOT", snapshot)
	if err := db.Init(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { aliases.SetInstance(nil); db.Close() })
	im, err := ipset.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	am := aliases.New(im)
	aliases.SetInstance(am)
	a, err := am.Create(aliases.Alias{Name: "split_routes", Type: "ipset"})
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot := func(prefix string) {
		t.Helper()
		content := "create " + a.IPSetName + " hash:net family inet\n"
		if prefix != "" {
			content += "add " + a.IPSetName + " " + prefix + "\n"
		}
		if err := os.WriteFile(snapshot, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSnapshot("0.0.0.0/1")
	if _, err = settings.UpdateSettings(map[string]any{"defaultClientAllowedIPsMode": "exclude-ipset", "defaultClientAllowedIPsAliasId": a.ID}); err != nil {
		t.Fatal(err)
	}
	defaults, err := settings.GetPeerDefaults()
	if err != nil {
		t.Fatal(err)
	}
	p := &peer.Peer{Name: "Split", PeerType: "client", PrivateKey: "private", AllowedIPs: "10.8.0.2/32", ClientAllowedIPs: defaults.ClientAllowedIPs, ClientAllowedIPsMode: defaults.ClientAllowedIPsMode, ClientAllowedIPsAliasID: defaults.ClientAllowedIPsAliasID}
	iface := &TunnelInterface{ID: "wg10", Address: "10.8.0.1/24", PublicKey: "pub", ListenPort: 51820, Protocol: "wireguard-1.0"}
	m := &Manager{WGHost: "vpn.example.com"}
	config, err := m.BuildPeerRemoteConfig(iface, p)
	if err != nil || !strings.Contains(config, "AllowedIPs = 128.0.0.0/1, ::/0\n") {
		t.Fatalf("config=%s err=%v", config, err)
	}
	writeSnapshot("128.0.0.0/1")
	config, err = m.BuildPeerRemoteConfig(iface, p)
	if err != nil || !strings.Contains(config, "AllowedIPs = 0.0.0.0/1, ::/0\n") {
		t.Fatalf("refreshed=%s err=%v", config, err)
	}
	if p.ClientAllowedIPs != defaults.ClientAllowedIPs {
		t.Fatal("export mutated stored manual routes")
	}
	p.PrivateKey = ""
	config, err = m.BuildPeerRemoteConfig(iface, p)
	if err != nil || !strings.Contains(config, "AllowedIPs = 0.0.0.0/1, ::/0\n") {
		t.Fatalf("keyless=%s err=%v", config, err)
	}
	writeSnapshot("0.0.0.0/1")
	iface.DomainAliasDNS = true
	config, err = m.BuildPeerRemoteConfig(iface, p)
	if err != nil || !strings.Contains(config, "AllowedIPs = 128.0.0.0/1, ::/0, 10.8.0.1/32\n") {
		t.Fatalf("DNS=%s err=%v", config, err)
	}
	writeSnapshot("")
	if _, err = m.BuildPeerRemoteConfig(iface, p); err == nil {
		t.Fatal("empty ipset fell back to full tunnel")
	}
	if err := os.Remove(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = m.BuildPeerRemoteConfig(iface, p); err == nil {
		t.Fatal("read failure fell back to full tunnel")
	}
	p.ClientAllowedIPsMode = "manual"
	if _, err = m.BuildPeerRemoteConfig(iface, p); err != nil {
		t.Fatal(err)
	}
}
