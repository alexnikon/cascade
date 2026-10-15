package tunnel

import (
	"testing"
	"time"

	"github.com/alexnikon/cascade/internal/db"
	"github.com/alexnikon/cascade/internal/webaccess"
)

func TestWebAccessProtectsTunnelBeforeMutation(t *testing.T) {
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	_, err := db.DB().Exec(`INSERT INTO interfaces(id,name,address,enabled) VALUES('wg12','admin','10.8.2.1/24',1)`)
	if err != nil {
		t.Fatal(err)
	}
	m, err := webaccess.New(db.DB(), webaccess.Dependencies{
		Prepare: func(p webaccess.Policy) (webaccess.Prepared, error) { return webaccess.Prepare(db.DB(), p) },
		Apply:   func([]webaccess.Rule) error { return nil }, Trial: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	webaccess.SetManager(m)
	t.Cleanup(func() { webaccess.SetManager(nil); m.Close() })
	if _, err := m.Trial(webaccess.Policy{Enabled: true, InterfaceID: "wg12"}); err != nil {
		t.Fatal(err)
	}
	iface := &TunnelInterface{ID: "wg12", Address: "10.8.2.1/24", Enabled: true}
	address := "10.9.2.1/24"
	for name, operation := range map[string]func() error{"delete": iface.Delete, "stop": iface.Stop, "restart": iface.Restart, "subnet": func() error { return iface.Update(InterfaceUpdate{Address: &address}) }} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil {
				t.Fatal("administrative interface mutation accepted")
			}
		})
	}
	if iface.Address != "10.8.2.1/24" || !iface.Enabled {
		t.Fatal("protected interface was mutated")
	}
	var count int
	if err := db.DB().QueryRow("SELECT count(*) FROM interfaces WHERE id='wg12'").Scan(&count); err != nil || count != 1 {
		t.Fatal("interface row was removed")
	}
}
