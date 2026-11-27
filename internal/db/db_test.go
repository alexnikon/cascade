package db

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMigration49RetiresClientAliasBindingsAndPreservesData(t *testing.T) {
	if err := Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	d := DB()
	// Reconstruct the v48 schema, including active and stale alias bindings.
	_, err := d.Exec(`
ALTER TABLE peers ADD COLUMN client_allowed_ips_mode TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE peers ADD COLUMN client_allowed_ips_alias_id TEXT NOT NULL DEFAULT '';
DROP TABLE auth_challenges;
DROP TABLE browser_sessions;
ALTER TABLE users DROP COLUMN auth_version;
ALTER TABLE peers DROP COLUMN client_keepalive_timeout;
DELETE FROM schema_migrations WHERE version>=49;
INSERT INTO interfaces(id,name,address) VALUES('wg10','Clients','10.8.0.1/24');
INSERT INTO aliases(id,name,type,entries,ipset_name) VALUES('routes','excluded_routes','ipset','["192.0.2.0/24"]','cascade_routes');
INSERT INTO firewall_rules(id,name,source,action) VALUES('rule','Alias rule','{"type":"alias","aliasId":"routes"}','ACCEPT');
INSERT INTO settings(key,value) VALUES
 ('defaultClientAllowedIPsMode','exclude-ipset'), ('defaultClientAllowedIPsAliasId','routes'),
 ('defaultClientAllowedIPs','198.51.100.0/24'), ('routerName','Preserved router');
INSERT INTO peers(id,interface_id,public_key,private_key,client_allowed_ips,client_allowed_ips_mode,client_allowed_ips_alias_id) VALUES
 ('bound','wg10','bound-public','bound-private','203.0.113.0/24','exclude-ipset','routes'),
 ('stale','wg10','stale-public','stale-private','','exclude-ipset','missing'),
 ('manual','wg10','manual-public','manual-private','192.0.2.0/24','manual','');
`)
	if err != nil {
		t.Fatal(err)
	}
	// Compare complete unaffected rows, including keys, counters and rule metadata.
	snapshot := func(query string) string {
		t.Helper()
		rows, err := d.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "%#v\n", values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	var columns []string
	rows, err := d.Query(`SELECT name FROM pragma_table_info('peers') WHERE name NOT IN ('client_allowed_ips_mode','client_allowed_ips_alias_id') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	queries := []string{
		"SELECT " + strings.Join(columns, ",") + " FROM peers ORDER BY id",
		"SELECT * FROM aliases ORDER BY id",
		"SELECT * FROM firewall_rules ORDER BY id",
		"SELECT * FROM settings WHERE key NOT IN ('defaultClientAllowedIPsMode','defaultClientAllowedIPsAliasId') ORDER BY key",
	}
	before := make([]string, len(queries))
	for i, query := range queries {
		before[i] = snapshot(query)
	}
	if err := runMigrations(d); err != nil {
		t.Fatal(err)
	}
	for i, query := range queries {
		if after := snapshot(query); after != before[i] {
			t.Fatalf("migration changed preserved data for %s:\nbefore %s\nafter %s", query, before[i], after)
		}
	}
	var retired int
	if err := d.QueryRow(`SELECT count(*) FROM pragma_table_info('peers') WHERE name IN ('client_allowed_ips_mode','client_allowed_ips_alias_id')`).Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("retired columns=%d err=%v", retired, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM settings WHERE key IN ('defaultClientAllowedIPsMode','defaultClientAllowedIPsAliasId')`).Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("retired settings=%d err=%v", retired, err)
	}
	if err := runMigrations(d); err != nil {
		t.Fatalf("repeated startup: %v", err)
	}
}

func TestOpen_CreatesDBFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "cascade-db-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	t.Cleanup(func() {
		Close()
		os.RemoveAll(dir)
	})

	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// DB handle must be non-nil.
	d := DB()
	if d == nil {
		t.Fatal("DB() returned nil after Init()")
	}

	// Ping to confirm connection works.
	if err := d.Ping(); err != nil {
		t.Fatalf("DB().Ping(): %v", err)
	}
}

// ── Table existence checks ────────────────────────────────────────────────────

