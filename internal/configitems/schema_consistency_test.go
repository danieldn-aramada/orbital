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
// where ownership comes from.
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

// Ownership DERIVES from the schema: every non-null back-edge must produce a
// Contains entry on its parent, and the one type that cannot must be declared.
//
// This replaced a gate that forced the CONFIG to declare what the schema already
// required. Derivation makes that unnecessary — a non-null back-edge is now the
// ownership statement itself — so what is left to check is that the derivation
// actually covers them. A gap here is an orphan behind a dangling non-null edge:
// DGraph propagates the missing field to the ROOT of any query selecting it, and
// DGRAPH.md records one such delete breaking export for a whole data centre.
func TestOwnership_CoversEveryNonNullBackEdge(t *testing.T) {
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
		for _, c := range dependentsOf(types, cfg, e.Parent) {
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
	if len(cfg.OwnerReferences["NetworkInterface"]) == 0 {
		t.Error("NetworkInterface's owner is an XOR across three nullable edges; without a " +
			"ownerReferences: entry a server delete leaves its NICs behind")
	}
}

// typesFromSDL builds the minimal TypeInfo ownership needs — field names,
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

// Every ConfigItem type must DECLARE its orbId shape.
//
// This is the "you forgot Foo" guard, and it is a build-time test rather than a
// runtime warning on purpose: adding a type without a pattern goes red in
// `make test-unit` before it is ever applied to a graph, where a log line would
// have been missed. It reads the file, not a deployed schema, so it needs no
// services — same contract as TestSchemaMatchesViews above.
//
// `orbIdPattern: external` satisfies it for a type whose id is assigned
// elsewhere. The opt-out is explicit and greppable precisely so that forgetting
// and deciding look different in review.
func TestEveryConfigItem_DeclaresAnOrbIdPattern(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	typeRe := regexp.MustCompile(`^type\s+(\w+)\s+implements\s+([^{]*)\{`)

	var missing []string
	seen := 0
	for i, line := range lines {
		m := typeRe.FindStringSubmatch(line)
		if m == nil || !strings.Contains(m[2], "ConfigItem") {
			continue
		}
		seen++
		if !strings.Contains(docstringAbove(lines, i), OrbIDPatternAnnotation) {
			missing = append(missing, m[1])
		}
	}
	if seen == 0 {
		t.Fatal("parsed no ConfigItem types — the parser, not the schema, is wrong")
	}
	if len(missing) > 0 {
		t.Errorf("these ConfigItem types declare no %s — add one naming the natural key, "+
			"or `%s %s` if the id is assigned outside orbital: %s",
			OrbIDPatternAnnotation, OrbIDPatternAnnotation, OrbIDPatternExternal, strings.Join(missing, ", "))
	}
}

// Each shipped pattern must PARSE and reference only fields the schema has.
// A pattern is identity: one that cannot resolve builds nothing, and silently
// falls through to the next alternative or fails construction entirely.
func TestShippedOrbIdPatterns_ParseAndResolve(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	fields, implCI := parseSchemaTypes(string(src))
	lines := strings.Split(string(src), "\n")
	typeRe := regexp.MustCompile(`^type\s+(\w+)\s+implements\s+([^{]*)\{`)

	for i, line := range lines {
		m := typeRe.FindStringSubmatch(line)
		if m == nil || !implCI[m[1]] {
			continue
		}
		typeName := m[1]
		for _, raw := range annotationValues(docstringAbove(lines, i), OrbIDPatternAnnotation) {
			p, err := ParseOrbIDPattern(raw)
			if err != nil {
				t.Errorf("%s: orbIdPattern %q does not parse: %v", typeName, raw, err)
				continue
			}
			if p.External {
				continue
			}
			for _, path := range p.Paths() {
				if path == "kind" {
					continue
				}
				if !resolvesFrom(typeName, path, fields) {
					t.Errorf("%s: orbIdPattern %q references %q, which the schema cannot resolve",
						typeName, raw, path)
				}
			}
		}
	}
}

// resolvesFrom walks a dotted placeholder path from a starting type, hopping
// through fieldTargets at each edge. Inherited interface fields count: DGraph
// forbids redeclaring one on an implementor, so `name` lives only on ConfigItem.
func resolvesFrom(typeName, path string, fields map[string]map[string]bool) bool {
	cur := typeName
	for _, seg := range strings.Split(path, ".") {
		if !hasFieldOrInherited(cur, seg, fields) {
			return false
		}
		next, ok := fieldTargets[cur+"."+seg]
		if !ok {
			for _, in := range schemaImplements[cur] {
				if n, ok2 := fieldTargets[in+"."+seg]; ok2 {
					next, ok = n, true
					break
				}
			}
		}
		cur = next
	}
	return true
}

func hasFieldOrInherited(typeName, field string, fields map[string]map[string]bool) bool {
	if fs, ok := fields[typeName]; ok && fs[field] {
		return true
	}
	for _, in := range schemaImplements[typeName] {
		if fs, ok := fields[in]; ok && fs[field] {
			return true
		}
	}
	// An INTERFACE-typed edge resolves through its implementors. ClusterBackup
	// points at `KubernetesCluster`, which is an interface that does NOT itself
	// implement ConfigItem — so `name` exists only on EksaKubernetesCluster,
	// and that is the node actually stored. Without this hop every
	// cluster-derived pattern reads as unresolvable while working fine.
	for impl, ifaces := range schemaImplements {
		for _, in := range ifaces {
			if in != typeName {
				continue
			}
			if hasFieldOrInherited(impl, field, fields) {
				return true
			}
		}
	}
	return false
}

// docstringAbove returns the """...""" block immediately preceding line i, or
// "". Adjacency is the rule: a docstring separated from its type by anything
// else is not that type's docstring, and GraphQL agrees.
func docstringAbove(lines []string, i int) string {
	if i == 0 {
		return ""
	}
	end := i - 1
	if strings.TrimSpace(lines[end]) == "" || !strings.HasSuffix(strings.TrimSpace(lines[end]), `"""`) {
		return ""
	}
	// Single-line form: """...""" on one line.
	if t := strings.TrimSpace(lines[end]); strings.HasPrefix(t, `"""`) && len(t) > 6 {
		return strings.TrimSuffix(strings.TrimPrefix(t, `"""`), `"""`)
	}
	for start := end - 1; start >= 0; start-- {
		if strings.TrimSpace(lines[start]) == `"""` {
			return strings.Join(lines[start+1:end], "\n")
		}
	}
	return ""
}
