package tunnel

import (
	"testing"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/peer"
)

func TestAWG31InterfacePersistenceRoundTrip(t *testing.T) {
	db.Close()
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	on := true
	settings := &peer.AWGSettings{
		Jc: 6, Jmin: 10, Jmax: 50, S1: 64, S2: 67, S3: 64, S4: 12,
		H1: "1-2", H2: "3-4", H3: "5-6", H4: "7-8",
		HeaderProtectionKey:    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		ContentPaddingAddition: "10-100", RekeyAfterTime: "100-120", RekeyTimeout: "3-7",
		RejectAfterTime: "150-180", KeepaliveTimeout: "5-15", MaxHandshakeAttempts: "15-20",
		RandomTrailers: &on, DisableCookies: &on,
	}
	_, err := Create(InterfaceInput{ID: "wg10", Name: "AWG3", Address: "10.8.0.1/24", ListenPort: 51820, Protocol: "amneziawg-3.1", PrivateKey: "private", PublicKey: "public", AWG2: settings})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadInterface("wg10")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Protocol != "amneziawg-3.1" || loaded.AWG2 == nil {
		t.Fatalf("unexpected interface: %+v", loaded)
	}
	if loaded.AWG2.HeaderProtectionKey != settings.HeaderProtectionKey || loaded.AWG2.RandomTrailers == nil || !*loaded.AWG2.RandomTrailers {
		t.Fatalf("AWG3 settings changed: %+v", loaded.AWG2)
	}
}
