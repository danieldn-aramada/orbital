//go:build integration

package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// GET /api/v1/schema/graph is what AEP draws the schema from with a library of
// its own, so the shape is the contract: every edge names nodes the response
// lists, a two-way relationship is one edge, and the levels come out of the
// deployed schema and the SHIPPED views together.
func TestSchemaGraphAPI_ServesTheDeployedSchemaAsLevelledNodesAndEdges(t *testing.T) {
	fields := NewSharedFieldSource(testutil.DGraphURL(), ViewsSource{Path: testutil.ViewsPath()}, slog.Default())
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/schema/graph", nil), rec)
	if err := NewSchemaGraph(fields, slog.Default()).Get(c); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET: err=%v status=%d body=%s", err, rec.Code, rec.Body.String())
	}
	var g configitems.SchemaGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	nodes := map[string]configitems.SchemaGraphNode{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}

	t.Run("every ConfigItem type is a node with an id, label, kind and level", func(t *testing.T) {
		for _, id := range []string{"DataCenter", "Server", "NetworkInterface", "KubernetesCluster", "ConfigItem"} {
			n, ok := nodes[id]
			if !ok || n.Label == "" || (n.Kind != "type" && n.Kind != "interface") {
				t.Errorf("%s: missing or incomplete node %+v", id, n)
			}
		}
	})
	t.Run("every edge joins two nodes the response lists", func(t *testing.T) {
		for _, e := range g.Edges {
			if _, ok := nodes[e.Source]; !ok {
				t.Errorf("edge %s: source %q is not a node", e.ID, e.Source)
			}
			if _, ok := nodes[e.Target]; !ok {
				t.Errorf("edge %s: target %q is not a node", e.ID, e.Target)
			}
		}
	})
	t.Run("a two-way relationship is one edge", func(t *testing.T) {
		between := 0
		for _, e := range g.Edges {
			if (e.Source == "Rack" && e.Target == "Server") || (e.Source == "Server" && e.Target == "Rack") {
				between++
			}
		}
		if between != 1 {
			t.Errorf("Rack.servers / Server.rack must be one edge, got %d", between)
		}
	})
	t.Run("levels follow the deployed schema and the shipped views", func(t *testing.T) {
		for id, want := range map[string]int{"DataCenter": 0, "Server": 1, "NetworkAdapter": 2, "NetworkInterface": 3} {
			if got := nodes[id].Level; got != want {
				t.Errorf("%s is on level %d, want %d (NetworkInterface's home is declared in views.yaml)", id, got, want)
			}
		}
	})
}
