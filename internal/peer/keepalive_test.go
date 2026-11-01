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
