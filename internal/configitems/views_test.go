package configitems

import (
	"strings"
	"testing"
)

func TestLabel_IsDisplayNotSlug(t *testing.T) {
	cases := map[string]string{
		"network-devices": "Network Devices",
		"servers":         "Servers",
		"ip-addresses":    "Ip Addresses",
		"idrac-settings":  "Idrac Settings",
		// An annotated slug carries straight through, which is the point.
		"clusters": "Clusters",
	}
	for in, want := range cases {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}

	// The slug is ALREADY plural. Pluralising again turned "addresses" into
	// "addresseses" on every page whose type name ends in s.
	if got := Label("ip-addresses"); strings.HasSuffix(got, "eses") {
		t.Errorf("Label double-pluralised: %q", got)
	}
}
func TestMetaFields(t *testing.T) {
	tests := []struct {
		name  string
		iface []string
		want  []string
	}{
		{
			name:  "canonical ConfigItem interface, in display order",
			iface: []string{"id", "namespace", "orbId", "name", "createdBy", "createdAt", "updatedBy", "updatedAt", "version"},
			want:  []string{"name", "namespace", "orbId", "version", "createdBy", "createdAt", "updatedAt", "updatedBy"},
		},
		{
			// A field added to the interface must appear without a code change
			// here — that is the whole reason the list is derived.
			name:  "an unknown interface field is appended, not dropped",
			iface: []string{"orbId", "version", "retiredAt"},
			want:  []string{"orbId", "version", "retiredAt"},
		},
		{
			name:  "no interface fields",
			iface: nil,
			want:  []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metaFields(tt.iface)
			if len(got) != len(tt.want) {
				t.Fatalf("metaFields() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("metaFields()[%d] = %q, want %q (full: %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}
func viewFor(t *testing.T, views []View, typeName string) View {
	t.Helper()
	for _, v := range views {
		if v.Type == typeName {
			return v
		}
	}
	t.Fatalf("no view for %s", typeName)
	return View{}
}

// A value column may cross a list; a count may not reach past one, because the
// response would hold one aggregate per element and the cell would show their
// list instead of a total.
func TestResolveColumnPaths_ListHops(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {Fields: []DerivedField{
			{Name: "kubernetesNodes", Kind: "OBJECT", TypeName: "KubernetesNode", IsList: true},
		}},
		"KubernetesNode": {Fields: []DerivedField{
			{Name: "role", Kind: "SCALAR"},
			{Name: "taints", Kind: "OBJECT", TypeName: "Taint", IsList: true},
		}},
		"Taint": {Fields: []DerivedField{{Name: "key", Kind: "SCALAR"}}},
	}
	for _, tc := range []struct {
		path     string
		resolves bool
	}{
		{"kubernetesNodes.role", true},
		{"kubernetesNodes.count", true},
		{"kubernetesNodes.taints.key", true},
		{"kubernetesNodes.taints.count", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			cols, warn := resolveColumnPaths(types, types["Server"], "Server", nil, []string{tc.path})
			if got := len(cols) == 1; got != tc.resolves {
				t.Fatalf("resolves = %v, want %v (warnings: %v)", got, tc.resolves, warn)
			}
		})
	}
}
