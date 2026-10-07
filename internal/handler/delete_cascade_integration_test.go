//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/configitems"
	"github.com/labstack/echo/v4"
)

// The cascade is the page's DECLARED subgraph: the root plus every entity its
// `subgraph:` paths reach, and nothing else. Every survivor's edge into the
// deleted set is cleared, and a survivor holding a NON-NULL edge into it is
// named in the preview as orphaned.
//
// The hand-written traversals this lineage replaced had drifted into four
// silent defects with no model to check them against. Each test below pins one
// clause of the model, or one of the defects.

const (
	cascadeDC     = crNS + ":dc-cascade"
	cascadeServer = crNS + ":server-cascade"
	cascadeRack   = crNS + ":rack-cascade"
	cascadeNIC    = crNS + ":nic-cascade"
	cascadeSCP    = crNS + ":scp-cascade"
	cascadeIP     = crNS + ":ip-cascade"
	cascadeNetDev = crNS + ":netdev-cascade"
	cascadeNode   = crNS + ":knode-cascade"
	cascadeClust  = crNS + ":cluster-cascade"
)

// seedCascadeFixture builds a data centre with one rack, one server and one
// network device, the server carrying a NIC, an SCP and an OOB IP.
//
// Deliberately includes the three children a server delete used to ORPHAN and
// the one a data-centre delete used to orphan — a fixture that only held the
// children the old code handled could not have failed.
func seedCascadeFixture(t *testing.T) {
	t.Helper()
	crGQL(t, `mutation($i:[AddDataCenterInput!]!){ addDataCenter(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeDC, "name": "cascade dc", "version": 1}}})
	crGQL(t, `mutation($i:[AddRackInput!]!){ addRack(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeRack, "name": "cascade rack", "version": 1,
			"dataCenter": map[string]any{"orbId": cascadeDC}}}})
	crGQL(t, `mutation($i:[AddIPAddressInput!]!){ addIPAddress(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeIP, "name": "10.9.9.9", "address": "10.9.9.9", "version": 1}}})
	crGQL(t, `mutation($i:[AddServerInput!]!){ addServer(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeServer, "name": "cascade server", "version": 1,
			"hostname":   "cascade-host",
			"dataCenter": map[string]any{"orbId": cascadeDC},
			"rack":       map[string]any{"orbId": cascadeRack},
			"oobIP":      map[string]any{"orbId": cascadeIP}}}})
	crGQL(t, `mutation($i:[AddNetworkAdapterInput!]!){ addNetworkAdapter(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeNIC, "name": "NIC.1", "version": 1,
			"server": map[string]any{"orbId": cascadeServer}}}})
	crGQL(t, `mutation($i:[AddServerConfigurationProfileInput!]!){ addServerConfigurationProfile(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeSCP, "name": "scp", "version": 1,
			"server": map[string]any{"orbId": cascadeServer}}}})
	// The node is created NESTED inside a cluster, which is the only way:
	// KubernetesNode.cluster is REQUIRED and typed by the KubernetesCluster
	// INTERFACE, and DGraph generates no ref input for an interface-typed field.
	crGQL(t, `mutation($i:[AddEksaKubernetesClusterInput!]!){ addEksaKubernetesCluster(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeClust, "name": "cascade cluster", "version": 1,
			"dataCenter": map[string]any{"orbId": cascadeDC},
			"nodes": []any{map[string]any{
				"namespace": crNS, "orbId": cascadeNode, "name": "cascade node", "version": 1,
				"server": map[string]any{"orbId": cascadeServer}}}}}})
	crGQL(t, `mutation($i:[AddNetworkDeviceInput!]!){ addNetworkDevice(input:$i, upsert:true){ numUids } }`,
		map[string]any{"i": []any{map[string]any{
			"namespace": crNS, "orbId": cascadeNetDev, "name": "cascade switch", "version": 1,
			"dataCenter": map[string]any{"orbId": cascadeDC}}}})
	t.Cleanup(func() {
		for _, e := range [][2]string{
			{"KubernetesNode", cascadeNode}, {"EksaKubernetesCluster", cascadeClust},
			{"NetworkAdapter", cascadeNIC}, {"ServerConfigurationProfile", cascadeSCP},
			{"NetworkDevice", cascadeNetDev}, {"Server", cascadeServer},
			{"Rack", cascadeRack}, {"IPAddress", cascadeIP}, {"DataCenter", cascadeDC},
		} {
			deleteEntity(t, e[0], e[1])
		}
	})
}

// withSubgraph runs the handler against the shipped views with one page's
// subgraph replaced — so a test can pin a clause of the model without depending
// on how the shipped file happens to lay that page out.
func withSubgraph(t *testing.T, h *DeleteHandler, typeName string, members ...configitems.ViewTab) {
	t.Helper()
	live, err := h.views(context.Background())
	if err != nil {
		t.Fatalf("views: %v", err)
	}
	patched := make(configitems.ViewSet, len(live))
	copy(patched, live)
	for i := range patched {
		if patched[i].Type == typeName {
			patched[i].Subgraph = members
		}
	}
	h.views = func(context.Context) (configitems.ViewSet, error) { return patched, nil }
}

// A multi-hop path is followed, and ONLY the declared hop dies: no composition
// through the reached type's own page, and an intermediate the path passes
// through survives. The intermediate's NON-NULL edge into the deleted root then
// points at nothing, so it is named ORPHANED — and the delete still proceeds.
func TestCascade_DeletesExactlyTheDeclaredPathsAndNamesOrphans(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)
	withSubgraph(t, h, "DataCenter",
		configitems.ViewTab{Field: "servers.networkAdapters", Type: "NetworkAdapter", IsList: true})

	plan, err := h.planFor(context.Background(), "DataCenter", cascadeDC)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var orphaned string
	for _, g := range plan.preview.Orphaned {
		orphaned += g.Label + ": " + strings.Join(g.Items, ",") + "; "
	}
	for _, want := range []string{"Servers: cascade server", "Racks: cascade rack", "Network Devices: cascade switch"} {
		if !strings.Contains(orphaned, want) {
			t.Errorf("preview must name %q as orphaned — each holds `dataCenter: DataCenter!`; got %q", want, orphaned)
		}
	}

	if rec := deleteReq(t, h, "DataCenter", cascadeDC, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("an orphaning delete is informed, not refused: %d %s", rec.Code, rec.Body.String())
	}
	if exists(t, "DataCenter", cascadeDC) || exists(t, "NetworkAdapter", cascadeNIC) {
		t.Error("the root and the declared two-hop member must both be gone")
	}
	if !exists(t, "Server", cascadeServer) {
		t.Error("the server is an INTERMEDIATE hop, not a declared member — it must survive")
	}
	if !exists(t, "KubernetesNode", cascadeNode) {
		t.Error("the server's node is in the SERVER page's subgraph, not the data centre's — " +
			"a delete never composes through another page")
	}
}

// A non-null child the subgraph does NOT claim is orphaned, said, and left.
// The shipped Server page claims its KubernetesNode; a dev who drops it gets
// the consequence named in the dialog rather than a silent dangling edge.
func TestCascade_UnclaimedNonNullChildIsNamedOrphaned(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)
	withSubgraph(t, h, "Server",
		configitems.ViewTab{Field: "networkAdapters", Type: "NetworkAdapter", IsList: true})

	plan, err := h.planFor(context.Background(), "Server", cascadeServer)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var orphaned, preserved string
	for _, g := range plan.preview.Orphaned {
		orphaned += g.Label + " " + strings.Join(g.Items, " ")
	}
	for _, g := range plan.preview.Preserved {
		preserved += g.Label + " " + strings.Join(g.Items, " ")
	}
	if !strings.Contains(orphaned, "cascade node") {
		t.Errorf("KubernetesNode.server is Server! — the node must be named orphaned; got %q", orphaned)
	}
	// The negative: a NULLABLE edge is preserved, not orphaned. A flag that
	// fires for every survivor trains the operator to ignore it.
	if strings.Contains(orphaned, "scp") || !strings.Contains(preserved, "scp") {
		t.Errorf("ServerConfigurationProfile.server is nullable — preserved, never orphaned; orphaned=%q preserved=%q",
			orphaned, preserved)
	}
	if rec := deleteReq(t, h, "Server", cascadeServer, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if !exists(t, "KubernetesNode", cascadeNode) {
		t.Error("an unclaimed node is the dev's choice — it must survive the delete")
	}
}

// A relationship outside the subgraph survives, and is NAMED in the preview.
//
// Both halves. Surviving silently is how an operator discovers after the fact
// that something they expected to go is still there.
func TestCascade_UndeclaredRelationshipIsPreservedAndSaidSo(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)
	ctx := context.Background()

	plan, err := h.planFor(ctx, "Server", cascadeServer)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var preservedText string
	for _, g := range plan.preview.Preserved {
		preservedText += g.Label + " " + strings.Join(g.Items, " ")
	}
	for _, want := range []string{"Ip Addresses", "Racks", "Data Centers"} {
		if !strings.Contains(preservedText, want) {
			t.Errorf("preview must name %q as preserved; got %q", want, preservedText)
		}
	}

	if rec := deleteReq(t, h, "Server", cascadeServer, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	for _, kept := range [][2]string{
		{"IPAddress", cascadeIP}, {"Rack", cascadeRack}, {"DataCenter", cascadeDC},
	} {
		if !exists(t, kept[0], kept[1]) {
			t.Errorf("%s %s was deleted; it is not in the server's subgraph", kept[0], kept[1])
		}
	}
}

// The SHIPPED Server page leaves no orphans.
//
// Three past defects in one test, every one of them a child holding a NON-NULL
// edge to a node that no longer existed. DGraph propagates a missing non-null
// field to the ROOT of any query that selects it, so each was one delete away
// from taking out every query that walked there. The page now declares each.
func TestCascade_ShippedServerPageLeavesNoOrphans(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)

	if rec := deleteReq(t, h, "Server", cascadeServer, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	// NetworkAdapter.server is `Server!` — it cannot exist without one.
	if exists(t, "NetworkAdapter", cascadeNIC) {
		t.Error("the server's NIC survived, holding a non-null edge to a deleted server")
	}
	// KubernetesNode.server is `Server!` too. The old path CLEARED that edge
	// instead of removing the node, which left a node whose required `server`
	// resolved to nothing — the same failure by a different route.
	if exists(t, "KubernetesNode", cascadeNode) {
		t.Error("the server's KubernetesNode survived, holding a non-null edge to a deleted server")
	}
	// …and the cluster it belonged to is NOT in the server's subgraph.
	if !exists(t, "EksaKubernetesCluster", cascadeClust) {
		t.Error("deleting a server destroyed its node's cluster; a cluster is not in a server's subgraph")
	}
	assertNoEdgeTo(t, "EksaKubernetesCluster", cascadeClust, "nodes")
	// ServerConfigurationProfile.server is nullable, and the SCP is not in the
	// Server page's subgraph, so it legitimately survives — but then NOTHING
	// may be left pointing at the dead server.
	if exists(t, "ServerConfigurationProfile", cascadeSCP) {
		assertNoEdgeTo(t, "ServerConfigurationProfile", cascadeSCP, "server")
	}
}

// Deleting a data centre removes its network devices.
//
// `NetworkDevice.dataCenter` is `DataCenter!`, and no cascade touched them: a
// data-centre delete left every switch in it pointing at a tombstone.
func TestCascade_DataCenterDeleteRemovesNetworkDevices(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)

	if rec := deleteReq(t, h, "DataCenter", cascadeDC, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if exists(t, "NetworkDevice", cascadeNetDev) {
		t.Error("the data centre's network device survived, holding a non-null edge to a deleted data centre")
	}
}

// A shared IPAddress survives every delete that reaches it.
//
// The same edge used to have opposite answers depending on which page you
// clicked Delete on: a server delete preserved the IP, a data-centre delete
// destroyed it. No shipped page lists one in its subgraph.
func TestCascade_SharedIPAddressSurvivesEveryParent(t *testing.T) {
	for _, root := range []struct{ typeName, orbID string }{
		{"Server", cascadeServer},
		{"DataCenter", cascadeDC},
	} {
		t.Run(root.typeName, func(t *testing.T) {
			h, _ := deleteFixture(t)
			seedCascadeFixture(t)
			if rec := deleteReq(t, h, root.typeName, root.orbID, "", "admin"); rec.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
			}
			if !exists(t, "IPAddress", cascadeIP) {
				t.Errorf("deleting a %s destroyed a shared IP address; no page declares one",
					root.typeName)
			}
		})
	}
}

// A survivor's edge into the deleted set is cleared.
//
// Derived, not listed: `@hasInverse` tells orbital which field points back, so
// there is no hand-maintained table to forget an entry in. A DQL delete does not
// maintain `@hasInverse`, and orbital's cascade is a DQL upsert deliberately —
// it is the only way to get a version-guarded CAS.
func TestCascade_ClearsEdgesHeldBySurvivors(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)

	if rec := deleteReq(t, h, "Server", cascadeServer, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	// The export subgraph query's shape: walk the survivor's list edge and
	// select a non-nullable field through it. This is what breaks.
	for _, survivor := range []struct{ typeName, orbID, edge string }{
		{"DataCenter", cascadeDC, "servers"},
		{"Rack", cascadeRack, "servers"},
	} {
		assertNoEdgeTo(t, survivor.typeName, survivor.orbID, survivor.edge)
	}
}

// The preview and the delete are built from one walk.
//
// Two walks would be two chances to disagree, and the disagreement would be
// invisible: an operator confirms a dialog describing one set and a different
// set is removed.
func TestCascade_PreviewAndDeleteAgree(t *testing.T) {
	h, _ := deleteFixture(t)
	seedCascadeFixture(t)
	ctx := context.Background()

	plan, err := h.planFor(ctx, "DataCenter", cascadeDC)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.preview.TotalCount != len(plan.uids) {
		t.Errorf("the preview says %d and the delete would remove %d",
			plan.preview.TotalCount, len(plan.uids))
	}
	rec := deleteReq(t, h, "DataCenter", cascadeDC, "", "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"deleted":`+itoa(plan.preview.TotalCount)) {
		t.Errorf("the delete reported a different count from the preview's %d: %s",
			plan.preview.TotalCount, rec.Body.String())
	}
}

