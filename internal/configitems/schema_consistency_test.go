package configitems

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The build-time drift guard: schema/schema.graphql against config/views.yaml.
//
// Parses the schema in the TEST ONLY — no runtime parser, no gqlparser
// dependency — to catch the drift the compiler cannot: an edge renamed, or a
// type removed, without the views config following. Mirrors NetBox's build-time
// parent_object guard.
//
// It reads the files, not a deployed graph, so it runs in `make test-unit` with
// no services. The runtime check against LIVE introspection is a different and
// non-negotiable thing (ViewConfig.Validate): a deployed schema can differ from
// the file, which is the whole exposure of keeping views in a separate artifact.
func TestSchemaMatchesViews(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	fields, implementsConfigItem := parseSchemaTypes(string(src))

	// Inherited fields count. DGraph forbids redeclaring an interface field on an
	// implementor, so EksaKubernetesCluster's `nodes` and every type's `name`
	// appear only on the interface — a check that missed that would reject the
	// correct config.
	hasField := func(typeName, field string) bool {
		if fs, ok := fields[typeName]; ok && fs[field] {
			return true
		}
		for _, in := range schemaImplements[typeName] {
			if fs, ok := fields[in]; ok && fs[field] {
				return true
			}
		}
		return false
	}
	fieldTarget := func(typeName, field string) string {
		if t := fieldTargets[typeName+"."+field]; t != "" {
			return t
		}
		for _, in := range schemaImplements[typeName] {
			if t := fieldTargets[in+"."+field]; t != "" {
				return t
			}
		}
		return ""
	}

	// (a) The mutation matcher covers every ConfigItem type the schema declares.
	//
	// It is DERIVED from the deployed schema at runtime, so this cannot drift in
	// the dangerous direction. The check stays because it is free and because the
	// consequence of a gap — an ungated, unaudited write — is the quietest
	// failure in this codebase.
	re := MutationRegexFor(sortedKeys(implementsConfigItem))
	for name := range implementsConfigItem {
		for _, verb := range []string{"add", "update", "delete"} {
			if !re.MatchString(verb + name) {
				t.Errorf("%s%s is not matched; its mutations would skip the approval gate "+
					"and produce no audit event", verb, name)
			}
		}
	}

	// (b) The degraded matcher must never MISS. When the deployed schema cannot
	// be read, over-matching is the correct answer: the audit path runs only on
	// mutations DGraph accepted, and the approval gate only ever refuses or
	// passes, so the cost of an extra match is nil and the cost of a miss is a
	// silent hole.
	for name := range implementsConfigItem {
		if !BroadMutationRegex.MatchString("update" + name) {
			t.Errorf("the degraded matcher misses update%s; it must over-match, never under-match", name)
		}
	}

	cfg, _, _, err := LoadViewConfig("../../config/views.yaml", "")
	if err != nil {
		t.Fatalf("load views config: %v", err)
	}

	// (c) Every tab path and ref names real fields. At runtime a stale one is
	// dropped and logged, which is right for a deployment and wrong for a commit:
	// the fix is free here and invisible there.
	for _, typeName := range sortedKeys(cfg.Pages) {
		if _, ok := fields[typeName]; !ok {
			t.Errorf("views config declares a page for %q, which schema.graphql does not declare", typeName)
			continue
		}
		for _, r := range cfg.Pages[typeName].Summary.Refs {
			if !hasField(typeName, r) {
				t.Errorf("views: %s ref %q is not a field on %s", typeName, r, typeName)
			}
		}
		for _, m := range cfg.Pages[typeName].Tabs {
			cur := typeName
			for _, seg := range strings.Split(m.Path, ".") {
				if !hasField(cur, seg) {
					t.Errorf("views: %s member %q — %s has no field %q", typeName, m.Path, cur, seg)
					break
				}
				cur = fieldTarget(cur, seg)
				if cur == "" {
					break
				}
			}
		}
	}
	for _, typeName := range sortedKeys(cfg.Types) {
		if _, ok := fields[typeName]; !ok {
			t.Errorf("views config declares a type %q, which schema.graphql does not declare", typeName)
			continue
		}
		for _, f := range sortedKeys(cfg.Types[typeName].Fields) {
			if !hasField(typeName, f) {
				t.Errorf("views: %s declares field %q, which is not on %s", typeName, f, typeName)
			}
		}
	}
}

