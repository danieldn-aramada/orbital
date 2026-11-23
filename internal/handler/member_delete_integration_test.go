//go:build integration

package handler

import "testing"

// Deleting a node from its cluster's page goes through /graphql as
// deleteKubernetesNode. The server it runs on is a reference, not part of what
// the node owns: it must survive, and only lose the node from its list.
func TestDeleteKubernetesNode_KeepsItsServer(t *testing.T) {
	gql, _ := gateFixture(t)
	const (
		cluster = "cr-engine:azurelocalkubernetescluster-al1"
		node    = "cr-engine:al1-n1"
	)
	crGQL(t, `mutation($input: [AddAzureLocalKubernetesClusterInput!]!) { addAzureLocalKubernetesCluster(input: $input) { numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": cluster, "name": "al1", "version": 1,
			"dataCenter": map[string]any{"orbId": crDC},
			"nodes": []any{map[string]any{
				"namespace": crNS, "orbId": node, "name": "al1-n1", "version": 1,
				"server": map[string]any{"orbId": crServerA},
			}},
		}}})
	// Runs before the fixture's cleanup deletes the data centre, which would
	// leave a cluster missing its required dataCenter and break /clusters.
	t.Cleanup(func() {
		deleteEntity(t, "KubernetesNode", node)
		deleteEntity(t, "AzureLocalKubernetesCluster", cluster)
	})
	if !exists(t, "KubernetesNode", node) {
		t.Fatal("setup: the node was not created")
	}

	err := mutate(t, gql, adminCaller(),
		`mutation DeleteMember($filter: KubernetesNodeFilter!) { deleteKubernetesNode(filter: $filter) { numUids } }`,
		map[string]any{"filter": map[string]any{"orbId": map[string]any{"eq": node}, "version": map[string]any{"eq": 1}}})
	if err != nil {
		t.Fatalf("delete node: %v", err)
	}

	if exists(t, "KubernetesNode", node) {
		t.Fatal("the node survived its delete")
	}
	if !exists(t, "Server", crServerA) {
		t.Fatal("deleting a node deleted the server it ran on")
	}
	out := crQuery(t, `query($orbId: String!) { s: getServer(orbId: $orbId) { kubernetesNodes { orbId } } }`,
		map[string]any{"orbId": crServerA})
	s, _ := out["s"].(map[string]any)
	nodes, _ := s["kubernetesNodes"].([]any)
	for _, n := range nodes {
		if m, _ := n.(map[string]any); m["orbId"] == node {
			t.Fatal("the server still lists the deleted node")
		}
	}
}
