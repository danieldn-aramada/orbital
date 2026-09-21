package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestAuthorizeMutation_ContextRole pins the regression where a caller whose
// role arrives on the context rather than from the users table was 403'd on
// every GraphQL mutation: no users-table row, so the old user_id→DB check always
// failed. A delegatedAuthorization provider is that caller today (the removed
// external-jwt mode was the first one), so the gate must honour the context role
// instead. No DB needed — that path never touches h.db.
func TestAuthorizeMutation_ContextRole(t *testing.T) {
	h := &GraphQL{} // db nil — the context-role path is role-only
	for _, tc := range []struct {
		role string
		want bool
	}{
		{"admin", true},     // a delegatedAuthorization role — must be allowed
		{"dev", true},       // minimum for mutations
		{"readonly", false}, // below dev — denied even with a context role
	} {
		t.Run(tc.role, func(t *testing.T) {
			e := echo.New()
			c := e.NewContext(httptest.NewRequest(http.MethodPost, "/graphql", nil), httptest.NewRecorder())
			c.Set("role", tc.role)
			if got, _ := h.authorizeMutation(c); got != tc.want {
				t.Errorf("authorizeMutation(role=%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

// TestAuthorizeMutation_DevModeNoDB confirms the nil-db dev path still passes
// (no authz backend), unchanged by the context-role short-circuit.
func TestAuthorizeMutation_DevModeNoDB(t *testing.T) {
	h := &GraphQL{} // db nil, no context role
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/graphql", nil), httptest.NewRecorder())
	if ok, _ := h.authorizeMutation(c); !ok {
		t.Error("dev mode (nil db, no context role) should allow mutations")
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name   string
		input  any
		want   float64
		wantOK bool
	}{
		{name: "float64", input: float64(3.14), want: 3.14, wantOK: true},
		{name: "zero float64", input: float64(0), want: 0, wantOK: true},
		{name: "int", input: int(7), want: 7, wantOK: true},
		{name: "json.Number integer", input: json.Number("42"), want: 42, wantOK: true},
		{name: "json.Number float", input: json.Number("1.5"), want: 1.5, wantOK: true},
		// The !ok cases are the A.3 regression guard: an unparseable version must
		// report ok=false so the MVCC check rejects it instead of coercing to 0
		// and silently passing.
		{name: "json.Number invalid is not ok", input: json.Number("not a number"), want: 0, wantOK: false},
		{name: "nil is not ok", input: nil, want: 0, wantOK: false},
		{name: "string is not ok", input: "not a number", want: 0, wantOK: false},
		{name: "bool is not ok", input: true, want: 0, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toFloat64(tt.input)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("toFloat64(%v) = (%v, %v), want (%v, %v)", tt.input, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestExtractResourceIDs(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		variables map[string]any
		respBody  []byte
		want      []string
	}{
		{
			name:      "single orbId in variables",
			query:     `mutation { updateServer(input: {}) { server { id } } }`,
			variables: map[string]any{"orbId": "alaska:SRV001"},
			respBody:  []byte(`{}`),
			want:      []string{"alaska:SRV001"},
		},
		{
			name:  "orbId in input array",
			query: `mutation { addServer(input: []) { server { id } } }`,
			variables: map[string]any{
				"input": []any{
					map[string]any{"orbId": "alaska:SRV001"},
					map[string]any{"orbId": "alaska:SRV002"},
				},
			},
			respBody: []byte(`{}`),
			want:     []string{"alaska:SRV001", "alaska:SRV002"},
		},
		{
			name:      "orbId in inline filter expression",
			query:     `mutation { updateServer(filter: { orbId: { eq: "alaska:SRV003" } }, set: {}) { server { id } } }`,
			variables: map[string]any{},
			respBody:  []byte(`{}`),
			want:      []string{"alaska:SRV003"},
		},
		{
			// The shape used by dispatchAcceptMutation: $filter passed as a
			// variable so the audit-log expanded row renders the same Input
			// block as a user-driven mutation. The orbId hides inside
			// variables.filter.orbId.eq and was previously missed.
			name:  "orbId in filter variable (eq)",
			query: `mutation AcceptDivergence($filter: IdracSettingsFilter!, $set: IdracSettingsPatch!) { updateIdracSettings(input: {filter: $filter, set: $set}) { numUids } }`,
			variables: map[string]any{
				"filter": map[string]any{
					"orbId": map[string]any{"eq": "colo:CWJHDX3-idrac"},
				},
				"set": map[string]any{"sshEnabled": true},
			},
			respBody: []byte(`{"data":{"updateIdracSettings":{"numUids":1}}}`),
			want:     []string{"colo:CWJHDX3-idrac"},
		},
		{
			name:  "orbIds in filter variable (in)",
			query: `mutation Bulk($filter: ServerFilter!, $set: ServerPatch!) { updateServer(input: {filter: $filter, set: $set}) { numUids } }`,
			variables: map[string]any{
				"filter": map[string]any{
					"orbId": map[string]any{"in": []any{"alaska:SRV001", "alaska:SRV002"}},
				},
				"set": map[string]any{"powerState": "on"},
			},
			respBody: []byte(`{"data":{"updateServer":{"numUids":2}}}`),
			want:     []string{"alaska:SRV001", "alaska:SRV002"},
		},
		{
			name:      "orbId in response body",
			query:     `mutation { addServer(input: []) { server { orbId } } }`,
			variables: map[string]any{},
			respBody:  []byte(`{"data":{"addServer":{"server":[{"orbId":"alaska:SRV004"}]}}}`),
			want:      []string{"alaska:SRV004"},
		},
		{
			name:  "deduplicated across all sources",
			query: `mutation { updateServer(filter: { orbId: { eq: "alaska:SRV001" } }, set: {}) { server { orbId } } }`,
			variables: map[string]any{
				"orbId": "alaska:SRV001",
			},
			respBody: []byte(`{"data":{"updateServer":{"server":[{"orbId":"alaska:SRV001"}]}}}`),
			want:     []string{"alaska:SRV001"},
		},
		{
			// (a) The reported bug: the variable is not named `orbId`, so every
			// lookup missed and the gate refused a valid variable-form mutation.
			name:  "orbId behind a differently-named variable reference",
			query: `mutation UpdateCluster($clusterOrbId: String!, $set: KubernetesClusterPatch!) { updateKubernetesCluster(input: {filter: {orbId: {eq: $clusterOrbId}}, set: $set}) { numUids } }`,
			variables: map[string]any{
				"clusterOrbId": "colo:cluster-a",
				"set":          map[string]any{"cni": "cilium"},
			},
			respBody: []byte(`{"data":{"updateKubernetesCluster":{"numUids":1}}}`),
			want:     []string{"colo:cluster-a"},
		},
		{
			// A compound mutation CANNOT have two variables named orbId, so this
			// shape was unresolvable by construction.
			name:  "compound mutation with two differently-named variables",
			query: `mutation Del($a: String!, $b: String!) { d1: deleteServer(filter: {orbId: {eq: $a}}) { numUids } d2: deleteRack(filter: {orbId: {eq: $b}}) { numUids } }`,
			variables: map[string]any{
				"a": "alaska:SRV001",
				"b": "alaska:Rack-5",
			},
			respBody: []byte(`{}`),
			want:     []string{"alaska:Rack-5", "alaska:SRV001"},
		},
		{
			// (b) inline `in` list, literals and variable references mixed.
			name:  "inline in-list mixing literals and variable references",
			query: `mutation Bulk($second: String!) { updateServer(filter: {orbId: {in: ["alaska:SRV001", $second]}}, set: {}) { numUids } }`,
			variables: map[string]any{
				"second": "alaska:SRV002",
			},
			respBody: []byte(`{}`),
			want:     []string{"alaska:SRV001", "alaska:SRV002"},
		},
		{
			// (c) the whole filter object behind a variable that is not named
			// "filter" — the literal key was the only one ever read.
			name:  "filter object behind a differently-named variable",
			query: `mutation Apply($myFilter: ServerFilter!, $set: ServerPatch!) { updateServer(input: {filter: $myFilter, set: $set}) { numUids } }`,
			variables: map[string]any{
				"myFilter": map[string]any{"orbId": map[string]any{"in": []any{"colo:SRV009", "colo:SRV010"}}},
				"set":      map[string]any{"hostname": "h"},
			},
			respBody: []byte(`{}`),
			want:     []string{"colo:SRV009", "colo:SRV010"},
		},
		{
			// A reference we cannot resolve must yield NOTHING, so the gate
			// still refuses it rather than waving it through on a guess.
			name:      "unresolvable variable reference yields no ids",
			query:     `mutation U($missing: String!) { updateServer(filter: {orbId: {eq: $missing}}, set: {}) { numUids } }`,
			variables: map[string]any{"set": map[string]any{"hostname": "h"}},
			respBody:  []byte(`{}`),
			want:      nil,
		},
		{
			// A filter on a field that is not orbId must not be mined for ids:
			// a wrongly-attributed resource id reads as fact on an audit row.
			name:  "filter variable on a non-orbId field contributes nothing",
			query: `mutation ByName($f: ServerFilter!, $set: ServerPatch!) { updateServer(input: {filter: $f, set: $set}) { numUids } }`,
			variables: map[string]any{
				"f":   map[string]any{"hostname": map[string]any{"eq": "not-an-orbid"}},
				"set": map[string]any{"model": "m"},
			},
			respBody: []byte(`{}`),
			want:     nil,
		},
		{
			name:      "empty variables and body returns empty",
			query:     `mutation { addServer(input: []) { server { id } } }`,
			variables: map[string]any{},
			respBody:  []byte(`{"data":{}}`),
			want:      nil,
		},
		{
			name:      "nested orbIds in response collected recursively",
			query:     `mutation { addDataCenter(input: []) { dataCenter { orbId servers { orbId } } } }`,
			variables: map[string]any{},
			respBody: []byte(`{"data":{"addDataCenter":{"dataCenter":[{
				"orbId":"alaska",
				"servers":[{"orbId":"alaska:SRV001"},{"orbId":"alaska:SRV002"}]
			}]}}}`),
			want: []string{"alaska", "alaska:SRV001", "alaska:SRV002"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractResourceIDs(tt.query, tt.variables, tt.respBody)
			if !slices.Equal(got, tt.want) {
				t.Errorf("extractResourceIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// resolveWriteSelector is the write path's answer to "which single row does this
// mutation target, and which variable carries its patch" — and it is deliberately
// STRICTER than extractResourceIDs, which only has to know an orbId to look up a
// policy. Pure, and every interesting case is a shape it must REFUSE to resolve:
// a selector it reads wrongly would stamp one row's successor version onto
// another, which is worse than the 400 the caller gets instead.
func TestResolveWriteSelector(t *testing.T) {
	const canonical = `mutation U($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`

	patch := map[string]any{"hostname": "x"}
	cases := []struct {
		name         string
		query        string
		vars         map[string]any
		wantOrbID    string
		wantOrbIDVar string
		wantSetVar   string
	}{
		{
			name:      "canonical spelling",
			query:     canonical,
			vars:      map[string]any{"orbId": "ns:server-A", "set": patch},
			wantOrbID: "ns:server-A", wantOrbIDVar: "orbId", wantSetVar: "set",
		},
		{
			name:      "renamed selector and patch resolve identically",
			query:     `mutation U($sOrb: String!, $patch: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $sOrb } }, set: $patch }) { numUids } }`,
			vars:      map[string]any{"sOrb": "ns:server-A", "patch": patch},
			wantOrbID: "ns:server-A", wantOrbIDVar: "sOrb", wantSetVar: "patch",
		},
		{
			name:  "orbId passed as an orbital-only variable the query never declares",
			query: `mutation U($set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: "ns:server-A" } }, set: $set }) { numUids } }`,
			vars:  map[string]any{"orbId": "ns:server-A", "set": patch},
			// Not resolvable HERE — an inline literal disqualifies the query.
			// fetchCurrentState still finds it, by reading the variable directly
			// and BEFORE calling this; that ordering is what keeps orbctl working.
			wantOrbID: "", wantOrbIDVar: "", wantSetVar: "set",
		},
		{
			name:       "in-list names more than one row",
			query:      `mutation U($a: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { in: [$a] } }, set: $set }) { numUids } }`,
			vars:       map[string]any{"a": "ns:server-A", "set": patch},
			wantSetVar: "set",
		},
		{
			name:       "two eq filters, so no single target",
			query:      `mutation U($a: String!, $b: String!, $set: ServerPatch!) { x: updateServer(input: { filter: { orbId: { eq: $a } }, set: $set }) { numUids } y: updateServer(input: { filter: { orbId: { eq: $b } }, set: $set }) { numUids } }`,
			vars:       map[string]any{"a": "ns:server-A", "b": "ns:server-B", "set": patch},
			wantSetVar: "set",
		},
		{
			name:       "a literal alongside a variable still disqualifies",
			query:      `mutation U($a: String!, $set: ServerPatch!) { x: updateServer(input: { filter: { orbId: { eq: $a } }, set: $set }) { numUids } y: updateServer(input: { filter: { orbId: { eq: "ns:server-B" } }, set: $set }) { numUids } }`,
			vars:       map[string]any{"a": "ns:server-A", "set": patch},
			wantSetVar: "set",
		},
		{
			name:  "whole filter behind a variable is readable by the gate, not by the writer",
			query: `mutation U($f: ServerFilter!, $set: ServerPatch!) { updateServer(input: { filter: $f, set: $set }) { numUids } }`,
			vars: map[string]any{
				"f":   map[string]any{"orbId": map[string]any{"eq": "ns:server-A"}},
				"set": patch,
			},
			wantSetVar: "set",
		},
		{
			name:       "reference to a variable that was never supplied",
			query:      canonical,
			vars:       map[string]any{"set": patch},
			wantSetVar: "set",
		},
		{
			name:       "selector variable holding a non-string",
			query:      canonical,
			vars:       map[string]any{"orbId": 42, "set": patch},
			wantSetVar: "set",
		},
		{
			name:      "two patch variables — ambiguous, so neither is chosen",
			query:     `mutation U($orbId: String!, $p: ServerPatch!, $q: ServerPatch!) { x: updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $p }) { numUids } y: updateIdracSettings(input: { filter: { orbId: { eq: $orbId } }, set: $q }) { numUids } }`,
			vars:      map[string]any{"orbId": "ns:server-A", "p": patch, "q": patch},
			wantOrbID: "", wantOrbIDVar: "", wantSetVar: "",
		},
		{
			name:      "no patch at all (a delete)",
			query:     `mutation D($orbId: String!) { deleteServer(filter: { orbId: { eq: $orbId } }) { numUids } }`,
			vars:      map[string]any{"orbId": "ns:server-A"},
			wantOrbID: "ns:server-A", wantOrbIDVar: "orbId", wantSetVar: "",
		},
		{
			name:      "offset: $x must not be mistaken for a patch",
			query:     `mutation U($orbId: String!, $n: Int!) { updateServer(input: { filter: { orbId: { eq: $orbId } } }, offset: $n) { numUids } }`,
			vars:      map[string]any{"orbId": "ns:server-A", "n": map[string]any{"nope": true}},
			wantOrbID: "ns:server-A", wantOrbIDVar: "orbId", wantSetVar: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveWriteSelector(&gqlRequest{Query: tc.query, Variables: tc.vars})
			if got.OrbID != tc.wantOrbID {
				t.Errorf("OrbID = %q, want %q", got.OrbID, tc.wantOrbID)
			}
			if got.OrbIDVar != tc.wantOrbIDVar {
				t.Errorf("OrbIDVar = %q, want %q", got.OrbIDVar, tc.wantOrbIDVar)
			}
			if got.SetVar != tc.wantSetVar {
				t.Errorf("SetVar = %q, want %q", got.SetVar, tc.wantSetVar)
			}
		})
	}
}
