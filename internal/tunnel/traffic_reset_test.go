package tunnel

import (
	"errors"
	"testing"

	"github.com/alexnikon/cascade/internal/db"
)

func TestResetPeerTrafficClientsAndS2S(t *testing.T) {
	for _, peerType := range []string{"client", "interconnect"} {
		for _, active := range []bool{false, true} {
			t.Run(peerType+map[bool]string{false: "/disabled", true: "/active"}[active], func(t *testing.T) {
				db.Close()
				if err := db.Init(t.TempDir()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(db.Close)
				if _, err := db.DB().Exec(`INSERT INTO interfaces (id,name) VALUES ('wg14','S2S')`); err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"target", "other"} {
					if _, err := db.DB().Exec(`INSERT INTO peers (id,interface_id,public_key,peer_type,total_rx,total_tx) VALUES (?,'wg14',?,?,1000,2000)`, id, id, peerType); err != nil {
						t.Fatal(err)
					}
				}
				iface, err := LoadInterface("wg14")
				if err != nil {
					t.Fatal(err)
				}
				iface.Enabled = active
				readBaseline := func() (string, error) {
					if !active {
						t.Fatal("disabled interface requested kernel counters")
					}
					return "interface\ntarget\tpsk\tendpoint\tips\t0\t3000\t4000\t25", nil
				}
				for range 2 {
					if err := iface.resetPeerTraffic("target", readBaseline); err != nil {
						t.Fatal(err)
					}
				}
				state := iface.trafficState["target"]
				if state.totalRx != 0 || state.totalTx != 0 || state.dirty {
					t.Fatalf("reset state = %+v", state)
				}
				if active && (state.lastSeenRx != 3000 || state.lastSeenTx != 4000) {
					t.Fatalf("kernel baseline = %+v", state)
				}
				for _, p := range iface.RuntimeSnapshot().Peers {
					if p.ID == "target" && (p.TotalRx != 0 || p.TotalTx != 0) {
						t.Fatalf("reset snapshot = %+v", p)
					}
					if p.ID == "other" && (p.TotalRx != 1000 || p.TotalTx != 2000) {
						t.Fatalf("other peer changed = %+v", p)
					}
				}
				iface.FlushTrafficTotals()
				reloaded, err := LoadInterface("wg14")
				if err != nil {
					t.Fatal(err)
				}
				if p := reloaded.GetPeer("target"); p.TotalRx != 0 || p.TotalTx != 0 {
					t.Fatalf("reset did not survive reload: %+v", p)
				}
				if err := iface.resetPeerTraffic("missing", readBaseline); !errors.Is(err, ErrTrafficPeerNotFound) {
					t.Fatalf("missing peer error = %v", err)
				}

				// A failed baseline read or database write must preserve current usage.
				state.totalRx, state.totalTx, state.dirty = 50, 60, true
				iface.peers["target"].TotalRx, iface.peers["target"].TotalTx = 50, 60
				if active {
					if err := iface.resetPeerTraffic("target", func() (string, error) { return "", errors.New("unavailable") }); err == nil {
						t.Fatal("baseline failure unexpectedly succeeded")
					}
				}
				if _, err := db.DB().Exec(`CREATE TRIGGER reject_reset BEFORE UPDATE OF total_rx ON peers BEGIN SELECT RAISE(ABORT, 'write failed'); END`); err != nil {
					t.Fatal(err)
				}
				if err := iface.resetPeerTraffic("target", readBaseline); err == nil {
					t.Fatal("database failure unexpectedly succeeded")
				}
				if state.totalRx != 50 || state.totalTx != 60 || iface.GetPeer("target").TotalRx != 50 {
					t.Fatal("failed reset changed current usage")
				}
			})
		}
	}
}
