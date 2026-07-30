package metrics

import "testing"

// TestIsGraphQLPath guards the base-path regression: the GraphQL route is
// mounted under ORBITAL_BASE_PATH (e.g. "/orbital/graphql" in AKS), so a plain
// path == "/graphql" check silently drops every request when a base path is
// set — which is why orbital_graphql_operation_duration_seconds never recorded
// in the AKS deployment. The suffix match must accept the base-path'd form.
func TestIsGraphQLPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/graphql", true},         // local dev — no base path
		{"/orbital/graphql", true}, // AKS ORBITAL_BASE_PATH=/orbital — the regression this guards
		{"/api/v1/export", false},
		{"/datacenters/:id", false},
		{"/orbital/publish-history", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isGraphQLPath(tc.path); got != tc.want {
			t.Errorf("isGraphQLPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