// expectedTables includes the tables required by the current schema.
var expectedTables = []string{
	"settings",
	"templates",
	"interfaces",
	"peers",
	"routes",
	"nat_rules",
	"aliases",
	"firewall_rules",
	"gateways",
	"gateway_groups",
	"schema_migrations",
	"web_access",
	"users",
	"api_tokens",
	"browser_sessions",
	"auth_challenges",
}

func TestMigration46RetiresConfigurationLinkTokens(t *testing.T) {
	if err := Init(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	d := DB()
	_, err := d.Exec(`INSERT INTO interfaces(id,name,address) VALUES('legacy','legacy','10.8.0.1/24');
INSERT INTO peers(id,interface_id,public_key,one_time_link) VALUES('legacy-peer','legacy','public-key','legacy-token');
DROP TABLE web_access;
ALTER TABLE peers ADD COLUMN client_allowed_ips_mode TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE peers ADD COLUMN client_allowed_ips_alias_id TEXT NOT NULL DEFAULT '';
ALTER TABLE peers DROP COLUMN persistent_keepalive_v3;
ALTER TABLE templates DROP COLUMN mtu;
DROP TABLE auth_challenges;
DROP TABLE browser_sessions;
ALTER TABLE users DROP COLUMN auth_version;
ALTER TABLE peers DROP COLUMN client_keepalive_timeout;
DELETE FROM schema_migrations WHERE version>=46;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(d); err != nil {
		t.Fatal(err)
	}
	var token, policy string
	if err := d.QueryRow("SELECT one_time_link FROM peers WHERE id='legacy-peer'").Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Fatal("migration retained a public configuration token")
	}
	if err := d.QueryRow("SELECT policy FROM web_access WHERE id=1").Scan(&policy); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(policy, `"enabled":false`) {
		t.Fatal("migration enabled web access restriction by default")
	}
}

func TestOpen_AllTablesExist(t *testing.T) {
	dir, err := os.MkdirTemp("", "cascade-db-tables-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	t.Cleanup(func() {
		Close()
		os.RemoveAll(dir)
	})

	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	d := DB()
	for _, table := range expectedTables {
		t.Run(table, func(t *testing.T) {
			var name string
			err := d.QueryRow(
				`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
			).Scan(&name)
			if err != nil {
				t.Errorf("table %q not found in sqlite_master: %v", table, err)
			} else if name != table {
				t.Errorf("expected table name %q, got %q", table, name)
			}
		})
	}
}

// ── Migration version ─────────────────────────────────────────────────────────

func TestOpen_MigrationVersionIsLatest(t *testing.T) {
	dir, err := os.MkdirTemp("", "cascade-db-migver-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	t.Cleanup(func() {
		Close()
		os.RemoveAll(dir)
	})

	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	d := DB()
	var version int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}

	// Every migration has a consecutive version.
	want := len(migrations)
	if version != want {
		t.Errorf("schema version = %d, want %d", version, want)
	}
}

func TestMigrationV41PreservesLegacyRowsAndAddsNullableAWG3Fields(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	d := DB()
	if _, err := d.Exec(`INSERT INTO templates (id,name,jc,jmin,jmax,s1,s2,s3,s4,h1,h2,h3,h4) VALUES ('legacy','Legacy',6,10,50,64,67,64,4,'1-2','3-4','5-6','7-8')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO interfaces (id,name,address,listen_port,protocol,private_key,public_key,jc,jmin,jmax,s1,s2,s3,s4) VALUES ('wg10','Legacy','10.8.0.1/24',51820,'amneziawg-2.0','private','public',6,10,50,64,67,64,4)`); err != nil {
		t.Fatal(err)
	}
	var version string
	var templateKey, interfaceKey any
	if err := d.QueryRow(`SELECT protocol_version, header_protection_key FROM templates WHERE id='legacy'`).Scan(&version, &templateKey); err != nil {
		t.Fatal(err)
	}
	if version != "2.0" || templateKey != nil {
		t.Fatalf("legacy template changed: version=%q key=%v", version, templateKey)
	}
	if err := d.QueryRow(`SELECT header_protection_key FROM interfaces WHERE id='wg10'`).Scan(&interfaceKey); err != nil {
		t.Fatal(err)
	}
	if interfaceKey != nil {
		t.Fatalf("legacy interface gained AWG3 data: %v", interfaceKey)
	}
}

