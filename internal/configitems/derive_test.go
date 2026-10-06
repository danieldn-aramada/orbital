package configitems

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSchema is a controllable SchemaClient: it counts calls so the cache tests
// can assert that a page load triggers no network work, and can be switched
// between failing and serving to exercise degraded mode.
type fakeSchema struct {
	sdl        string
	types      map[string]TypeInfo
	iface      []string
	err        error
	sdlCalls   int
	introCalls int
}

func (f *fakeSchema) DeployedSDL(context.Context) (string, error) {
	f.sdlCalls++
	if f.err != nil {
		return "", f.err
	}
	return f.sdl, nil
}

func (f *fakeSchema) Introspect(context.Context) (map[string]TypeInfo, []string, error) {
	f.introCalls++
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.types, f.iface, nil
}

func sample() *fakeSchema {
	return &fakeSchema{
		sdl:   "type Server { hostname: String }",
		iface: []string{"id", "orbId", "name", "namespace", "version", "createdAt", "createdBy", "updatedAt", "updatedBy"},
		types: map[string]TypeInfo{
			"Server": TypeInfo{Fields: []DerivedField{
				{Name: "id", Editable: true},
				{Name: "orbId", Editable: true},
				{Name: "name", Editable: true},
				{Name: "version", Editable: true},
				{Name: "hostname", Editable: true},
				{Name: "model", Editable: true},
				{Name: "idracSettings", Kind: "OBJECT", TypeName: "IdracSettings"}, // edge — not editable
				{Name: "racks", Kind: "OBJECT", TypeName: "Rack", IsList: true},    // list — not editable
			}},
			"DataCenter": TypeInfo{Fields: []DerivedField{
				{Name: "name", Editable: true},
				{Name: "model", Editable: true},
				{Name: "assetDataV2", Editable: true},
				{Name: "servers", Kind: "OBJECT", TypeName: "Server", IsList: true},
			}},
			"Rack": TypeInfo{Fields: []DerivedField{
				{Name: "name", Editable: true},
				{Name: "uHeight", Editable: true},
			}},
			"IdracSettings": TypeInfo{Fields: []DerivedField{
				{Name: "firmwareVersion", Editable: true},
				{Name: "sshEnabled", Editable: true},
			}},
		},
	}
}

