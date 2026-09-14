package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestAuthorizeMutation_ExternalJWTRole pins the regression where external-jwt
// callers (ORBITAL_AUTH_MODE=external-jwt) were 403'd on every GraphQL mutation:
// they have no users-table row, so the old user_id→DB check always failed. The
// mutation gate must honor the pre-mapped context role instead. No DB needed —
// the external-jwt path never touches h.db.
func TestAuthorizeMutation_ExternalJWTRole(t *testing.T) {
	h := &GraphQL{} // db nil — external-jwt path is role-only
	for _, tc := range []struct {
		role string
		want bool
	}{
		{"admin", true},     // AEP default (ORBITAL_JWT_DEFAULT_ROLE) — must be allowed
		{"dev", true},       // minimum for mutations
		{"readonly", false}, // below dev — denied even via external-jwt
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
// (no authz backend), unchanged by the external-jwt short-circuit.
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