// ── Idempotency: Init twice (same dir) ────────────────────────────────────────

func TestOpen_IdempotentMigrations(t *testing.T) {
	dir, err := os.MkdirTemp("", "cascade-db-idem-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	t.Cleanup(func() {
		Close()
		os.RemoveAll(dir)
	})

	if err := Init(dir); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	Close()

	// Second Init should not fail (migrations already applied).
	if err := Init(dir); err != nil {
		t.Fatalf("second Init (idempotent): %v", err)
	}
}

// ── DB() panics before Init ───────────────────────────────────────────────────

func TestDB_PanicsBeforeInit(t *testing.T) {
	// Ensure no DB is open (Close is safe to call even when already nil).
	Close()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from DB() before Init(), got none")
		}
	}()
	DB()
}

// ── Migration v10: grant admin to first user if none exists ───────────────────

// v10SQL is the exact SQL from migration v10 — tested in isolation below.
const v10SQL = `
UPDATE users SET is_admin = 1
WHERE id = (SELECT id FROM users ORDER BY created_at ASC LIMIT 1)
  AND NOT EXISTS (SELECT 1 FROM users WHERE is_admin = 1);
`

// TestMigration_v10_GrantsAdminToFirstUser verifies that the v10 migration SQL
// promotes the oldest user to admin when no admin exists yet.
func TestMigration_v10_GrantsAdminToFirstUser(t *testing.T) {
	dir, err := os.MkdirTemp("", "cascade-db-v10-grant-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	t.Cleanup(func() {
		Close()
		os.RemoveAll(dir)
	})

	// Migrations run automatically via Init() — fresh DB is at the latest version.
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	d := DB()

	// Insert a user directly with is_admin=0, simulating a pre-v10 state
	// where no admin was assigned.
	_, err = d.Exec(
		`INSERT INTO users (id, username, password_hash, is_admin) VALUES ('u1', 'testuser', 'hash', 0)`,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// Simulate the v10 migration by running its SQL directly.
	if _, err := d.Exec(v10SQL); err != nil {
		t.Fatalf("v10 SQL: %v", err)
	}

	// The user should now have is_admin=1.
	var isAdmin int
	if err := d.QueryRow(`SELECT is_admin FROM users WHERE id = 'u1'`).Scan(&isAdmin); err != nil {
		t.Fatalf("query is_admin: %v", err)
	}
	if isAdmin != 1 {
		t.Errorf("expected is_admin=1 after v10 migration, got %d", isAdmin)
	}
}

// TestMigration_v10_IdempotentWhenAdminExists verifies that the v10 migration SQL
// does not touch any user when at least one admin already exists.
func TestMigration_v10_IdempotentWhenAdminExists(t *testing.T) {
	dir, err := os.MkdirTemp("", "cascade-db-v10-idem-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	t.Cleanup(func() {
		Close()
		os.RemoveAll(dir)
	})

	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	d := DB()

	// Insert first user with is_admin=1 (already an admin).
	_, err = d.Exec(
		`INSERT INTO users (id, username, password_hash, is_admin) VALUES ('u1', 'firstuser', 'hash', 1)`,
	)
	if err != nil {
		t.Fatalf("insert first user: %v", err)
	}

	// Insert second user with is_admin=0.
	_, err = d.Exec(
		`INSERT INTO users (id, username, password_hash, is_admin) VALUES ('u2', 'seconduser', 'hash', 0)`,
	)
	if err != nil {
		t.Fatalf("insert second user: %v", err)
	}

	// Simulate the v10 migration by running its SQL directly.
	if _, err := d.Exec(v10SQL); err != nil {
		t.Fatalf("v10 SQL: %v", err)
	}

	// Second user must still have is_admin=0 — the migration is idempotent.
	var isAdmin int
	if err := d.QueryRow(`SELECT is_admin FROM users WHERE id = 'u2'`).Scan(&isAdmin); err != nil {
		t.Fatalf("query is_admin for u2: %v", err)
	}
	if isAdmin != 0 {
		t.Errorf("expected is_admin=0 for second user after v10 migration (admin already existed), got %d", isAdmin)
	}
}

