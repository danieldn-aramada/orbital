package configitems

import (
	"context"
	"errors"
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
				{Name: "idracSettings"}, // edge — not editable
				{Name: "racks"},         // list — not editable
			}},
			"DataCenter": TypeInfo{Doc: "editable: name", Fields: []DerivedField{
				{Name: "name", Editable: true},
				{Name: "model", Editable: true},
				{Name: "assetDataV2", Editable: true},
				{Name: "servers"},
			}},
			"Rack": TypeInfo{Doc: "editable: name", Fields: []DerivedField{
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

// Acceptance 5 + 6: interface fields are never editable on any type, and every
// edge is excluded — both derived, never a literal list.
func TestDerive_ExcludesInterfaceFieldsAndEdges(t *testing.T) {
	f := sample()
	got := Derive(f.types, f.iface)

	eq(t, "Server", got["Server"], []string{"hostname", "model"})

	for _, banned := range []string{"id", "orbId", "version", "createdAt", "updatedBy"} {
		for _, f := range got["Server"] {
			if f == banned {
				t.Errorf("Server: %q is a ConfigItem interface field and must never be editable", banned)
			}
		}
	}
	for _, edge := range []string{"idracSettings", "racks", "servers"} {
		for _, f := range append(got["Server"], got["DataCenter"]...) {
			if f == edge {
				t.Errorf("%q is an edge and must be excluded from the scalar set", edge)
			}
		}
	}
}

// Acceptance 7: `name` IS editable on DataCenter and Rack and NOT on Server.
// Naive type-minus-interface derivation gets this wrong, because `name` lives
// on the ConfigItem interface.
// `name` is re-admitted by the type's `editable:` annotation — a hardcoded Go
// map until 2026-09-29. The fixture carries the annotation because the SCHEMA
// carries it: DataCenter and Rack declare `editable: name`, Server does not.
func TestDerive_ReadmitsNameOnDeclaredTypes(t *testing.T) {
	f := sample()
	got := Derive(f.types, f.iface)

	eq(t, "DataCenter", got["DataCenter"], []string{"assetDataV2", "model", "name"})
	eq(t, "Rack", got["Rack"], []string{"name", "uHeight"})

	for _, fld := range got["Server"] {
		if fld == "name" {
			t.Error("Server: `name` must NOT be editable — it is editable only on DataCenter and Rack")
		}
	}
}

// BeforeFields is generated from the same model, so the two cannot disagree.
func TestBeforeSelection_GeneratedFromTheSameDerivedSet(t *testing.T) {
	f := sample()
	derived := Derive(f.types, f.iface)
	children := func(parent string) []Type {
		if parent == "Server" {
			return []Type{{Name: "IdracSettings", ChildField: "idracSettings"}}
		}
		return nil
	}
	lookup := func(t string) []string { return derived[t] }
	got := BeforeSelection("Server", lookup, children)
	want := "id orbId name version hostname model idracSettings { firmwareVersion sshEnabled }"
	if got != want {
		t.Errorf("BeforeSelection(Server)\n  got:  %s\n  want: %s", got, want)
	}

	// `name` is in the head already and must not be repeated.
	dc := BeforeSelection("DataCenter", lookup, children)
	if dc != "id orbId name version assetDataV2 model" {
		t.Errorf("BeforeSelection(DataCenter) = %s", dc)
	}
}

// Acceptance 13: DGraph unreachable and nothing cached — the editor must refuse
// with a STATED REASON, never an empty field list. An empty list reads as "this
// type has no fields", which is a different and misleading claim.
func TestResolver_RefusesWithReasonWhenSchemaUnavailable(t *testing.T) {
	f := sample()
	f.err = errors.New("connection refused")
	r := NewResolver(f, time.Minute)

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
	r := NewResolver(f, time.Minute)

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
	r := NewResolver(f, time.Minute)
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
	r := NewResolver(f, time.Minute)
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

// editorIgnored is the ONLY opt-out (editable-unless-annotated), and it is
// editor-scoped: the field stays writable through the API so a scanner can
// still write it.
func TestDerive_EditorIgnoredAnnotationSuppressesAField(t *testing.T) {
	// A type editorIgnoredBridge does not list, so this asserts the ANNOTATION
	// and not the temporary bridge.
	types := map[string]TypeInfo{
		"AnnotationFixture": {Fields: []DerivedField{
			{Name: "model", Editable: true},
			{Name: "capacityBytes", Editable: true, Doc: "editorIgnored"},
			{Name: "wwn", Editable: true, Doc: "The world-wide name.\neditorIgnored"},
		}},
	}
	got := Derive(types, nil)
	eq(t, "AnnotationFixture", got["AnnotationFixture"], []string{"model"})
}

// Prose that merely mentions the word must NOT disable a field — otherwise a
// docstring explaining the convention would silently switch it on.
func TestIsEditorIgnored_RequiresAnExactLineNotProse(t *testing.T) {
	cases := []struct {
		doc  string
		want bool
	}{
		{"editorIgnored", true},
		{"  editorIgnored  ", true},
		{"Human text.\neditorIgnored", true},
		{"", false},
		{"this field is not editorIgnored by default", false},
		{"editorIgnoredMaybe", false},
		{"editorIgnroed", false}, // the typo case: must NOT match
	}
	for _, c := range cases {
		if got := IsEditorIgnored(c.doc); got != c.want {
			t.Errorf("IsEditorIgnored(%q) = %v, want %v", c.doc, got, c.want)
		}
	}
}

// A typo'd annotation is silent by construction, so something must surface it.
func TestUnknownAnnotations_SurfacesLikelyTypos(t *testing.T) {
	got := UnknownAnnotations("editorIgnroed")
	if len(got) != 1 || got[0] != "editorIgnroed" {
		t.Errorf("a typo'd annotation must be reported, got %v", got)
	}
	if len(UnknownAnnotations("editorIgnored")) != 0 {
		t.Error("the valid annotation must not be reported as unknown")
	}
	if len(UnknownAnnotations("A human sentence about this field.")) != 0 {
		t.Error("ordinary prose must not be reported as an annotation")
	}
}

// The environment-coupling defence: once editability comes from the DEPLOYED
// schema, an environment on an older schema.graphql silently gets MORE editable
// fields. Nothing errors — the count is the only signal, so it has to be right.
func TestAnnotations_ReportsResolvedCountAndTypos(t *testing.T) {
	types := map[string]TypeInfo{
		"StorageDevice": {Fields: []DerivedField{
			{Name: "model", Editable: true},
			{Name: "capacityBytes", Editable: true, Doc: "editorIgnored"},
			{Name: "wwn", Editable: true, Doc: "editorIgnored"},
		}},
		"Server": {Fields: []DerivedField{
			{Name: "hostname", Editable: true},
			{Name: "serialNumber", Editable: true, Doc: "editorIgnroed"}, // typo
		}},
	}
	rep := Annotations(types)

	if rep.EditorIgnoredTotal != 2 {
		t.Errorf("EditorIgnoredTotal = %d, want 2", rep.EditorIgnoredTotal)
	}
	if rep.EditorIgnored["StorageDevice"] != 2 {
		t.Errorf("StorageDevice count = %d, want 2", rep.EditorIgnored["StorageDevice"])
	}
	if _, ok := rep.EditorIgnored["Server"]; ok {
		t.Error("Server has only a TYPO'd annotation and must not be counted as suppressed")
	}
	if len(rep.Unknown) != 1 || rep.Unknown[0] != "Server.serialNumber: editorIgnroed" {
		t.Errorf("the typo must be reported with its location, got %v", rep.Unknown)
	}
}

// An environment whose annotations are absent reports ZERO — which is the
// signal. It must not silently look the same as a correctly annotated one.
func TestAnnotations_AbsentAnnotationsReportZeroNotSilence(t *testing.T) {
	types := map[string]TypeInfo{
		"AnnotationFixture": {Fields: []DerivedField{
			{Name: "model", Editable: true},
			{Name: "capacityBytes", Editable: true}, // older schema: no annotation
		}},
	}
	rep := Annotations(types)
	if rep.EditorIgnoredTotal != 0 {
		t.Errorf("EditorIgnoredTotal = %d, want 0", rep.EditorIgnoredTotal)
	}
	if len(rep.Unknown) != 0 {
		t.Errorf("no annotations at all means nothing unknown either, got %v", rep.Unknown)
	}
	// And the consequence the count is warning about: the field IS editable here.
	got := Derive(types, nil)
	if !containsStr(got["AnnotationFixture"], "capacityBytes") {
		t.Error("without the annotation the field is editable — that is the hazard the count surfaces")
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

// TestOrderFor parses the `order:` annotation.
func TestOrderFor(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{"absent", "", nil},
		{"single", "order: name", []string{"name"}},
		{"spaces trimmed", "order:  name ,  provider ,clusterType ", []string{"name", "provider", "clusterType"}},
		// The real form: several annotations share one docstring, because
		// GraphQL allows a declaration only ONE description block.
		{"alongside another annotation", "slug: clusters\norder: name, provider", []string{"name", "provider"}},
		{"empty value is not an order", "order:", nil},
		{"prose is not an annotation", "orders arrive here", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OrderFor(tt.doc)
			if len(got) != len(tt.want) {
				t.Fatalf("OrderFor(%q) = %v, want %v", tt.doc, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("OrderFor(%q)[%d] = %q, want %q", tt.doc, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestApplyOrder covers the partial-order rule.
//
// Regression class: someone "simplifies" this into a complete ordered list. A
// complete list is a FROZEN view — a field added in a later release has no
// place in it and silently never appears, which is the exact failure NetBox
// carries with its saved column lists. The tail must stay open.
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

// TestEditableInterfaceFields covers the annotation that replaced the hardcoded
// readmitInterfaceFields map.
//
// Regression class: `name` silently ceasing to be editable on DataCenter and
// Rack. "Type fields minus interface fields" removes it from every type, and
// nothing about the page looks wrong afterwards — the field just stops being
// offered, which is a capability regression nobody sees.
func TestEditableInterfaceFields(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{"absent", "", nil},
		{"single", "editable: name", []string{"name"}},
		{"several, spaces trimmed", "editable: name , namespace", []string{"name", "namespace"}},
		// The real form: GraphQL allows one description block, so annotations
		// share it line by line.
		{"alongside another", "slug: dcs\neditable: name", []string{"name"}},
		{"empty value is not a list", "editable:", nil},
		{"prose is not an annotation", "editable by admins only", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EditableInterfaceFields(tt.doc)
			if len(got) != len(tt.want) {
				t.Fatalf("EditableInterfaceFields(%q) = %v, want %v", tt.doc, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestDerive_ReadmitsOnlyWhatTheAnnotationNames pins the behaviour end to end.
func TestDerive_ReadmitsOnlyWhatTheAnnotationNames(t *testing.T) {
	iface := []string{"orbId", "name", "version"}
	types := map[string]TypeInfo{
		"DataCenter": {Doc: "editable: name", Fields: []DerivedField{
			{Name: "name", Editable: true}, {Name: "model", Editable: true}, {Name: "orbId", Editable: true},
		}},
		"Server": {Fields: []DerivedField{
			{Name: "name", Editable: true}, {Name: "hostname", Editable: true},
		}},
		// A pin naming a field the type does not have is inert, not an error:
		// a page must never fail to render because an annotation went stale.
		"Rack": {Doc: "editable: nosuchfield", Fields: []DerivedField{
			{Name: "uHeight", Editable: true},
		}},
	}
	got := Derive(types, iface)

	if !contains2(got["DataCenter"], "name") {
		t.Errorf("DataCenter must re-admit name: %v", got["DataCenter"])
	}
	if contains2(got["DataCenter"], "orbId") {
		t.Errorf("an interface field NOT named must stay out: %v", got["DataCenter"])
	}
	if contains2(got["Server"], "name") {
		t.Errorf("Server has no editable: annotation, so name must stay out: %v", got["Server"])
	}
	if len(got["Rack"]) != 1 || got["Rack"][0] != "uHeight" {
		t.Errorf("a stale annotation must be inert: %v", got["Rack"])
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

// TestDetailOnlyIsSeparateFromJSONString pins the layering.
//
// Regression class: re-conflating them. `jsonString` says WHAT a field holds
// (drives editor parsing and pretty-printing); `detailOnly` says WHERE it may
// appear. Inferring placement from content is what made "show assetDataV2 as a
// column" unexpressible and forced a Go change to hide it.
func TestDetailOnlyIsSeparateFromJSONString(t *testing.T) {
	info := TypeInfo{Fields: []DerivedField{
		{Name: "assetDataV2", Doc: "jsonString\ndetailOnly"},
		{Name: "smallJSON", Doc: "jsonString"},
		{Name: "bigText", Doc: "detailOnly"},
		{Name: "model", Doc: ""},
	}}

	detail := DetailOnlyFieldsFor(info)
	if len(detail) != 2 || detail[0] != "assetDataV2" || detail[1] != "bigText" {
		t.Errorf("DetailOnlyFieldsFor = %v, want [assetDataV2 bigText]", detail)
	}

	jsonFields := JSONStringFieldsFor(info)
	if len(jsonFields) != 2 || jsonFields[0] != "assetDataV2" || jsonFields[1] != "smallJSON" {
		t.Errorf("JSONStringFieldsFor = %v, want [assetDataV2 smallJSON]", jsonFields)
	}

	// The two that make the distinction load-bearing: JSON that IS a column,
	// and a non-JSON field that is not.
	if IsDetailOnly("jsonString") {
		t.Error("jsonString alone must not imply detailOnly — that was the conflation")
	}
	if !IsDetailOnly("detailOnly") || IsJSONString("detailOnly") {
		t.Error("detailOnly alone must not imply jsonString")
	}
}