// fieldTargets maps "Type.field" to the field's named type, populated by
// parseSchemaTypes. A package-level var rather than a return value because the
// parser has two callers and only one of them walks paths.
var fieldTargets map[string]string

// schemaImplements maps a type to the interfaces it declares, so a check can see
// the fields it inherits rather than only the ones it redeclares — which DGraph
// forbids it from doing at all.
var schemaImplements map[string][]string

// nonNullFields records "Type.field" -> whether the field is non-null, which is
// where containment comes from.
var nonNullFields map[string]bool

// parseSchemaTypes returns type/interface name -> set of field names, and the
// set of types whose declaration line names ConfigItem.
//
// A deliberately small line scanner — not a GraphQL parser. Taking a parser
// dependency to read one file in one test is the trade this codebase refuses.
func parseSchemaTypes(src string) (map[string]map[string]bool, map[string]bool) {
	fields := map[string]map[string]bool{}
	implCI := map[string]bool{}
	fieldTargets = map[string]string{}
	schemaImplements = map[string][]string{}
	nonNullFields = map[string]bool{}
	blockRe := regexp.MustCompile(`^(?:type|interface)\s+(\w+)([^{]*)\{`)
	fieldRe := regexp.MustCompile(`^\s+(\w+)\s*:\s*(\[?)(\w+)\]?(!?)`)

	cur := ""
	for _, line := range strings.Split(src, "\n") {
		if m := blockRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
			fields[cur] = map[string]bool{}
			if strings.Contains(m[2], "ConfigItem") {
				implCI[cur] = true
			}
			for _, w := range strings.Fields(strings.ReplaceAll(m[2], "&", " ")) {
				if w != "implements" {
					schemaImplements[cur] = append(schemaImplements[cur], w)
				}
			}
			continue
		}
		if cur == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "}") {
			cur = ""
			continue
		}
		if m := fieldRe.FindStringSubmatch(line); m != nil {
			fields[cur][m[1]] = true
			fieldTargets[cur+"."+m[1]] = m[3]
			nonNullFields[cur+"."+m[1]] = m[4] == "!"
		}
	}
	return fields, implCI
}

// Containment DERIVES from the schema: every non-null back-edge must produce a
// Contains entry on its parent, and the one type that cannot must be declared.
//
// This replaced a gate that forced the CONFIG to declare what the schema already
// required. Derivation makes that unnecessary — a non-null back-edge is now the
// containment statement itself — so what is left to check is that the derivation
// actually covers them. A gap here is an orphan behind a dangling non-null edge:
// DGraph propagates the missing field to the ROOT of any query selecting it, and
// DGRAPH.md records one such delete breaking export for a whole data centre.
func TestContainment_CoversEveryNonNullBackEdge(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	cfg, _, _, err := LoadViewConfig("../../config/views.yaml", "")
	if err != nil {
		t.Fatalf("load views config: %v", err)
	}
	types := typesFromSDL(string(src))

	for _, e := range NonNullBackEdges(string(src)) {
		if _, known := types[e.Parent]; !known {
			continue
		}
		found := false
		for _, c := range containedChildren(types, cfg, e.Parent) {
			if c.ChildType == e.Child {
				found = true
			}
		}
		if !found {
			t.Errorf("%s.%s is %s! but %s does not contain %s — deleting one would orphan it "+
				"behind a dangling non-null edge", e.Child, e.Field, e.Parent, e.Parent, e.Child)
		}
	}

	// The declared exception, which nullability cannot express.
	if len(cfg.Containment["NetworkInterface"]) == 0 {
		t.Error("NetworkInterface's owner is an XOR across three nullable edges; without a " +
			"containment: entry a server delete leaves its NICs behind")
	}
}

// typesFromSDL builds the minimal TypeInfo containment needs — field names,
// their targets and their nullability — without a running DGraph, so this stays
// in `make test-unit`.
func typesFromSDL(src string) map[string]TypeInfo {
	fields, _ := parseSchemaTypes(src)
	out := make(map[string]TypeInfo, len(fields))
	for typeName, fs := range fields {
		info := TypeInfo{Implements: schemaImplements[typeName]}
		for f := range fs {
			info.Fields = append(info.Fields, DerivedField{
				Name:     f,
				TypeName: fieldTargets[typeName+"."+f],
				NonNull:  nonNullFields[typeName+"."+f],
			})
		}
		out[typeName] = info
	}
	return out
}
