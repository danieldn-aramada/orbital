//go:build integration

package handler

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/armada/orbital/internal/testutil"

	"github.com/armada/orbital/internal/configitems"
)

// Three hand-maintained lists must agree for a proposed-change mark to work:
//
//  1. the `tr[data-field]` slots in a `data-field-orbid` table (where a mark can appear),
//  2. the type's FormFields in internal/configitems (what the editor can write),
//  3. the `*ValuesJSON` map in the page handler (the CURRENT value a proposal is compared against).
//
// A slot with no field behind it is dead UI — the mark can never fire. A field
// with no slot means a real proposal annotates nothing. A field missing from the
// values map has no current value, so orbital.js (renderFieldMark) cannot tell
// "proposed value already matches" from "proposed value differs".
//
// All three failures are silent. That is what earns these guards.
//
// Both tests DISCOVER their subjects rather than hardcoding one file, so a mark
// table or values map added for a new type is covered the moment it exists —
// and fails loudly until it is declared in the maps below. The previous
// Server-only versions would have let a DataCenter mark table ship unguarded,
// and never looked at IdracValuesJSON or MaintenanceValuesJSON at all.

// markSlotOwners maps each discovered mark table to the registry type whose
// FormFields it must match. Key is "<path under web/templates>:<orbId expr>",
// because the same expression (e.g. {{.OrbID}}) means a different type in a
// different template.
var markSlotOwners = map[string]string{
	// Empty: every hand-written mark table has been deleted with its page.
	// New entries belong here only for a table whose slots are WRITTEN OUT per
	// field — see derivedMarkTables for why the generic ones are not.
}

// derivedMarkTables are mark tables whose slots are generated from the same
// derived field set the guard would compare them against.
//
// This guard exists because a hand-written template can DRIFT from the field
// list: someone adds an editable field and forgets the slot, or leaves a slot
// behind when a field goes. The generic detail template emits a slot from
// `$editable`, which IS the derived editable set, so the two cannot disagree —
// there is no second list to fall out of step with. Comparing them would assert
// that a value equals itself.
//
// ⚠️ Only exempt a table that genuinely derives its slots. A table listing
// fields literally belongs in markSlotOwners, however tempting it is to quiet
// this test.
var derivedMarkTables = map[string]bool{
	"shared/pages/generic-detail.gohtml:{{.OrbID}}":   true,
	"shared/pages/generic-detail.gohtml:{{.OrbID}}\n": true,
}

// valuesJSONOwners maps each discovered `<name>: rawFieldValues(...)` site to
// the registry type whose FormFields its keys must match.
var valuesJSONOwners = map[string]string{
	// Empty for the same reason as markSlotOwners: rawFieldValues went with
	// server.go. The generic renderer builds its values map from
	// editableSubtree(view, entity), i.e. straight from the derived field set,
	// so there is no hand-written key list to drift.
}

// TestFieldMarkSlotsMatchRegistry_AllTypes pins list 1 against list 2, for
// every mark table in every template.
//
// This invariant used to be a hardcoded `expect(slots).toBe(6)` at the end of
// e2e/proposed-change-marks.spec.ts. That assertion broke the moment two
// legitimate fields were added — `serialNumber` (2026-09-22) and `uHeight` —
// even though registry and template had been updated together correctly. A
// magic number cannot tell "someone added a field properly" from "someone added
// a slot with no field behind it", so it reported a false failure and told
// nobody anything. Comparing the sets directly distinguishes them and needs no
// browser.
func TestFieldMarkSlotsMatchRegistry_AllTypes(t *testing.T) {
	t.Chdir("../..")

	found := discoverMarkTables(t)
	if len(found) == 0 {
		t.Fatal("no data-field-orbid tables found under web/templates — did the mark feature move or change shape?")
	}

	for key, block := range found {
		if derivedMarkTables[key] {
			continue
		}
		typeName, declared := markSlotOwners[key]
		if !declared {
			t.Errorf("undeclared mark table %q — a proposed-change mark table exists with no guard.\n"+
				"  Add it to markSlotOwners with the registry type its slots must match.", key)
			continue
		}
		t.Run(key, func(t *testing.T) {
			got := dataFieldNames(block)
			want := editableFieldsFor(t, typeName)
			if !equalSets(got, want) {
				t.Errorf(
					"%s: mark slots do not match FormFields in configitems/registry.go\n"+
						"  template slots: %v\n"+
						"  registry fields: %v\n"+
						"  only in template (dead slot — a mark here can never fire): %v\n"+
						"  only in registry (editable field with nowhere to show a proposal): %v",
					typeName, got, want, difference(got, want), difference(want, got),
				)
			}
		})
	}

	for key := range markSlotOwners {
		if _, ok := found[key]; !ok {
			t.Errorf("declared mark table %q no longer exists — coverage was lost silently.\n"+
				"  Remove it from markSlotOwners if the table was deliberately deleted.", key)
		}
	}
}

