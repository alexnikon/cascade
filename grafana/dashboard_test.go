package grafana

import (
	"encoding/json"
	"os"
	"testing"
)

type dashboardFile struct {
	Version int `json:"version"`
	Panels  []struct {
		ID       int                      `json:"id"`
		Title    string                   `json:"title"`
		Type     string                   `json:"type"`
		TimeFrom string                   `json:"timeFrom"`
		GridPos  struct{ H, W, X, Y int } `json:"gridPos"`
		Targets  []struct {
			Expr string `json:"expr"`
		} `json:"targets"`
	} `json:"panels"`
}

func TestDashboardOutboundCalendarTrafficPanels(t *testing.T) {
	content, err := os.ReadFile("cascade-dashboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard dashboardFile
	if err := json.Unmarshal(content, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.Version < 2 {
		t.Fatalf("dashboard version=%d, want at least 2", dashboard.Version)
	}
	want := map[int]struct{ title, timeFrom string }{
		35: {"Outbound Traffic Today", "now/d"},
		36: {"Outbound Traffic This Month", "now/M"},
	}
	for _, panel := range dashboard.Panels {
		expected, ok := want[panel.ID]
		if !ok {
			continue
		}
		if panel.Title != expected.title || panel.Type != "stat" || panel.TimeFrom != expected.timeFrom {
			t.Fatalf("unexpected panel %d: %+v", panel.ID, panel)
		}
		if len(panel.Targets) != 1 || panel.Targets[0].Expr != `sum(increase(cascade_interface_sent_bytes_total{instance=~"$instance",interface=~"$interface"}[$__range]))` {
			t.Fatalf("unexpected query for panel %d: %+v", panel.ID, panel.Targets)
		}
		delete(want, panel.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing traffic panels: %v", want)
	}
}

func TestDashboardPanelsDoNotOverlap(t *testing.T) {
	content, err := os.ReadFile("cascade-dashboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard dashboardFile
	if err := json.Unmarshal(content, &dashboard); err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for i, panel := range dashboard.Panels {
		if seen[panel.ID] {
			t.Fatalf("duplicate panel ID %d", panel.ID)
		}
		seen[panel.ID] = true
		for _, other := range dashboard.Panels[i+1:] {
			if rectanglesOverlap(panel.GridPos.X, panel.GridPos.Y, panel.GridPos.W, panel.GridPos.H, other.GridPos.X, other.GridPos.Y, other.GridPos.W, other.GridPos.H) {
				t.Fatalf("panels %d and %d overlap", panel.ID, other.ID)
			}
		}
	}
}

func rectanglesOverlap(ax, ay, aw, ah, bx, by, bw, bh int) bool {
	return ax < bx+bw && ax+aw > bx && ay < by+bh && ay+ah > by
}
