package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/web/data/page"
)

// buildCreateForm assembles the New-node form for a list page.
//
// Every part is derived: the scalars from the view's editable set, the required
// relationships from the SCHEMA's non-null edges, the identity inputs from the
// type's `orbIdPattern`, and the unit from the same EditorMembers the editor
// already writes. Nothing here is per-type, which is the whole point — a type
// that earns a page earns a create form with no code.
//
// Returns nil when the type cannot be created, and the caller then renders no
// button. Two reasons it can be nil, and they are different:
//
//   - the view is an INTERFACE — DGraph generates no add<Interface>, so there is
//     no single mutation to send until something picks an implementation;
//   - the type's pattern is `external` or needs a path the form cannot collect,
//     which means orbital does not mint this id and must not pretend to.
func buildCreateForm(
	ctx context.Context,
	dgraphURL string,
	v configitems.View,
	byType map[string]configitems.View,
	labelFor func(typeName string) func(string) string,
	meta func(typeName string) configitems.TypeInfo,
) *page.CreateForm {
	if v.IsInterface || v.Slug == "" {
		return nil
	}
	info := meta(v.Type)
	if len(info.Fields) == 0 {
		return nil
	}
	// EXACTLY ONE pattern. One type, one orbId shape — a dev declares how the id
	// is built and changing it later is a breaking change, not a correction.
	//
	// More than one means the type's identity depends on WHICH owner it has
	// (NetworkInterface: a server's serviceTag or a switch's serial), and a form
	// cannot know which you meant without asking. That is an unresolved
	// modelling question — the fix is splitting the type by owner, which
	// `config/views.yaml` already records as deferred — so the page offers no
	// create button rather than guessing and minting an id under the wrong one.
	//
	// It also keeps the resolution rule in ONE place: Go's ConstructOrbID picks
	// the first pattern whose placeholders all resolve, and reimplementing that
	// in JS is how the two would drift.
	if len(v.OrbIDPattern) != 1 {
		return nil
	}

	labeller := labelFor(v.Type)
	form := &page.CreateForm{
		Kind:  v.Type,
		Label: configitems.HumanFieldLabel(v.Type),
		DomID: SafeDomID(v.Type),
	}

	// The identity inputs. A path with a dot crosses an edge — `{server.serviceTag}`
	// on a child — which for a ROOT create cannot be collected, because the thing
	// on the other end is the node being created.
	identity := map[string]bool{}
	for _, p := range v.OrbIDPattern {
		for _, path := range p.Paths() {
			if strings.Contains(path, ".") {
				continue
			}
			identity[path] = true
			form.OrbIDPaths = append(form.OrbIDPaths, path)
		}
	}
	form.OrbIDTemplate = strings.ReplaceAll(v.OrbIDPattern[0].Raw, "{kind}", v.OrbIDKind)
	if len(form.OrbIDPaths) == 0 {
		// Every pattern crosses an edge or is `external`: this type's id is
		// minted elsewhere, so offering a create form would invent one.
		return nil
	}
	sort.Strings(form.OrbIDPaths)

	// `name` ALWAYS, first, and required. It lives on the ConfigItem interface,
	// so the editable set excludes it for every type that has not re-admitted it
	// (`fields.name.editable: true` — only DataCenter and Rack do). That is
	// right for EDITING and wrong for creating: name is the display label on
	// every list, tab and picker, so a node created without one renders blank
	// everywhere and the reader has no way to tell which row is theirs.
	hasName := false
	for _, f := range info.Fields {
		if f.Name == "name" {
			hasName = true
			break
		}
	}
	if hasName {
		form.Fields = append(form.Fields, createField(info, v, "name", labeller, true))
	}
	for _, f := range v.Fields {
		if f == "name" || v.NoCreate[f] {
			continue // already added above, or declared `create: false`
		}
		form.Fields = append(form.Fields, createField(info, v, f, labeller, identity[f]))
	}

	// Required relationships: the same non-null analysis that derives
	// ownership. A non-null edge on the type being created is a thing the
	// create MUST supply, because the node cannot exist without it.
	for _, f := range info.Fields {
		if f.Kind == "SCALAR" || f.Kind == "ENUM" || f.IsList || !f.NonNull {
			continue
		}
		// A view exists for every ConfigItem type, so this is the test for
		// "points at something orbital models" without a second lookup table.
		if _, isConfigItem := byType[f.TypeName]; !isConfigItem {
			continue
		}
		opts, err := createOptions(ctx, dgraphURL, f.TypeName)
		if err != nil {
			form.Unavailable = "Orbital could not load the " + labeller(f.Name) +
				" options, so it cannot offer a create form: " + err.Error()
			return form
		}
		form.Relations = append(form.Relations, page.CreateRelation{
			Field: f.Name, Label: labeller(f.Name), Type: f.TypeName,
			Required: true, Options: opts,
		})
		// Namespace comes from the FIRST required relationship that has one.
		// Never typed: it is the prefix of the parent's orbId, and a second
		// input for the same value is a second chance to disagree with it.
		if form.NamespaceFrom == "" {
			form.NamespaceFrom = f.Name
		}
	}
	if form.NamespaceFrom == "" {
		// Nothing supplies a namespace. Creating one is its own question —
		// a new DataCenter means a new namespace — and is out of scope.
		return nil
	}

	// The unit: contained, page-declared, single-cardinality — the same set
	// EditorMembers returns, created in the SAME nested mutation.
	for _, m := range v.EditorMembers() {
		cv, ok := byType[m.ChildType]
		if !ok || len(cv.Fields) == 0 {
			continue
		}
		tmpl := childOrbIDTemplate(cv, m.ParentEdge)
		if tmpl == "" {
			// The child's id cannot be built from this form — it depends on
			// something the parent create does not collect. Offering the inputs
			// would produce a mutation DGraph refuses (`orbId` is `String! @id`).
			continue
		}
		child := page.CreateUnitChild{
			Field: m.ChildField, Label: labeller(m.ChildField), Kind: m.ChildType,
			OrbIDTemplate: tmpl,
		}
		childLabel := labelFor(m.ChildType)
		childInfo := meta(m.ChildType)
		for _, f := range cv.Fields {
			if cv.NoCreate[f] {
				continue
			}
			child.Fields = append(child.Fields, createField(childInfo, cv, f, childLabel, false))
		}
		form.Unit = append(form.Unit, child)
	}
	return form
}

