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
		name  string
		input any
		want  float64
	}{
		{name: "float64", input: float64(3.14), want: 3.14},
		{name: "zero float64", input: float64(0), want: 0},
		{name: "int", input: int(7), want: 7},
		{name: "json.Number integer", input: json.Number("42"), want: 42},
		{name: "json.Number float", input: json.Number("1.5"), want: 1.5},
		{name: "nil returns zero", input: nil, want: 0},
		{name: "string returns zero", input: "not a number", want: 0},
		{name: "bool returns zero", input: true, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toFloat64(tt.input)
			if got != tt.want {
				t.Errorf("toFloat64(%v) = %v, want %v", tt.input, got, tt.want)
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

