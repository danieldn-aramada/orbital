package configitems

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// R3 (Spike 33): the registry's ownership must match schema/schema.graphql.
// Parses the schema in the TEST ONLY — no runtime parser, no gqlparser
// dependency — to catch the one drift the compiler can't: a schema edge renamed
// (or a new ConfigItem type added) without updating the registry. Mirrors
// NetBox's build-time parent_object NotImplementedError guard.
func TestRegistryMatchesSchema(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	fields, implementsConfigItem := parseSchemaTypes(string(src))

	hasField := func(typeName, field string) bool {
		fs, ok := fields[typeName]
		return ok && fs[field]
	}

	// (a) Every registered type exists in the schema (as a type or interface).
	for _, ty := range Types {
		if _, ok := fields[ty.Name]; !ok {
			t.Errorf("registry type %q is not declared in schema.graphql", ty.Name)
		}
	}

	// (b) Every ConfigItem type in the schema has a registry entry.
	for name := range implementsConfigItem {
		if _, ok := FindByName(name); !ok {
			t.Errorf("schema type %q implements ConfigItem but has no registry entry (add it — see docs/playbooks/add-configitem.md)", name)
		}
	}

	// (c) Every declared ownership field is a real field on the right type.
	for _, ty := range Types {
		if ty.OwnerField != "" && !hasField(ty.Name, ty.OwnerField) {
			t.Errorf("%s.OwnerField %q is not a field on %s", ty.Name, ty.OwnerField, ty.Name)
		}
		if ty.ChildField != "" && ty.OwnerType != "" && !hasField(ty.OwnerType, ty.ChildField) {
			t.Errorf("%s.ChildField %q is not a field on owner %s", ty.Name, ty.ChildField, ty.OwnerType)
		}
		for _, e := range ty.OwnerEdges {
			if !hasField(ty.Name, e.Field) {
				t.Errorf("%s owner-edge Field %q is not a field on %s", ty.Name, e.Field, ty.Name)
			}
			if e.DownField != "" && !hasField(e.OwnerType, e.DownField) {
				t.Errorf("%s owner-edge DownField %q is not a field on owner %s", ty.Name, e.DownField, e.OwnerType)
			}
		}
	}

	// (d) Ownership must be acyclic — the presentation parent chain must
	// terminate (matches the graphdiff cycle guard's assumption for valid data).
	owners := map[string][]string{}
	for _, ty := range Types {
		for _, e := range ty.OwnerEdgesOf() {
			owners[ty.Name] = append(owners[ty.Name], e.OwnerType)
		}
	}
	state := map[string]int{} // 0 unseen, 1 on-path, 2 done
	var dfs func(string) bool
	dfs = func(n string) bool {
		state[n] = 1
		for _, o := range owners[n] {
			if state[o] == 1 {
				return false
			}
			if state[o] == 0 && !dfs(o) {
				return false
			}
		}
		state[n] = 2
		return true
	}
	for _, ty := range Types {
		if state[ty.Name] == 0 && !dfs(ty.Name) {
			t.Errorf("ownership cycle detected involving %q", ty.Name)
		}
	}
}

// TestBeforeFieldsCoverFormFields guards the one drift that is silent in both
// directions — no compiler error, no failing request, no visible UI change.
//
// The regression: someone adds an editable field to a type's FormFields (or to
// schema.graphql and then FormFields) and forgets to add it to BeforeFields.
// The edit still succeeds. The audit event is still written. But the field is
// absent from the `before` snapshot the proxy fetches, so computeChanges — which
// walks the intersection of before and after keys — skips it, and that field's
// edits never appear in `changes[]`, in the audit panel's diff, or in any client
// rendering attribution. Nothing anywhere reports a problem.
//
// The registry was clean when this was written; the point is to keep it that way,
// because there is no other signal that it stopped being true.
//
// Also checks the reverse (a BeforeFields token that is not a real schema field),
// which catches a typo that would silently drop the same field from the snapshot.
func TestBeforeFieldsCoverFormFields(t *testing.T) {
	src, err := os.ReadFile("../../schema/schema.graphql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	fields, _ := parseSchemaTypes(string(src))

	for _, ty := range Types {
		if ty.BeforeFields == "" {
			continue
		}
		before := topLevelSelection(ty.BeforeFields)

		// (a) Every editor-writable field must be in the before-snapshot.
		for _, f := range ty.FormFields {
			if !before[f] {
				t.Errorf("%s: FormField %q is missing from BeforeFields — edits to it will silently produce no audit diff (add it to BeforeFields in registry.go)",
					ty.Name, f)
			}
		}

		// (b) Every before-snapshot field must exist on the type. A typo here is
		// equally silent: DGraph rejects the selection, before-fetch logs a warning
		// and returns nil, and the whole diff disappears for that type.
		//
		// "Exists on the type" includes fields inherited from every interface it
		// implements — `id`/`orbId`/`name`/`version` are declared once on the
		// ConfigItem interface, not repeated in each type block.
		if _, ok := fields[ty.Name]; !ok {
			continue // TestRegistryMatchesSchema (a) already reports this
		}
		declared := map[string]bool{}
		for _, src := range append([]string{ty.Name, "ConfigItem"}, ty.Implements...) {
			for f := range fields[src] {
				declared[f] = true
			}
		}
		for f := range before {
			if !declared[f] {
				t.Errorf("%s: BeforeFields names %q, which is not a field on %s (or any interface it implements) in schema.graphql", ty.Name, f, ty.Name)
			}
		}
	}
}

// topLevelSelection returns the depth-0 field names of a GraphQL selection
// string, so nested sub-selections (`idracSettings { sshEnabled ... }`) contribute
// only their own name and not their children's — a child field name would
// otherwise appear to satisfy a parent-level FormField check.
func topLevelSelection(sel string) map[string]bool {
	out := map[string]bool{}
	depth := 0
	for _, tok := range strings.Fields(sel) {
		switch tok {
		case "{":
			depth++
		case "}":
			depth--
		default:
			// Handles "name{" and "}" fused to a token by sloppy spacing.
			for strings.HasSuffix(tok, "{") {
				tok = strings.TrimSuffix(tok, "{")
				depth++
			}
			if tok == "" {
				continue
			}
			if depth == 0 {
				out[tok] = true
			}
		}
	}
	return out
}

// parseSchemaTypes returns type/interface name -> set of field names, and the
// set of types whose declaration line contains "ConfigItem" (i.e. implement it).
// A deliberately small line scanner — not a GraphQL parser.
func parseSchemaTypes(src string) (map[string]map[string]bool, map[string]bool) {
	fields := map[string]map[string]bool{}
	implCI := map[string]bool{}
	blockRe := regexp.MustCompile(`^(?:type|interface)\s+(\w+)([^{]*)\{`)
	fieldRe := regexp.MustCompile(`^\s+(\w+)\s*:`)

	cur := ""
	for _, line := range strings.Split(src, "\n") {
		if m := blockRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
			fields[cur] = map[string]bool{}
			// m[2] is the text between the name and "{" — e.g. " implements
			// KubernetesCluster & ConfigItem ". ConfigItem here means the type
			// implements it (the ConfigItem interface's own line has no such text).
			if strings.Contains(m[2], "ConfigItem") {
				implCI[cur] = true
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
		}
	}
	return fields, implCI
}
