package configitems

import (
	"fmt"
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

	// (c) Every subgraph path names real fields. At runtime a stale one is
	// dropped and logged, which is right for a deployment and wrong for a commit:
	// the fix is free here and invisible there.
	for _, typeName := range sortedKeys(cfg.Pages) {
		if _, ok := fields[typeName]; !ok {
			t.Errorf("views config declares a page for %q, which schema.graphql does not declare", typeName)
			continue
		}
		for _, m := range cfg.Pages[typeName].Subgraph {
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

// Every NON-NULL relationship must have an `@hasInverse` partner.
//
// The delete preview names an ORPHAN — an entity left holding a non-null edge
// into what a delete removes — by reading that edge from the far side, and the
// cascade only selects edges it can walk back along. A non-null edge with no
// inverse is invisible to it: the delete would orphan through it and the
// preview would say nothing.
func TestEveryNonNullEdge_HasAnInverse(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	types := typesFromSDL(string(src))
	inverse := InverseEdges(string(src))
	for _, e := range NonNullBackEdges(string(src)) {
		if _, known := types[e.Parent]; !known {
			continue
		}
		if inverse[e.Child+"."+e.Field] == "" {
			t.Errorf("%s.%s is %s! with no @hasInverse partner — a delete of the %s would orphan it "+
				"and the delete preview could not see it", e.Child, e.Field, e.Parent, e.Parent)
		}
	}
}

// A subgraph member is DELETED with the page's root, so it must be something
// that cannot outlive that root: it holds a non-null edge back to its parent,
// or its orbId is built from that edge. A member with neither is a peer the
// page merely links to, and declaring it deletes an independent entity.
//
// It has happened twice. Server.oobIP (2026-10-19) destroyed a shareable IP
// with its server. NetworkDevice's virtualChassisMembers and
// networkInterfaceConnectedNetworkDevice (2026-11-08) took every VC peer and
// every cabled server port with a switch. Both were display-era leftovers from
// before 2026-10-14, when the subgraph also decided placement.
//
// Necessary, not sufficient: passing does not make an edge ownership (`!` means
// non-null and nothing more, DGRAPH.md). It only rules out declaring a peer.
// Only the LAST hop of a path dies, so only the last hop is checked.
func TestEverySubgraphMember_DependsOnItsParent(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	parseSchemaTypes(string(src))
	inverse := InverseEdges(string(src))

	// DGraph declares an interface's field once, on the interface, so a lookup
	// that stops at the type misses `KubernetesCluster.nodes` on an implementor.
	onTypeOrInterfaces := func(typeName string, look func(string) bool) bool {
		if look(typeName) {
			return true
		}
		for _, in := range schemaImplements[typeName] {
			if look(in) {
				return true
			}
		}
		return false
	}
	str := func(m map[string]string, typeName, field string) string {
		var out string
		onTypeOrInterfaces(typeName, func(n string) bool { out = m[n+"."+field]; return out != "" })
		return out
	}

	lines := strings.Split(string(src), "\n")
	typeRe := regexp.MustCompile(`^type\s+(\w+)\s`)
	idFrom := map[string]map[string]bool{}
	for i, line := range lines {
		m := typeRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, raw := range annotationValues(docstringAbove(lines, i), OrbIDPatternAnnotation) {
			p, err := ParseOrbIDPattern(raw)
			if err != nil || p.External {
				continue
			}
			for _, path := range p.Paths() {
				if idFrom[m[1]] == nil {
					idFrom[m[1]] = map[string]bool{}
				}
				idFrom[m[1]][strings.SplitN(path, ".", 2)[0]] = true
			}
		}
	}

	cfg, _, _, err := LoadViewConfig("../../config/views.yaml", "")
	if err != nil {
		t.Fatalf("load views config: %v", err)
	}
	for _, root := range sortedKeys(cfg.Pages) {
		for _, m := range cfg.Pages[root].Subgraph {
			hops := strings.Split(m.Path, ".")
			parent := root
			for _, hop := range hops[:len(hops)-1] {
				parent = str(fieldTargets, parent, hop)
			}
			field := hops[len(hops)-1]
			child := str(fieldTargets, parent, field)
			back := str(inverse, parent, field)
			if back == "" {
				t.Errorf("views: %s subgraph member %q — %s.%s has no @hasInverse partner, so nothing on the %s "+
					"points back at the %s it would be deleted with", root, m.Path, parent, field, child, parent)
				continue
			}
			nonNull := onTypeOrInterfaces(child, func(n string) bool { return nonNullFields[n+"."+back] })
			if !nonNull && !idFrom[child][back] {
				t.Errorf("views: %s subgraph member %q — a %s can outlive its %s: %s.%s is nullable and no "+
					"orbIdPattern of %s is built from it. A subgraph member is DELETED with the page's root; "+
					"a peer belongs outside the subgraph, where it still gets a tab",
					root, m.Path, child, parent, child, back, child)
			}
		}
	}
}

// typesFromSDL builds a minimal TypeInfo — field names, their targets and
// their nullability — without a running DGraph, so this stays in
// `make test-unit`.
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
				if path == "type" {
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

// schemaStyleViolations reports where an SDL breaks the schema's house style:
//   - every type, interface, enum and field carries a description with prose,
//     not only annotations — descriptions are what introspection and GraphiQL
//     show a consumer;
//   - no `#` comment outside the file's leading header — a `#` sits where a
//     description belongs and is invisible to every consumer;
//   - no line starting with a directive — the parsers here are line-based, so a
//     formatter wrapping `@hasInverse` onto its own line would silently drop
//     the pair.
func schemaStyleViolations(src string) []string {
	lines := strings.Split(src, "\n")
	defRe := regexp.MustCompile(`^(type|interface|enum)\s+(\w+)`)
	fieldRe := regexp.MustCompile(`^\s+(\w+)\s*:`)
	isAnnotation := func(t string) bool {
		return t == JSONStringAnnotation || strings.HasPrefix(t, OrbIDSuffixAnnotation) || strings.HasPrefix(t, OrbIDPatternAnnotation)
	}
	described := func(i int) bool {
		for _, l := range strings.Split(docstringAbove(lines, i), "\n") {
			if t := strings.TrimSpace(l); t != "" && !isAnnotation(t) {
				return true
			}
		}
		return false
	}

	var out []string
	header, inDoc, inEnum := true, false, false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") && header {
			continue
		}
		if t != "" {
			header = false
		}
		if t == `"""` {
			inDoc = !inDoc
			continue
		}
		if inDoc || (strings.HasPrefix(t, `"""`) && strings.HasSuffix(t, `"""`) && len(t) > 6) {
			continue
		}
		if strings.Contains(line, "#") {
			out = append(out, fmt.Sprintf("line %d: a # comment outside the header; write a description instead", i+1))
		}
		if strings.HasPrefix(t, "@") {
			out = append(out, fmt.Sprintf("line %d: a directive on its own line; keep it on its field's line", i+1))
		}
		if m := defRe.FindStringSubmatch(line); m != nil {
			inEnum = m[1] == "enum"
			if !described(i) {
				out = append(out, fmt.Sprintf("line %d: %s %s has no description", i+1, m[1], m[2]))
			}
			continue
		}
		if t == "}" {
			inEnum = false
			continue
		}
		if m := fieldRe.FindStringSubmatch(line); m != nil && !inEnum && !described(i) {
			out = append(out, fmt.Sprintf("line %d: field %s has no description", i+1, m[1]))
		}
	}
	return out
}

func TestSchemaStyle(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	t.Run("the shipped schema follows it", func(t *testing.T) {
		for _, v := range schemaStyleViolations(string(src)) {
			t.Error(v)
		}
	})

	for _, tc := range []struct{ name, sdl, want string }{
		{"a type without a description is refused", "type A {\n  \"\"\"\n  Described.\n  \"\"\"\n  f: String\n}\n", "type A has no description"},
		{"an annotation alone is not a description", "\"\"\"\norbIdPattern: {name}\n\"\"\"\ntype A {\n  \"\"\"\n  Described.\n  \"\"\"\n  f: String\n}\n", "type A has no description"},
		{"a field without a description is refused", "\"\"\"\nAn A.\n\"\"\"\ntype A {\n  f: String\n}\n", "field f has no description"},
		{"a # comment where a description belongs is refused", "\"\"\"\nAn A.\n\"\"\"\ntype A {\n  # Explained here instead.\n  \"\"\"\n  Described.\n  \"\"\"\n  f: String\n}\n", "a # comment outside the header"},
		{"a directive wrapped onto its own line is refused", "\"\"\"\nAn A.\n\"\"\"\ntype A {\n  \"\"\"\n  Described.\n  \"\"\"\n  f: [A]\n    @hasInverse(field: g)\n}\n", "a directive on its own line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(schemaStyleViolations(tc.sdl), "; ")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want a violation containing %q, got %q", tc.want, got)
			}
		})
	}
}
