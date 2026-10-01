package tunnel

import (
	"net"
	"strings"
	"testing"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/peer"
	"github.com/alexnikon/cascade/internal/settings"
)

func TestDomainDNSPersistenceAndConfigPriority(t *testing.T) {
	db.Close()
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	iface, err := Create(InterfaceInput{ID: "wg12", Name: "to-fin", Address: "10.101.0.1/24", ListenPort: 51832, Protocol: "wireguard-1.0", DomainAliasDNS: true, DNS: "9.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadInterface(iface.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.DomainAliasDNS || loaded.DNS != "9.9.9.9" {
		t.Fatalf("roundtrip=%+v", loaded)
	}
	p := &peer.Peer{PrivateKey: "private", ClientAllowedIPs: "192.0.2.0/24"}
	m := newTestManager()
	cfg, err := m.BuildPeerRemoteConfig(loaded, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "DNS = 10.101.0.1\n") || !strings.Contains(cfg, "10.101.0.1/32") {
		t.Fatalf("config=%s", cfg)
	}
	loaded.DomainAliasDNS = false
	if err := loaded.save(); err != nil {
		t.Fatal(err)
	}
	cfg, err = m.BuildPeerRemoteConfig(loaded, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "DNS = 9.9.9.9\n") || strings.Contains(cfg, "10.101.0.1/32") {
		t.Fatalf("disabled config=%s", cfg)
	}
	loaded.DNS = ""
	global, err := settings.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = m.BuildPeerRemoteConfig(loaded, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "DNS = "+global.DNS+"\n") {
		t.Fatalf("global fallback=%s", cfg)
	}
}

func TestDomainDNSDefaultsOff(t *testing.T) {
	db.Close()
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	_, err := db.DB().Exec(`INSERT INTO interfaces (id,name,address,listen_port,private_key,public_key) VALUES ('wg12','to-fin','10.101.0.1/24',51832,'private','public')`)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadInterface("wg12")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DomainAliasDNS {
		t.Fatal("existing interface unexpectedly enabled DNS")
	}
}

func TestDomainDNSAllowsOnlyEnabledClients(t *testing.T) {
	iface := &TunnelInterface{peers: map[string]*peer.Peer{
		"active":   {PeerType: "client", Enabled: true, AllowedIPs: "10.101.0.2/32, 2001:db8::2/128"},
		"disabled": {PeerType: "client", Enabled: false, AllowedIPs: "10.101.0.3/32"},
		"s2s":      {PeerType: "interconnect", Enabled: true, AllowedIPs: "10.102.0.0/24"},
	}}
	for _, ip := range []string{"10.101.0.2", "2001:db8::2"} {
		if !iface.domainDNSAllows(net.ParseIP(ip)) {
			t.Errorf("client refused: %s", ip)
		}
	}
	for _, ip := range []string{"10.101.0.3", "10.102.0.2", "192.0.2.2"} {
		if iface.domainDNSAllows(net.ParseIP(ip)) {
			t.Errorf("unauthorized client allowed: %s", ip)
		}
	}
	iface.peers["active"].Enabled = false
	if iface.domainDNSAllows(net.ParseIP("10.101.0.2")) {
		t.Fatal("disabled peer remains authorized")
	}
}