// TestFieldMarkValuesMatchRegistry_AllTypes closes the THIRD list, for every
// values map in every handler.
//
// That list was live-broken at HEAD on 2026-09-23: `serialNumber` was absent
// from SummaryValuesJSON — directly beneath a comment reading "Exactly the
// Server FormFields in configitems/registry.go". The comment asserted the
// property; nothing checked it. Consequence: a redundant proposal on
// serialNumber rendered a mark it should not, and a proposal clearing it
// rendered none at all. The sibling maps for IdracSettings and ServerMaintenance
// carry the identical obligation and had never been checked.
func TestFieldMarkValuesMatchRegistry_AllTypes(t *testing.T) {
	t.Chdir("../..")

	found := discoverValuesMaps(t)
	// Zero is now the CORRECT answer: every hand-written values map was deleted
	// with its page. This used to t.Fatal on zero, to catch the sites moving or
	// changing shape without anyone noticing — a real risk while they existed.
	if len(found) == 0 {
		return
	}

	for name, keys := range found {
		typeName, declared := valuesJSONOwners[name]
		if !declared {
			t.Errorf("undeclared values map %q — it supplies current values for marks with no guard.\n"+
				"  Add it to valuesJSONOwners with the registry type its keys must match.", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			want := editableFieldsFor(t, typeName)
			if !equalSets(keys, want) {
				t.Errorf(
					"%s does not match %s FormFields in configitems/registry.go\n"+
						"  map keys:        %v\n"+
						"  registry fields: %v\n"+
						"  only in map (a value for a field nothing can propose): %v\n"+
						"  only in registry (NO current value, so the mark cannot tell\n"+
						"    'already matches' from 'differs'): %v",
					name, typeName, keys, want, difference(keys, want), difference(want, keys),
				)
			}
		})
	}

	for name := range valuesJSONOwners {
		if _, ok := found[name]; !ok {
			t.Errorf("declared values map %q no longer exists — coverage was lost silently.\n"+
				"  Remove it from valuesJSONOwners if it was deliberately deleted.", name)
		}
	}
}

var (
	fieldTableRe = regexp.MustCompile(`data-field-orbid="(\{\{\.[A-Za-z]+\}\})"`)
	dataFieldRe  = regexp.MustCompile(`<tr data-field="([A-Za-z0-9_]+)"`)
)

// discoverMarkTables walks every template and returns one entry per
// data-field-orbid table, keyed "<path under web/templates>:<orbId expr>".
func discoverMarkTables(t *testing.T) map[string]string {
	t.Helper()
	const root = "web/templates"
	out := map[string]string{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".gohtml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		src := string(raw)
		for _, loc := range fieldTableRe.FindAllStringSubmatchIndex(src, -1) {
			expr := src[loc[2]:loc[3]]
			rest := src[loc[1]:]
			end := strings.Index(rest, "</table>")
			if end < 0 {
				t.Errorf("%s: data-field-orbid=%q table has no closing </table>", rel, expr)
				continue
			}
			out[rel+":"+expr] = rest[:end]
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// discoverValuesMaps parses every non-test file in internal/handler and returns
// the literal keys of each `<name>: rawFieldValues(map[string]any{...})` site.
// Both forms are found: a struct-literal field (SummaryValuesJSON) and an
// assignment to a selector (srv.IdracValuesJSON).
func discoverValuesMaps(t *testing.T) map[string][]string {
	t.Helper()
	const dir = "internal/handler"
	out := map[string][]string{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr: // Field: rawFieldValues(...) inside a struct literal
				if id, ok := x.Key.(*ast.Ident); ok {
					if keys, ok := rawFieldValueKeys(x.Value); ok {
						out[id.Name] = keys
					}
				}
			case *ast.AssignStmt: // x.Field = rawFieldValues(...)
				if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
					return true
				}
				name := ""
				switch lhs := x.Lhs[0].(type) {
				case *ast.SelectorExpr:
					name = lhs.Sel.Name
				case *ast.Ident:
					name = lhs.Name
				}
				if name == "" {
					return true
				}
				if keys, ok := rawFieldValueKeys(x.Rhs[0]); ok {
					out[name] = keys
				}
			}
			return true
		})
	}
	return out
}

// rawFieldValueKeys returns the sorted literal keys of the map passed to a
// rawFieldValues(...) call, and whether the expression was such a call.
func rawFieldValueKeys(e ast.Expr) ([]string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != "rawFieldValues" || len(call.Args) == 0 {
		return nil, false
	}
	lit, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	var keys []string
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if bl, ok := kv.Key.(*ast.BasicLit); ok {
				keys = append(keys, strings.Trim(bl.Value, `"`))
			}
		}
	}
	sort.Strings(keys)
	return keys, true
}

func dataFieldNames(block string) []string {
	var out []string
	for _, m := range dataFieldRe.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// editableFieldsFor asks the DEPLOYED schema what a type's editable fields are.
//
// This used to read the hand-maintained FormFields in registry.go. Those are
// gone: the editor's field list now comes from introspection, so the question
// "does this template have a slot for every editable field" can only be
// answered against the running schema. That is why these guards moved from unit
// to integration — the baseline they compare against stopped being a constant.
func editableFieldsFor(t *testing.T, name string) []string {
	t.Helper()
	fields, err := sharedTestResolver(t).Fields(context.Background(), name)
	if err != nil {
		t.Fatalf("resolve fields for %q: %v", name, err)
	}
	out := append([]string(nil), fields...)
	sort.Strings(out)
	return out
}

var testResolverOnce struct {
	sync.Once
	r *configitems.Resolver
}

func sharedTestResolver(t *testing.T) *configitems.Resolver {
	t.Helper()
	testResolverOnce.Do(func() {
		url := testutil.DGraphURL()
		testResolverOnce.r = configitems.NewResolver(
			configitems.NewDGraphSchemaClient(url, adminURLFor(url)), time.Minute)
	})
	return testResolverOnce.r
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func difference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}
