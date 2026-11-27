package peer

import (
	"strings"
	"testing"

	"github.com/alexnikon/cascade/internal/db"
)

func TestNormalizeKeepaliveRange(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"", ""}, {" 005-025 ", "5-25"}, {"1-65535", "1-65535"}, {"25-25", "25-25"}} {
		got, err := NormalizeKeepaliveRange(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v", tc.input, got, err)
		}
	}
	for _, value := range []string{"0-5", "25-5", "1-65536", "25", "-5-25", "5 - 25", "1.5-2", "1-2\nPostUp=x", "999999999999999999999-2"} {
		if _, err := NormalizeKeepaliveRange(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestKeepaliveRangePersistenceAndAtomicUpdate(t *testing.T) {
	initTestDB(t)
	p := createTestPeer(t, "wg-range")
	value := "5-25"
	if _, err := UpdatePeer(p.ID, PeerUpdate{PersistentKeepaliveV3: &value}); err == nil {
		t.Fatal("accepted range on WireGuard")
	}
	if _, err := db.DB().Exec("UPDATE interfaces SET protocol='amneziawg-3.1' WHERE id=?", p.InterfaceID); err != nil {
		t.Fatal(err)
	}
	value = " 005-025 "
	p, err := UpdatePeer(p.ID, PeerUpdate{PersistentKeepaliveV3: &value})
	if err != nil {
		t.Fatal(err)
	}
	if p.PersistentKeepaliveV3 != "5-25" || p.PersistentKeepalive != 25 {
		t.Fatalf("unexpected peer: %+v", p)
	}
	renamed := "renamed"
	if _, err := UpdatePeer(p.ID, PeerUpdate{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	invalid := "25-5"
	badName := "must-not-stick"
	if _, err := UpdatePeer(p.ID, PeerUpdate{Name: &badName, PersistentKeepaliveV3: &invalid}); err == nil {
		t.Fatal("accepted invalid range")
	}
	saved, err := GetPeer(p.ID)
	if err != nil || saved.Name != renamed || saved.PersistentKeepaliveV3 != "5-25" {
		t.Fatalf("partial mutation: %+v, %v", saved, err)
	}
	empty := ""
	cleared, err := UpdatePeer(p.ID, PeerUpdate{PersistentKeepaliveV3: &empty})
	if err != nil || cleared.PersistentKeepaliveV3 != "" || cleared.KeepaliveValue("amneziawg-3.1") != "25" {
		t.Fatalf("clear: %+v, %v", cleared, err)
	}
	for _, kind := range []string{"client", "interconnect"} {
		created, err := CreatePeer(p.InterfaceID, PeerInput{Name: kind, PeerType: kind, PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=", AllowedIPs: "10.8.0.3/32", PersistentKeepaliveV3: "5-25"})
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := GetPeer(created.ID)
		if err != nil || loaded.PersistentKeepaliveV3 != "5-25" {
			t.Fatalf("create round-trip: %+v, %v", loaded, err)
		}
	}
}

func TestKeepaliveRangeConfigSelection(t *testing.T) {
	p := &Peer{Name: "range", Enabled: true, PublicKey: "pub", PrivateKey: "priv", AllowedIPs: "10.8.0.2/32", Address: "10.8.0.2/32", PersistentKeepalive: 25, PersistentKeepaliveV3: "5-25"}
	for _, protocol := range []string{"amneziawg-3.1", "amneziawg-2.0", "wireguard-1.0"} {
		want := "PersistentKeepalive = 25"
		if protocol == "amneziawg-3.1" {
			want = "PersistentKeepalive = 5-25"
		}
		iface := InterfaceData{Protocol: protocol}
		for _, config := range []string{p.ToWgConfig(protocol), p.generateCompleteConfig(iface), p.generateTemplateConfig(iface)} {
			if !strings.Contains(config, want+"\n") {
				t.Fatalf("%s: missing %q", protocol, want)
			}
		}
	}
}

func TestDisabledPersistentKeepalivePersists(t *testing.T) {
	initTestDB(t)
	p := createTestPeer(t, "wg-off")
	off := 0
	if _, err := UpdatePeer(p.ID, PeerUpdate{PersistentKeepalive: &off}); err != nil {
		t.Fatal(err)
	}
	loaded, err := GetPeer(p.ID)
	if err != nil || loaded.PersistentKeepalive != 0 {
		t.Fatalf("disabled interval was lost: %v", err)
	}
	for _, value := range []string{loaded.ToWgConfig("wireguard-1.0"), loaded.GenerateRemoteConfig(InterfaceData{})} {
		if !strings.Contains(value, "PersistentKeepalive = 0\n") {
			t.Fatal("config did not explicitly disable persistent keepalive")
		}
	}
}

func TestClientKeepaliveTimeout(t *testing.T) {
	initTestDB(t)
	p := createTestPeer(t, "wg-timeout")
	timeout := " 005-015 "
	if _, err := UpdatePeer(p.ID, PeerUpdate{ClientKeepaliveTimeout: &timeout}); err == nil {
		t.Fatal("accepted a timeout on WireGuard")
	}
	if _, err := db.DB().Exec("UPDATE interfaces SET protocol='amneziawg-3.1' WHERE id=?", p.InterfaceID); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdatePeer(p.ID, PeerUpdate{ClientKeepaliveTimeout: &timeout})
	if err != nil || updated.ClientKeepaliveTimeout != "5-15" {
		t.Fatalf("normalize: %+v, %v", updated, err)
	}
	name := "must not change"
	for _, bad := range []string{"-1", "20-10", "65536", "1-65536", "2-3\nPostUp=x"} {
		if _, err := UpdatePeer(p.ID, PeerUpdate{Name: &name, ClientKeepaliveTimeout: &bad}); err == nil {
			t.Fatalf("accepted timeout %q", bad)
		}
	}
	saved, err := GetPeer(p.ID)
	if err != nil || saved.Name != p.Name || saved.ClientKeepaliveTimeout != "5-15" {
		t.Fatalf("invalid edit changed the peer: %+v, %v", saved, err)
	}
	settings := &AWGSettings{KeepaliveTimeout: "8-10"}
	iface := InterfaceData{Protocol: "amneziawg-3.1", Settings: settings, Host: "2001:db8::1", ListenPort: 51820}
	for _, cfg := range []string{saved.generateCompleteConfig(iface), saved.generateTemplateConfig(iface)} {
		if !strings.Contains(cfg, "KeepaliveTimeout = 5-15\n") ||
			strings.Count(cfg, "KeepaliveTimeout = ") != 1 ||
			!strings.Contains(cfg, "Endpoint = [2001:db8::1]:51820\n") {
			t.Fatal("client config has an incorrect timer or IPv6 endpoint")
		}
	}
	if settings.KeepaliveTimeout != "8-10" || strings.Contains(saved.ToWgConfig(iface.Protocol), "KeepaliveTimeout =") {
		t.Fatal("client override changed server settings")
	}
	empty := ""
	cleared, err := UpdatePeer(p.ID, PeerUpdate{ClientKeepaliveTimeout: &empty})
	if err != nil || cleared.ClientKeepaliveTimeout != "" ||
		!strings.Contains(cleared.GenerateRemoteConfig(iface), "KeepaliveTimeout = 8-10\n") {
		t.Fatalf("clear did not restore inheritance: %v", err)
	}
}

func TestScalarKeepaliveValidationAndLegacyExport(t *testing.T) {
	initTestDB(t)
	existing := createTestPeer(t, "wg-scalar")
	for _, value := range []int{-1, 65536} {
		if _, err := CreatePeer(existing.InterfaceID, PeerInput{PersistentKeepalive: value}); err == nil {
			t.Fatalf("accepted invalid create value %d", value)
		}
		name := "should-not-stick"
		if _, err := UpdatePeer(existing.ID, PeerUpdate{Name: &name, PersistentKeepalive: &value}); err == nil {
			t.Fatalf("accepted invalid update value %d", value)
		}
		loaded, err := GetPeer(existing.ID)
		if err != nil || loaded.Name != existing.Name || loaded.PersistentKeepalive != existing.PersistentKeepalive {
			t.Fatalf("invalid update changed peer: %+v, %v", loaded, err)
		}
		loaded.PersistentKeepalive = value
		for _, protocol := range []string{"wireguard-1.0", "amneziawg-2.0", "amneziawg-3.1"} {
			iface := InterfaceData{Protocol: protocol}
			for _, config := range []string{loaded.ToWgConfig(protocol), loaded.GenerateRemoteConfig(iface), loaded.generateTemplateConfig(iface)} {
				if !strings.Contains(config, "PersistentKeepalive = 0\n") {
					t.Fatalf("%s export did not disable invalid legacy keepalive %d", protocol, value)
				}
			}
		}
	}
}
