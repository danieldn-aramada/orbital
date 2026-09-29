//go:build integration

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/testutil"
)

// crGQLRaw is crPost that RETURNS errors instead of failing on them — this test
// is about a specific DGraph error, so it has to be able to see one.
func crGQLRaw(t *testing.T, query string, vars map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(testutil.DGraphURL(), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post dgraph: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out
}

// A cascade delete must not leave the surviving PARENT pointing at the node it
// removed.
//
// orbital deletes through a DQL upsert (bulkDeleteGuarded) because that is what
// gives it the version-guarded CAS; DGraph's GraphQL `delete{Type}` has no
// equivalent. But `@hasInverse` is a GraphQL-layer construct — DGraph maintains
// it only for mutations that go through the GraphQL endpoint. A DQL `S * *`
// delete removes the child's own predicates and leaves the parent's list edge
// pointing at a now-empty uid.
//
// The consequence is not cosmetic. Any GraphQL query that walks that edge and
// selects a non-nullable field gets nothing back for the tombstoned node, and
// DGraph's error propagation fails the WHOLE query:
//
//	Non-nullable field 'orbId' (type String!) was not present in result from Dgraph.
//	GraphQL error propagation triggered.
//
// The export subgraph query is exactly that shape, so ONE cluster delete
// permanently broke export for its data centre — while the delete itself
// returned 200 with a correct audit event. Found 2026-09-23 with eight such
// corpses on colo-galleon, each left by a previous run of the e2e suite, and
// only ever surfaced as an opaque DGraph error in a different subsystem.
func TestDelete_LeavesNoDanglingParentEdge(t *testing.T) {
	h, _ := deleteFixture(t)
	const dc = crNS + ":dc-dangle"
	const cluster = crNS + ":cluster-dangle"

	crGQL(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": dc, "name": "dangle dc", "version": 1,
		}}})
	crGQL(t, `mutation($input:[AddEksaKubernetesClusterInput!]!){ addEksaKubernetesCluster(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": cluster, "name": "dangle cluster", "version": 1,
			"dataCenter": map[string]any{"orbId": dc},
		}}})
	t.Cleanup(func() { deleteEntity(t, "DataCenter", dc) })

	if rec := deleteReq(t, h, "KubernetesCluster", cluster, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete cluster: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// The export subgraph query's shape: walk the parent's list edge and select
	// a non-nullable field through an inline fragment. This is what breaks.
	raw := crGQLRaw(t, `query($orbId: String!) {
	  queryDataCenter(filter: { orbId: { eq: $orbId } }) {
	    orbId
	    kubernetesClusters { ... on ConfigItem { orbId name } }
	  }
	}`, map[string]any{"orbId": dc})

	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			QueryDataCenter []struct {
				OrbID              string `json:"orbId"`
				KubernetesClusters []struct {
					OrbID string `json:"orbId"`
				} `json:"kubernetesClusters"`
			} `json:"queryDataCenter"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v — raw: %s", err, raw)
	}

	for _, e := range resp.Errors {
		if strings.Contains(e.Message, "Non-nullable field") {
			t.Errorf("the deleted cluster is still referenced by its data center — "+
				"the parent's list edge was not removed, so the export query fails for the whole DC.\n"+
				"  DGraph said: %s", e.Message)
		}
	}
	if len(resp.Errors) > 0 && len(resp.Data.QueryDataCenter) == 0 {
		t.Fatalf("query returned no data: %v", resp.Errors)
	}

	// And the positive: the DC still resolves, with the cluster simply gone.
	if len(resp.Data.QueryDataCenter) != 1 {
		t.Fatalf("expected the data center to survive its cluster's deletion, got %d", len(resp.Data.QueryDataCenter))
	}
	for _, k := range resp.Data.QueryDataCenter[0].KubernetesClusters {
		if k.OrbID == cluster {
			t.Errorf("deleted cluster %s still listed under the data center", cluster)
		}
	}
}

// TestDelete_ClusterLeavesNoDanglingServerEdge covers the OTHER end of the same
// edge, which the first fix missed.
//
// Deleting a cluster deletes its KubernetesNodes. A node's SERVER survives and
// points back at it, so that edge has to go too. Left behind, any later read
// walking Server.kubernetesNode and selecting orbId fails ENTIRELY — DGraph
// propagates a missing non-nullable field to the root — naming neither the
// delete nor the node.
//
// Found in local data 2026-09-25: one server left this way made a whole
// DataCenter page unrenderable, and it would have recurred on every cluster
// delete. The server delete path already cleared the mirror edge
// (KubernetesNode.server); this is the same edge from the other side.
func TestDelete_ClusterLeavesNoDanglingServerEdge(t *testing.T) {
	h, _ := deleteFixture(t)
	const dc = crNS + ":dc-knode"
	const cluster = crNS + ":cluster-knode"
	const server = crNS + ":server-KNODE1"
	const node = crNS + ":knode-1"

	crGQL(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": dc, "name": "knode dc", "version": 1,
		}}})
	crGQL(t, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": server, "name": "knode-srv", "version": 1,
			"dataCenter": map[string]any{"orbId": dc},
		}}})
	// The node is created NESTED inside its cluster, which is the only way:
	// KubernetesNode.cluster is REQUIRED and typed by the KubernetesCluster
	// INTERFACE, and DGraph generates no ref input for an interface-typed field
	// — AddKubernetesNodeInput has no `cluster` at all, yet refuses the mutation
	// without one. Its server link is set here at the same time.
	crGQL(t, `mutation($input:[AddEksaKubernetesClusterInput!]!){ addEksaKubernetesCluster(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": cluster, "name": "knode cluster", "version": 1,
			"dataCenter": map[string]any{"orbId": dc},
			"nodes": []any{map[string]any{
				"namespace": crNS, "orbId": node, "name": "knode-1", "version": 1,
				"server": map[string]any{"orbId": server},
			}},
		}}})

	t.Cleanup(func() {
		deleteEntity(t, "Server", server)
		deleteEntity(t, "DataCenter", dc)
	})

	if rec := deleteReq(t, h, "KubernetesCluster", cluster, "", "admin"); rec.Code != http.StatusOK {
		t.Fatalf("delete cluster: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Walk the surviving server's edge and select a non-nullable field — the
	// shape every generic detail page uses, and what breaks.
	raw := crGQLRaw(t, `query($orbId: String!) {
	  queryServer(filter: { orbId: { eq: $orbId } }) {
	    orbId
	    kubernetesNode { orbId name }
	  }
	}`, map[string]any{"orbId": server})

	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Data struct {
			QueryServer []struct {
				OrbID          string `json:"orbId"`
				KubernetesNode *struct {
					OrbID string `json:"orbId"`
				} `json:"kubernetesNode"`
			} `json:"queryServer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v — raw: %s", err, raw)
	}
	for _, e := range resp.Errors {
		if strings.Contains(e.Message, "Non-nullable field") {
			t.Errorf("the server still points at its deleted KubernetesNode — every later read "+
				"walking that edge fails for the WHOLE query, naming neither the delete nor the node.\n"+
				"  DGraph said: %s", e.Message)
		}
	}
	if len(resp.Data.QueryServer) != 1 {
		t.Fatalf("expected the server to survive its cluster's deletion, got %d: %v",
			len(resp.Data.QueryServer), resp.Errors)
	}
	if n := resp.Data.QueryServer[0].KubernetesNode; n != nil && n.OrbID == node {
		t.Errorf("deleted node %s is still linked from the server", node)
	}
}