// assertNoEdgeTo runs the shape that actually breaks: walk the edge and select a
// non-nullable field through it. A dangling edge fails the WHOLE query, which is
// why this asserts on `errors` rather than on the row count.
func assertNoEdgeTo(t *testing.T, typeName, orbID, edge string) {
	t.Helper()
	raw := crGQLRaw(t, `query($orbId: String!) {
	  query`+typeName+`(filter: { orbId: { eq: $orbId } }) {
	    orbId
	    `+edge+` { ... on ConfigItem { orbId } }
	  }
	}`, map[string]any{"orbId": orbID})
	if strings.Contains(string(raw), "Non-nullable field") {
		t.Errorf("%s %s still points at a deleted node through %s — "+
			"the whole query fails, which is how one delete breaks export for a data centre: %s",
			typeName, orbID, edge, raw)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// A delete through the page route leaves an audit event an operator can find
// through GET /api/v1/audit-log — read back through that API, not the write
// path — and planning one (the preview) leaves none.
//
// A missing audit row changes nothing observable at the time; it is found only
// when someone asks a question that can no longer be answered. debt.md carried
// this as untested.
func TestCascade_DeleteIsAuditedAndPreviewIsNot(t *testing.T) {
	h, f := deleteFixture(t)
	seedCascadeFixture(t)
	events := func() []eventItem {
		t.Helper()
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/audit-log?orbId="+cascadeServer, nil), rec)
		if err := (&AuditHandler{db: f.db, logger: slog.Default()}).List(c); err != nil {
			t.Fatalf("audit-log: %v", err)
		}
		var body auditLogResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode audit-log: %v (%s)", err, rec.Body.String())
		}
		var out []eventItem
		for _, e := range body.Events {
			if strings.Join(e.Operations, ",") == "deleteServer" {
				out = append(out, e)
			}
		}
		return out
	}

	if _, err := h.planFor(context.Background(), "Server", cascadeServer); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("a preview must not record a delete; got %+v", got)
	}

	if rec := deleteReq(t, h, "Server", cascadeServer, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	got := events()
	if len(got) != 1 {
		t.Fatalf("want exactly one deleteServer event for %s, got %d", cascadeServer, len(got))
	}
	if got[0].Actor != "deleter@test.com" || strings.Join(got[0].ResourceTypes, ",") != "Server" {
		t.Errorf("event = actor %q types %v, want deleter@test.com / [Server]", got[0].Actor, got[0].ResourceTypes)
	}
}
