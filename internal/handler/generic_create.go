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
// type's `orbIdPattern`, and the unit from the same editable subgraph members
// the editor already writes. Nothing here is per-type, which is the whole point — a type
// that earns a page earns a create form with no code.
//
// Returns nil when the type cannot be created, and the caller then renders no
// button. Two reasons it can be nil, and they are different:
//
//   - the view is an INTERFACE with no implementations, or with SEVERAL — one
//     implementation resolves silently, but nothing can pick between two;
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
	if v.Slug == "" {
		return nil
	}

	// An interface page creates an IMPLEMENTATION. DGraph generates no
	// add<Interface>, so the form has to name a concrete type; `pageView` keeps
	// the page's own identity for the button and the DOM, and `v` becomes the
	// type being created. Every use of `v` below already means exactly that, so
	// resolving it here and nowhere else keeps the rest of the function honest
	// for interface-backed and plain pages alike.
	pageView := v
	if v.IsInterface {
		impls := v.Implementations
		if len(impls) == 0 {
			// Nothing implements it, so there is no mutation to send.
			return nil
		}
		cv, ok := byType[impls[0]]
		if !ok {
			// The implementation has no view, so nothing knows how to render
			// or identify it.
			return nil
		}
		v = cv
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
		// Kind is the CONCRETE type — it builds the mutation name. Label and
		// DomID follow the PAGE, so /clusters reads "New Kubernetes Cluster"
		// and not the implementation's spelling.
		Kind:  v.Type,
		Label: configitems.HumanTypeName(pageView.Type),
		DomID: SafeDomID(pageView.Type),
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
	form.OrbIDTemplate = strings.ReplaceAll(v.OrbIDPattern[0].Raw, "{type}", v.OrbIDType)
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
	// On an INTERFACE page these are the SHARED fields only — the ones every
	// implementation has. An implementation's own are rendered per-type below,
	// so switching type keeps what was typed into the shared ones.
	ifaceShared := map[string]bool{}
	if pageView.IsInterface {
		for _, f := range pageView.Fields {
			ifaceShared[f] = true
		}
	}
	for _, f := range v.CreateFields {
		if f == "name" || v.NoCreate[f] {
			continue // already added above, or declared `create: false`
		}
		if pageView.IsInterface && !ifaceShared[f] {
			continue // this implementation's own — see form.Types
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
		opts, err := createOptions(ctx, dgraphURL, f.TypeName, "")
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
		// Nothing supplies a namespace, because nothing is REQUIRED above this
		// type — it is a root. So the form collects one instead of refusing.
		//
		// Keyed on "has no required parent", never on the type name: a second
		// root type gets this with no code change, and that is the whole reason
		// it is not a `v.Type == "DataCenter"` branch.
		form.NamespaceInput = true
	}

	// The TYPE picker. Rendered for an interface page ALWAYS, pre-selected when
	// there is one implementation.
	//
	// A question with one answer is noise; a field that already HOLDS the answer
	// tells the reader what they are creating at no cost — and the flow does not
	// change shape the day a second implementation lands. The alternative this
	// replaced returned an "unavailable" notice at two or more, which would have
	// removed the button exactly when the model grew.
	//
	// Only an implementation whose identity this form can build is offered: a
	// type the picker lists but cannot mint an id for would fail on submit.
	if pageView.IsInterface {
		for _, name := range pageView.Implementations {
			iv, ok := byType[name]
			if !ok || len(iv.CreateFields) == 0 || len(iv.OrbIDPattern) != 1 {
				continue
			}
			// Split the type's fields against the INTERFACE's. Shared ones are
			// rendered once and keep their values across a type change; only
			// the implementation's own swap. Re-rendering everything would
			// discard what the reader already typed into fields that did not
			// change.
			shared := map[string]bool{}
			for _, f := range pageView.Fields {
				shared[f] = true
			}
			ct := page.CreateType{
				Kind:          name,
				Label:         configitems.HumanTypeName(name),
				OrbIDTemplate: strings.ReplaceAll(iv.OrbIDPattern[0].Raw, "{type}", iv.OrbIDType),
			}
			ivLabel := labelFor(name)
			ivInfo := meta(name)
			for _, f := range iv.CreateFields {
				if shared[f] || f == "name" || iv.NoCreate[f] {
					continue
				}
				ct.Fields = append(ct.Fields, createField(ivInfo, iv, f, ivLabel, identity[f]))
			}
			form.Types = append(form.Types, ct)
		}
		if len(form.Types) == 0 {
			return nil
		}
	}

	// Repeating rows: the page's editable LIST members, created in the SAME
	// nested mutation as the single ones. A data centre and its racks in one
	// submit, because two mutations are two transactions and the first stays
	// committed when the second fails.
	for _, m := range pageView.EditorListMembers() {
		if strings.Contains(m.Field, ".") {
			continue // a list below another hop: the form holds no row to hang it on
		}
		cv, ok := byType[m.Type]
		if !ok || len(cv.CreateFields) == 0 {
			continue
		}
		tmpl := repeatingChildOrbIDTemplate(cv, m.ParentEdge)
		if tmpl == "" {
			continue // identity not derivable from this form; see the single-child rule
		}
		rep := page.CreateUnitRepeating{
			Field: m.Field, Label: labeller(m.Field), Kind: m.Type, OrbIDTemplate: tmpl,
		}
		// Which of the row's fields BUILD its identity, so the form can star
		// them and refuse an empty one — the same treatment the root's `name`
		// gets, and for the same reason: without it the row has no orbId and
		// the whole nested mutation fails.
		//
		// Read off the row's own template, where a bare `{x}` is the row's
		// field and `{parent.x}` is the parent's. The parent's are already
		// required on the root form; starring them again inside every row
		// would ask for the same value twice.
		rowIdentity := map[string]bool{}
		for _, seg := range strings.Split(tmpl, "{")[1:] {
			if body, _, ok := strings.Cut(seg, "}"); ok && !strings.HasPrefix(body, "parent.") {
				rowIdentity[body] = true
			}
		}

		childLabel := labelFor(m.Type)
		childInfo := meta(m.Type)
		for _, f := range cv.CreateFields {
			if cv.NoCreate[f] {
				continue
			}
			rep.Fields = append(rep.Fields, createField(childInfo, cv, f, childLabel, rowIdentity[f]))
		}
		if len(rep.Fields) > 0 {
			form.Repeating = append(form.Repeating, rep)
		}
	}

	// The unit: the page's editable members one hop down — the same set the
	// editor writes, created in the SAME nested mutation. A deeper member hangs
	// off an intermediate this form does not create.
	for _, m := range pageView.EditorMembers() {
		if strings.Contains(m.Field, ".") {
			continue
		}
		cv, ok := byType[m.Type]
		if !ok || len(cv.CreateFields) == 0 {
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
			Field: m.Field, Label: labeller(m.Field), Kind: m.Type,
			OrbIDTemplate: tmpl,
		}
		childLabel := labelFor(m.Type)
		childInfo := meta(m.Type)
		// Identity read off the CHILD's own template, same rule as repeating
		// rows: a bare `{x}` is this child's field and must be required.
		childIdentity := map[string]bool{}
		for _, seg := range strings.Split(tmpl, "{")[1:] {
			if body, _, ok := strings.Cut(seg, "}"); ok && !strings.HasPrefix(body, "parent.") {
				childIdentity[body] = true
			}
		}
		for _, f := range cv.CreateFields {
			if cv.NoCreate[f] {
				continue
			}
			child.Fields = append(child.Fields, createField(childInfo, cv, f, childLabel, childIdentity[f]))
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
// createOptions lists existing nodes choosable as the far end of a reference.
// `namespace` scopes it; a CREATE passes "" because the namespace is derived
// from the parent being chosen and is not known yet.
func createOptions(ctx context.Context, dgraphURL, typeName, namespace string) ([]page.CreateOption, error) {
	const cap = 500
	filter := ""
	if namespace != "" {
		filter = fmt.Sprintf("filter: { namespace: { eq: %q } }, ", namespace)
	}
	q := fmt.Sprintf("{ q: query%s(%sfirst: %d, order: { asc: orbId }) { orbId name } }", typeName, filter, cap)
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
// `{server.serviceTag}-{type}` — and on a create that owner is the node being
// made, whose serviceTag is on the same form. So the back-edge prefix is
// stripped and `{type}` resolved, leaving placeholders the form can fill.
//
// Returns "" when a placeholder survives that the parent cannot supply, because
// a child whose id cannot be derived must not be offered at all: `orbId` is
// `String! @id`, so a nested child without one fails the entire mutation.
func childOrbIDTemplate(child configitems.View, parentEdge string) string {
	// ONE rule for unit children and repeating rows alike — see
	// repeatingChildOrbIDTemplate, which this now delegates to.
	//
	// It used to strip the parent prefix and leave every placeholder bare,
	// which worked only because the children that existed referenced the
	// PARENT: `{server.serviceTag}-idrac` became `{serviceTag}-idrac` and the
	// form filled it from its own inputs. A child whose id comes from its OWN
	// field could not be expressed — IPAddress is `{address}`, a Server has no
	// `address`, so the placeholder resolved to nothing and the child was
	// SILENTLY not offered. Adding oobIP to a subgraph appeared to do nothing.
	return repeatingChildOrbIDTemplate(child, parentEdge)
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

// repeatingChildOrbIDTemplate is childOrbIDTemplate for a LIST member, where a
// placeholder's SCOPE has to survive into the form.
//
// For a single child the two scopes collapse harmlessly: `{server.serviceTag}`
// becomes `{serviceTag}` and the form fills it from the parent's inputs, which
// is correct because there is one child and the parent's value identifies it.
// For repeating rows that is wrong twice over — `{name}` on Rack means THAT
// ROW's name, and filling it from the parent would mint one id for every row,
// colliding with each other and with the parent.
//
// So parent-derived placeholders are marked and the rest are left bare:
//
//	{dataCenter.name}-rack  →  {parent.name}-rack   (the parent's name)
//	{name}                  →  {name}               (this row's own name)
//
// The form fills `{parent.*}` from its root inputs and everything else from the
// row, so the two can never be confused.
func repeatingChildOrbIDTemplate(child configitems.View, parentEdge string) string {
	if len(child.OrbIDPattern) != 1 || parentEdge == "" {
		return ""
	}
	out := strings.ReplaceAll(child.OrbIDPattern[0].Raw, "{type}", child.OrbIDType)
	out = strings.ReplaceAll(out, "{"+parentEdge+".", "{parent.")
	// Any dot left is a hop through something else entirely — a sibling, or a
	// grandparent — and the form holds neither.
	for _, seg := range strings.Split(out, "{") {
		body, _, found := strings.Cut(seg, "}")
		if found && strings.Contains(body, ".") && !strings.HasPrefix(body, "parent.") {
			return ""
		}
	}
	return out
}
