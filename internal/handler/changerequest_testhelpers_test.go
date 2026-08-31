//go:build integration

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/armada/orbital/internal/graphdiff"
	"github.com/armada/orbital/internal/testutil"
	"github.com/google/uuid"
)

// Fixture and probe helpers for the change-request engine tests. Every write
// here goes straight to DGraph, deliberately bypassing orbital — these stand in
// for "someone else changed the graph", which is the condition the whole
// staleness mechanism exists to detect.

func seedCREngineFixture(t *testing.T) {
	t.Helper()
	crGQL(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": crDC, "name": "cr engine fixture", "version": 1,
		}}})
	addRack(t)
	crGQL(t, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{
			map[string]any{
				"namespace": crNS, "orbId": crServerA, "version": 1, "hostname": "a-original",
				"dataCenter": map[string]any{"orbId": crDC},
			},
			map[string]any{
				"namespace": crNS, "orbId": crServerB, "version": 1, "hostname": "b-original",
				"dataCenter": map[string]any{"orbId": crDC},
			},
		}})
	// The owned child gets its own mutation — nesting it under the Server would
	// only link, discarding these values.
	crGQL(t, `mutation($input:[AddIdracSettingsInput!]!){ addIdracSettings(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": crIdracA, "version": 1, "firmwareVersion": "1.0.0",
			"server": map[string]any{"orbId": crServerA},
		}}})

	t.Cleanup(func() {
		deleteEntity(t, "IdracSettings", crIdracA)
		deleteEntity(t, "Server", crServerA)
		deleteEntity(t, "Server", crServerB)
		deleteEntity(t, "Rack", crRack)
		deleteEntity(t, "DataCenter", crDC)
	})
}

func addRack(t *testing.T) {
	t.Helper()
	crGQL(t, `mutation($input:[AddRackInput!]!){ addRack(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": crRack, "name": "rack-1", "version": 1,
			"dataCenter": map[string]any{"orbId": crDC},
		}}})
}

// setHostname simulates a third party editing intent.
//
// It bumps `version` explicitly because it POSTs straight to DGraph, and every
// real write to intent goes through orbital, which stamps the increment itself
// (graphql.go). Without the bump the fixture writes content that orbital's OCC
// counter never saw — which is a DIFFERENT scenario, covered on its own by
// TestCR_OutOfBandWriteThatSkipsTheVersionCounterIsNotSeen.
func setHostname(t *testing.T, orbID, v string) {
	t.Helper()
	crGQL(t, `mutation($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": orbID, "set": map[string]any{"hostname": v, "version": readVersion(t, orbID) + 1}})
}

func readHostname(t *testing.T, orbID string) string {
	t.Helper()
	node := getServer(t, orbID, "hostname")
	s, _ := node["hostname"].(string)
	return s
}

func readVersion(t *testing.T, orbID string) int {
	t.Helper()
	node := getServer(t, orbID, "version")
	f, _ := toFloat64(node["version"])
	return int(f)
}

func exists(t *testing.T, typeName, orbID string) bool {
	t.Helper()
	out := crQuery(t, `query($orbId:String!){ n: get`+typeName+`(orbId:$orbId){ orbId } }`,
		map[string]any{"orbId": orbID})
	return out["n"] != nil
}

func getServer(t *testing.T, orbID string, fields ...string) map[string]any {
	t.Helper()
	sel := ""
	for _, f := range fields {
		sel += " " + f
	}
	out := crQuery(t, `query($orbId:String!){ n: getServer(orbId:$orbId){`+sel+` } }`,
		map[string]any{"orbId": orbID})
	node, _ := out["n"].(map[string]any)
	if node == nil {
		t.Fatalf("server %s not found", orbID)
	}
	return node
}

func deleteEntity(t *testing.T, typeName, orbID string) {
	t.Helper()
	crGQL(t, `mutation($orbId:String!){ delete`+typeName+`(filter:{orbId:{eq:$orbId}}){ numUids } }`,
		map[string]any{"orbId": orbID})
}

func compareChangeset(st crState) *graphdiff.Result {
	return graphdiff.Compare(st.Snapshot, applyChangesetTo(st.Snapshot, st.Changeset))
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return id
}

func crQuery(t *testing.T, query string, vars map[string]any) map[string]any {
	t.Helper()
	raw := crPost(t, query, vars)
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Data
}

func crGQL(t *testing.T, query string, vars map[string]any) {
	t.Helper()
	crPost(t, query, vars)
}

func crPost(t *testing.T, query string, vars map[string]any) []byte {
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
	if resp.StatusCode != http.StatusOK || bytes.Contains(out, []byte(`"errors"`)) {
		t.Fatalf("dgraph %d: %s\nquery: %s", resp.StatusCode, out, query)
	}
	return out
}