// createOptions fetches the existing nodes a required relationship may point at.
//
// Server-side, because a picker that yields an orbId is the entire job and an
// integrator drawing their own form needs the same list. Capped: a picker with
// ten thousand entries is not a picker, and the cap is stated in the UI rather
// than silently truncating.
func createOptions(ctx context.Context, dgraphURL, typeName string) ([]page.CreateOption, error) {
	const cap = 500
	q := fmt.Sprintf("{ q: query%s(first: %d, order: { asc: orbId }) { orbId name } }", typeName, cap)
	raw, err := runGraphQL(ctx, dgraphURL, q, "q")
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]page.CreateOption, 0, len(rows))
	for _, m := range rows {
		orbID, _ := m["orbId"].(string)
		name, _ := m["name"].(string)
		if orbID == "" {
			continue
		}
		if name == "" {
			name = orbID
		}
		out = append(out, page.CreateOption{OrbID: orbID, Name: name})
	}
	return out, nil
}

// childOrbIDTemplate rewrites a contained child's orbIdPattern against its
// PARENT's form inputs.
//
// A child's identity is declared in terms of its owner — IdracSettings is
// `{server.serviceTag}-{kind}` — and on a create that owner is the node being
// made, whose serviceTag is on the same form. So the back-edge prefix is
// stripped and `{kind}` resolved, leaving placeholders the form can fill.
//
// Returns "" when a placeholder survives that the parent cannot supply, because
// a child whose id cannot be derived must not be offered at all: `orbId` is
// `String! @id`, so a nested child without one fails the entire mutation.
func childOrbIDTemplate(child configitems.View, parentEdge string) string {
	// Same rule as the root: exactly one pattern, or the child's identity is
	// ambiguous and it must not be offered on this form.
	if len(child.OrbIDPattern) != 1 || parentEdge == "" {
		return ""
	}
	out := strings.ReplaceAll(child.OrbIDPattern[0].Raw, "{kind}", child.OrbIDKind)
	out = strings.ReplaceAll(out, "{"+parentEdge+".", "{")
	if strings.Contains(out, ".") {
		return "" // a placeholder still crosses an edge the form cannot reach
	}
	return out
}

// createField resolves one input: its scalar type and its default.
//
// The type decides the control AND the JSON the mutation sends — a Boolean
// field given the string "false" is refused by DGraph, and a text box produces
// exactly that.
//
// A default on an IDENTITY field is dropped: pre-filling the thing the orbId is
// built from would create a node under a key nobody chose, and a wrong orbId is
// permanent. Dropped silently here because Validate reports it; this is the
// belt to that braces.
func createField(info configitems.TypeInfo, v configitems.View, field string, label func(string) string, identity bool) page.CreateField {
	out := page.CreateField{Field: field, Label: label(field), Identity: identity}
	for _, f := range info.Fields {
		if f.Name == field {
			out.Type = f.TypeName
			break
		}
	}
	if !identity {
		out.Default = v.Defaults[field]
	}
	out.Hint = scalarHint(out.Type)
	return out
}

// scalarHint is what a field of this scalar type expects, as placeholder text.
//
// A form that does not say is a form that gets guessed at: a DateTime box with
// no hint invites "today", "14:30" and "06/10/2026", two of which fail and one
// of which fails differently per locale.
func scalarHint(scalar string) string {
	switch scalar {
	case "DateTime":
		return "2026-10-06T14:30:00Z"
	case "Int", "Int64":
		return "whole number"
	case "Float":
		return "number"
	default:
		return ""
	}
}
