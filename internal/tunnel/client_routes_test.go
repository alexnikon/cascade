package tunnel

import (
	"strings"
	"testing"

	"github.com/alexnikon/cascade/internal/aliases"
	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/peer"
	"github.com/alexnikon/cascade/internal/settings"
)

func TestManualClientExportsDoNotRequireAliases(t *testing.T) {
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	aliases.SetInstance(nil)
	t.Cleanup(func() { aliases.SetInstance(nil) })
	if _, err := settings.UpdateSettings(map[string]any{"defaultClientAllowedIPs": "198.51.100.0/24"}); err != nil {
		t.Fatal(err)
	}
	defaults, err := settings.GetPeerDefaults()
	if err != nil || defaults.ClientAllowedIPs != "198.51.100.0/24" {
		t.Fatalf("defaults=%+v err=%v", defaults, err)
	}
	iface := &TunnelInterface{ID: "wg10", Address: "10.8.0.1/24", PublicKey: "pub", ListenPort: 51820, Protocol: "wireguard-1.0"}
	m := &Manager{WGHost: "vpn.example.com"}
	for _, key := range []string{"private", ""} {
		for _, manual := range []string{"203.0.113.0/24", ""} {
			p := &peer.Peer{Name: "Manual", PeerType: "client", PrivateKey: key, AllowedIPs: "10.8.0.2/32", ClientAllowedIPs: manual}
			want := manual
			if want == "" {
				want = defaults.ClientAllowedIPs
			}
			for _, dns := range []bool{false, true} {
				iface.DomainAliasDNS = dns
				allowed := want
				if dns {
					allowed += ", 10.8.0.1/32"
				}
				config, err := m.BuildPeerRemoteConfig(iface, p)
				if err != nil || !strings.Contains(config, "AllowedIPs = "+allowed+"\n") {
					t.Fatalf("config=%s err=%v", config, err)
				}
				if p.ClientAllowedIPs != manual {
					t.Fatal("export mutated stored manual routes")
				}
			}
		}
	}
}
