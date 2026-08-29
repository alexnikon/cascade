package grafana

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func loadDashboard(t *testing.T) map[string]any {
	t.Helper()
	content, err := os.ReadFile("cascade-dashboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard map[string]any
	if err := json.Unmarshal(content, &dashboard); err != nil {
		t.Fatal(err)
	}
	return dashboard
}

func object(t *testing.T, value any, path string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object", path)
	}
	return result
}

func stringAt(t *testing.T, value any, path string) string {
	t.Helper()
	result, ok := value.(string)
	if !ok {
		t.Fatalf("%s is not a string", path)
	}
	return result
}

func queries(t *testing.T, dashboard map[string]any, panelID string) []map[string]any {
	t.Helper()
	spec := object(t, dashboard["spec"], "spec")
	elements := object(t, spec["elements"], "spec.elements")
	element := object(t, elements["panel-"+panelID], "panel-"+panelID)
	panelSpec := object(t, element["spec"], "panel spec")
	data := object(t, panelSpec["data"], "panel data")
	dataSpec := object(t, data["spec"], "query group")
	rawQueries, ok := dataSpec["queries"].([]any)
	if !ok {
		t.Fatalf("panel %s queries are not an array", panelID)
	}
	result := make([]map[string]any, 0, len(rawQueries))
	for _, raw := range rawQueries {
		result = append(result, object(t, raw, "query"))
	}
	return result
}

func querySpec(t *testing.T, query map[string]any) map[string]any {
	t.Helper()
	spec := object(t, query["spec"], "panel query spec")
	inner := object(t, spec["query"], "data query")
	return object(t, inner["spec"], "data query spec")
}

func queryExpr(t *testing.T, query map[string]any) string {
	return stringAt(t, querySpec(t, query)["expr"], "query expr")
}

func panelOptions(t *testing.T, dashboard map[string]any, panelID string) map[string]any {
	t.Helper()
	spec := object(t, dashboard["spec"], "spec")
	elements := object(t, spec["elements"], "spec.elements")
	element := object(t, elements["panel-"+panelID], "panel")
	panelSpec := object(t, element["spec"], "panel spec")
	viz := object(t, panelSpec["vizConfig"], "viz config")
	return object(t, viz["spec"], "viz spec")["options"].(map[string]any)
}

func TestDashboardPeerVariableUsesNames(t *testing.T) {
	dashboard := loadDashboard(t)
	spec := object(t, dashboard["spec"], "spec")
	rawVariables, ok := spec["variables"].([]any)
	if !ok {
		t.Fatal("variables are not an array")
	}
	var peer map[string]any
	for _, raw := range rawVariables {
		candidate := object(t, raw, "variable")
		candidateSpec := object(t, candidate["spec"], "variable spec")
		switch candidateSpec["name"] {
		case "peer":
			peer = candidateSpec
		case "peer_id":
			t.Fatal("legacy peer_id variable is still present")
		}
	}
	if peer == nil || peer["label"] != "Peer" {
		t.Fatalf("peer variable=%v, want label Peer", peer)
	}
	definition := stringAt(t, peer["definition"], "peer definition")
	if !strings.HasSuffix(definition, ", name)") {
		t.Fatalf("peer definition=%q does not select name label", definition)
	}
}

func TestDashboardDatabaseStatus(t *testing.T) {
	dashboard := loadDashboard(t)
	panelQueries := queries(t, dashboard, "81")
	if len(panelQueries) != 1 {
		t.Fatalf("database status queries=%d, want 1", len(panelQueries))
	}
	if got := queryExpr(t, panelQueries[0]); got != `max by (instance) (cascade_database_up{instance=~"$instance"})` {
		t.Fatalf("database query=%q", got)
	}
	if got := panelOptions(t, dashboard, "81")["textMode"]; got != "value" {
		t.Fatalf("database textMode=%v, want value", got)
	}
}

func TestDashboardGatewayStatus(t *testing.T) {
	dashboard := loadDashboard(t)
	panelQueries := queries(t, dashboard, "61")
	if len(panelQueries) != 1 {
		t.Fatalf("gateway status queries=%d, want 1", len(panelQueries))
	}
	if got := queryExpr(t, panelQueries[0]); got != `max by (instance, gateway) (cascade_gateway_status{instance=~"$instance",gateway=~"$gateway"})` {
		t.Fatalf("gateway query=%q", got)
	}
	if got := querySpec(t, panelQueries[0])["legendFormat"]; got != "{{gateway}}" {
		t.Fatalf("gateway legend=%v, want {{gateway}}", got)
	}
	if got := panelOptions(t, dashboard, "61")["textMode"]; got != "value_and_name" {
		t.Fatalf("gateway textMode=%v, want value_and_name", got)
	}
}

func TestDashboardPeerQueriesUseNameFilter(t *testing.T) {
	dashboard := loadDashboard(t)
	spec := object(t, dashboard["spec"], "spec")
	elements := object(t, spec["elements"], "elements")
	for panelID, raw := range elements {
		element := object(t, raw, panelID)
		panelSpec := object(t, element["spec"], panelID+" spec")
		data, ok := panelSpec["data"].(map[string]any)
		if !ok {
			continue
		}
		dataSpec, ok := data["spec"].(map[string]any)
		if !ok {
			continue
		}
		rawQueries, ok := dataSpec["queries"].([]any)
		if !ok {
			continue
		}
		for _, rawQuery := range rawQueries {
			expr := queryExpr(t, object(t, rawQuery, panelID+" query"))
			if strings.Contains(expr, "$peer") && strings.Contains(expr, "peer_id=~") {
				t.Fatalf("panel %s still filters peer_id with $peer: %s", panelID, expr)
			}
		}
	}
}