// sampleViews writes the views document the fixture schema is resolved against.
//
// The resolver needs one: editability comes from the views config now, so a
// resolver pointed at no file cannot answer "what is editable" at all — which
// is the correct failure, and not the one these tests are about.
func sampleViews(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "views.yaml")
	if err := os.WriteFile(p, []byte(`
pages:
  Server:
    tabs:
      - { path: idracSettings, editable: true }
types:
  DataCenter:
    fields:
      name: { editable: true }
  Rack:
    fields:
      name: { editable: true }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func eq(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %v, want %v", label, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: got %v, want %v", label, got, want)
			return
		}
	}
}
func TestBeforeSelection_GeneratedFromTheSameDerivedSet(t *testing.T) {
	derived := map[string][]string{
		"Server":        {"hostname", "model"},
		"DataCenter":    {"assetDataV2", "model"},
		"IdracSettings": {"firmwareVersion", "sshEnabled"},
	}
	lookup := func(t string) []string { return derived[t] }
	got := BeforeSelection("Server", lookup)
	want := "id orbId name version hostname model"
	if got != want {
		t.Errorf("BeforeSelection(Server)\n  got:  %s\n  want: %s", got, want)
	}

	// `name` is in the head already and must not be repeated.
	dc := BeforeSelection("DataCenter", lookup)
	if dc != "id orbId name version assetDataV2 model" {
		t.Errorf("BeforeSelection(DataCenter) = %s", dc)
	}
}

// A before-fetch that reaches into an owned child buys nothing: `changes` is
// keys(before) ∩ keys(set), and no single mutation writes both a type's scalars
// and its child's. Re-adding the sub-selection puts fields in the stored
// snapshot that no diff can ever reach.
func TestBeforeSelection_DoesNotReachIntoOwnedChildren(t *testing.T) {
	lookup := func(t string) []string {
		return map[string][]string{
			"Server":        {"hostname"},
			"IdracSettings": {"sshEnabled"},
		}[t]
	}
	if got := BeforeSelection("Server", lookup); strings.Contains(got, "{") {
		t.Errorf("before-fetch must be flat; got a sub-selection: %s", got)
	}
}

// Acceptance 13: DGraph unreachable and nothing cached — the editor must refuse
// with a STATED REASON, never an empty field list. An empty list reads as "this
// type has no fields", which is a different and misleading claim.
func TestResolver_RefusesWithReasonWhenSchemaUnavailable(t *testing.T) {
	f := sample()
	f.err = errors.New("connection refused")
	r := NewResolver(f, time.Minute).WithViews(sampleViews(t), "", "")

	got, err := r.Fields(context.Background(), "Server")
	if err == nil {
		t.Fatalf("expected an error, got fields %v", got)
	}
	if got != nil {
		t.Errorf("must return nil fields, not an empty list that reads as 'no fields': %v", got)
	}
	if !contains(err.Error(), "schema") || !contains(err.Error(), "connection refused") {
		t.Errorf("error must name the reason; got %q", err)
	}
}

// Acceptance 14: unreachable at boot, reachable later — works with NO restart.
func TestResolver_SelfHealsWithoutRestart(t *testing.T) {
	f := sample()
	f.err = errors.New("connection refused")
	r := NewResolver(f, time.Minute).WithViews(sampleViews(t), "", "")

	if _, err := r.Fields(context.Background(), "Server"); err == nil {
		t.Fatal("expected failure while DGraph is down")
	}

	f.err = nil // DGraph comes up; no restart, no new Resolver
	got, err := r.Fields(context.Background(), "Server")
	if err != nil {
		t.Fatalf("must self-heal once DGraph returns: %v", err)
	}
	eq(t, "Server after self-heal", got, []string{"hostname", "model"})
}

// The caching guarantee: repeated reads (every page load) do NO network work.
func TestResolver_ServesFromCacheWithoutRefetching(t *testing.T) {
	f := sample()
	r := NewResolver(f, time.Minute).WithViews(sampleViews(t), "", "")
	ctx := context.Background()

	if _, err := r.Fields(ctx, "Server"); err != nil {
		t.Fatal(err)
	}
	sdlAfterFirst, introAfterFirst := f.sdlCalls, f.introCalls

	for i := 0; i < 50; i++ {
		if _, err := r.Fields(ctx, "Server"); err != nil {
			t.Fatal(err)
		}
	}
	if f.sdlCalls != sdlAfterFirst || f.introCalls != introAfterFirst {
		t.Errorf("50 reads inside the check window must do zero network work; SDL %d->%d, introspect %d->%d",
			sdlAfterFirst, f.sdlCalls, introAfterFirst, f.introCalls)
	}
}

// …but a changed DEPLOYED schema re-derives, without a restart.
func TestResolver_ReDerivesWhenDeployedSchemaChanges(t *testing.T) {
	f := sample()
	now := time.Now()
	r := NewResolver(f, time.Minute).WithViews(sampleViews(t), "", "")
	r.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := r.Fields(ctx, "Server"); err != nil {
		t.Fatal(err)
	}
	introAfterFirst := f.introCalls

	// Window elapses, but the schema is unchanged: re-check the hash, do NOT
	// re-introspect.
	now = now.Add(2 * time.Minute)
	if _, err := r.Fields(ctx, "Server"); err != nil {
		t.Fatal(err)
	}
	if f.introCalls != introAfterFirst {
		t.Errorf("unchanged schema must not re-introspect; introspect calls %d -> %d", introAfterFirst, f.introCalls)
	}

	// Now the deployed schema really changes: a new field appears.
	f.sdl = "type Server { hostname: String serialNumber: String }"
	f.types["Server"] = TypeInfo{Fields: append(f.types["Server"].Fields, DerivedField{Name: "serialNumber", Editable: true})}
	now = now.Add(2 * time.Minute)

	got, err := r.Fields(ctx, "Server")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "Server after schema change", got, []string{"hostname", "model", "serialNumber"})
	if f.introCalls == introAfterFirst {
		t.Error("a changed deployed schema must trigger re-introspection")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A misspelled annotation is a valid docstring that silently does nothing, and
// the vocabulary shrinking to TWO words makes that MORE likely to slip
// through, not less: there is no longer a crowd of near-neighbours to make one
// look odd. `orbIdSufix:` derives the wrong orbId for every child of its type.
func TestUnknownAnnotations_SurfacesLikelyTypos(t *testing.T) {
	got := UnknownAnnotations("orbIdSufix: idrac")
	if len(got) != 1 || got[0] != "orbIdSufix: idrac" {
		t.Errorf("a typo'd annotation must be reported, got %v", got)
	}
	for _, valid := range []string{"jsonString", "orbIdSuffix: idrac"} {
		if u := UnknownAnnotations(valid); len(u) != 0 {
			t.Errorf("%q is a valid annotation and must not be reported as unknown; got %v", valid, u)
		}
	}
	// An annotation deleted from the vocabulary must now report as unknown —
	// otherwise a schema still carrying it reads as configured and does nothing.
	for _, gone := range []string{"editorIgnored", "derivesIdFrom: server"} {
		if len(UnknownAnnotations(gone)) != 1 {
			t.Errorf("%q left the vocabulary; a schema still carrying it must say so", gone)
		}
	}
	if len(UnknownAnnotations("A human sentence about this field.")) != 0 {
		t.Error("ordinary prose must not be reported as an annotation")
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
func TestApplyOrder(t *testing.T) {
	alphabetical := []string{"cni", "clusterType", "description", "environment", "kubernetesVersion", "provider"}

	t.Run("pinned lead, the rest keep their order", func(t *testing.T) {
		got := ApplyOrder(alphabetical, []string{"provider", "clusterType"})
		want := []string{"provider", "clusterType", "cni", "description", "environment", "kubernetesVersion"}
		assertOrder(t, got, want)
	})

	t.Run("an unpinned NEW field still appears", func(t *testing.T) {
		// The whole reason the order is a prefix and not a list.
		withNew := append([]string{"brandNewField"}, alphabetical...)
		got := ApplyOrder(withNew, []string{"provider"})
		found := false
		for _, f := range got {
			if f == "brandNewField" {
				found = true
			}
		}
		if !found {
			t.Fatalf("a field the pin does not name must still be rendered: %v", got)
		}
		if len(got) != len(withNew) {
			t.Fatalf("ApplyOrder dropped fields: got %v, want all of %v", got, withNew)
		}
	})

	t.Run("a pin naming a field this type lacks is skipped", func(t *testing.T) {
		// An interface's pin is inherited by every implementation, and an
		// implementation legitimately lacks fields its siblings add.
		got := ApplyOrder([]string{"cni", "provider"}, []string{"provider", "clusterType", "cni"})
		assertOrder(t, got, []string{"provider", "cni"})
	})

	t.Run("no pin leaves the order untouched", func(t *testing.T) {
		assertOrder(t, ApplyOrder(alphabetical, nil), alphabetical)
	})

	t.Run("a repeated pin does not duplicate the field", func(t *testing.T) {
		assertOrder(t, ApplyOrder([]string{"a", "b"}, []string{"b", "b"}), []string{"b", "a"})
	})
}

func assertOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func contains2(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