// ── Migration v11: traffic accumulation columns ───────────────────────────────

// TestMigration_v11_TrafficColumnsExist verifies that migration v11 adds
// total_rx and total_tx columns to the peers table with correct defaults.
func TestMigration_v11_TrafficColumnsExist(t *testing.T) {
	dir, err := os.MkdirTemp("", "cascade-db-v11-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer Close()

	d := DB()

	// Insert a minimal interface row (required for the peers FK).
	if _, err := d.Exec(`
		INSERT INTO interfaces (id, name, address, listen_port, protocol, enabled,
		                        disable_routes, private_key, public_key, created_at)
		VALUES ('wg10','test','10.8.0.1/24',51830,'wireguard-1.0',0,0,'priv','pub',datetime('now'))
	`); err != nil {
		t.Fatalf("insert interface: %v", err)
	}

	// Insert a minimal peer row — total_rx/total_tx should default to 0.
	if _, err := d.Exec(`
		INSERT INTO peers (id, interface_id, name, public_key, allowed_ips)
		VALUES ('p1','wg10','alice','AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=','10.8.0.2/32')
	`); err != nil {
		t.Fatalf("insert peer: %v", err)
	}

	var totalRx, totalTx int64
	if err := d.QueryRow(`SELECT total_rx, total_tx FROM peers WHERE id='p1'`).Scan(&totalRx, &totalTx); err != nil {
		t.Fatalf("query total_rx/total_tx (columns may be missing): %v", err)
	}
	if totalRx != 0 {
		t.Errorf("default total_rx = %d, want 0", totalRx)
	}
	if totalTx != 0 {
		t.Errorf("default total_tx = %d, want 0", totalTx)
	}

	// Verify the columns can be updated.
	if _, err := d.Exec(`UPDATE peers SET total_rx=1024, total_tx=2048 WHERE id='p1'`); err != nil {
		t.Fatalf("update total_rx/total_tx: %v", err)
	}
	if err := d.QueryRow(`SELECT total_rx, total_tx FROM peers WHERE id='p1'`).Scan(&totalRx, &totalTx); err != nil {
		t.Fatalf("re-query: %v", err)
	}
	if totalRx != 1024 || totalTx != 2048 {
		t.Errorf("updated values: got rx=%d tx=%d, want 1024/2048", totalRx, totalTx)
	}
}

func TestMigration47PreservesExistingPeerKeepalive(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	_, err := DB().Exec(`
 INSERT INTO interfaces(id,name,address,listen_port,protocol,private_key,public_key) VALUES('legacy','legacy','10.8.0.1/24',51820,'amneziawg-3.1','private','public');
 INSERT INTO peers(id,interface_id,name,public_key,persistent_keepalive) VALUES('legacy-peer','legacy','legacy','public',37);
 ALTER TABLE peers ADD COLUMN client_allowed_ips_mode TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE peers ADD COLUMN client_allowed_ips_alias_id TEXT NOT NULL DEFAULT '';
ALTER TABLE peers DROP COLUMN persistent_keepalive_v3;
 ALTER TABLE templates DROP COLUMN mtu;
 DROP TABLE auth_challenges;
DROP TABLE browser_sessions;
ALTER TABLE users DROP COLUMN auth_version;
ALTER TABLE peers DROP COLUMN client_keepalive_timeout;
DELETE FROM schema_migrations WHERE version>=47;`)
	if err != nil {
		t.Fatal(err)
	}
	Close()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	var numeric int
	var interval, clientTimeout string
	if err := DB().QueryRow("SELECT persistent_keepalive,persistent_keepalive_v3,client_keepalive_timeout FROM peers WHERE id='legacy-peer'").
		Scan(&numeric, &interval, &clientTimeout); err != nil {
		t.Fatal(err)
	}
	if numeric != 37 || interval != "" || clientTimeout != "" {
		t.Fatalf("migration changed peer: %d, %q, %q", numeric, interval, clientTimeout)
	}
}
