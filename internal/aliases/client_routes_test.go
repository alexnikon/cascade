package aliases

import (
	"strings"
	"testing"

	"github.com/alexnikon/cascade/internal/db"
)

func TestClientRoutingReferenceProtection(t *testing.T) {
	m := initTestDB(t)
	a := seedIPSetAlias(t, "split_routes")
	if _, err := db.DB().Exec(`INSERT INTO settings(key,value) VALUES ('defaultClientAllowedIPsMode','exclude-ipset'), ('defaultClientAllowedIPsAliasId',?)`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(a.ID); err == nil || !strings.Contains(err.Error(), "Global Settings") {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Update(a.ID, Alias{Name: "renamed_split_routes", Type: "ipset"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Update(a.ID, Alias{Type: "network"}); err == nil {
		t.Fatal("type change accepted")
	}
	if _, err := db.DB().Exec(`UPDATE settings SET value='manual' WHERE key='defaultClientAllowedIPsMode'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`INSERT INTO interfaces(id,name,address,listen_port) VALUES ('wg10','Routes','10.8.0.1/24',51820)`); err != nil {
		t.Fatal(err)
	}
	// Minimal legacy rows get the manual routing mode from the migration default.
	if _, err := db.DB().Exec(`INSERT INTO peers(id,interface_id,name,public_key) VALUES ('routing_peer','wg10','Routing','pub')`); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.DB().QueryRow(`SELECT client_allowed_ips_mode FROM peers WHERE id='routing_peer'`).Scan(&mode); err != nil || mode != "manual" {
		t.Fatalf("legacy default %q: %v", mode, err)
	}
	if _, err := db.DB().Exec(`UPDATE peers SET client_allowed_ips_mode='exclude-ipset',client_allowed_ips_alias_id=? WHERE id='routing_peer'`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(a.ID); err == nil || !strings.Contains(err.Error(), "client peer") {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.DB().Exec(`UPDATE peers SET client_allowed_ips_mode='manual',client_allowed_ips_alias_id='' WHERE id='routing_peer'`); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestClientRoutesUnavailableAndWrongType(t *testing.T) {
	m := initTestDB(t)
	if _, err := m.ClientAllowedIPs(""); err == nil {
		t.Fatal("empty ID accepted")
	}
	a, err := m.Create(Alias{Name: "manual_routes", Type: "network", Entries: []string{"192.0.2.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ClientAllowedIPs(a.ID); err == nil {
		t.Fatal("network alias accepted")
	}
	a = seedIPSetAlias(t, "missing_kernel_routes")
	if _, err = m.ClientAllowedIPs(a.ID); err == nil {
		t.Fatal("missing kernel ipset accepted")
	}
}
