package approval

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// stampedFields are written by orbital, never by a client. `version` is the
// MVCC counter, `created*`/`updated*` are provenance, and `namespace`/`orbId`
// are identity — orbital derives namespace from the orbId prefix at merge.
// Accepting any of these from a changeset would let a proposal rewrite the
// very provenance the approval trail depends on.
var stampedFields = map[string]bool{
	"id":        true,
	"orbId":     true,
	"namespace": true,
	"version":   true,
	"createdAt": true,
	"createdBy": true,
	"updatedAt": true,
	"updatedBy": true,
}

// identityKeys are the only keys allowed inside an edge value. Anything else
// is an attempted deep write.
var identityKeys = map[string]bool{"orbId": true, "id": true}

// ValidationError is one problem with one change item. Index is the item's
// position in changes[] so a client can point at the offending row.
type ValidationError struct {
	Index int    `json:"index"`
	OrbID string `json:"orbId,omitempty"`
	Field string `json:"field,omitempty"`
	Msg   string `json:"message"`
	Hint  string `json:"hint,omitempty"`
}

func (e ValidationError) Error() string {
	loc := fmt.Sprintf("changes[%d]", e.Index)
	if e.OrbID != "" {
		loc += " (" + e.OrbID + ")"
	}
	if e.Field != "" {
		loc += " field " + e.Field
	}
	return loc + ": " + e.Msg
}

// ValidationResult is the outcome of validating a changeset.
//
// Validate also NORMALIZES the changeset it is handed: every item's Type is
// filled in from the graph when the caller omitted it, so what a caller
// persists always names its types (D12: type is optional on input, always on
// output). Merge builds `add<Type>`/`update<Type>` from that field.
type ValidationResult struct {
	Errors []ValidationError
	// Present is the subset of declared orbIds that exist in the graph today.
	// This becomes ApprovalRequest.base_present — what distinguishes a create
	// from a target deleted mid-review.
	Present []string
}

// Validate checks a changeset against the deployed schema and the current
// graph, at CREATION time.
//
// Validating here rather than at merge is the whole point: a change request
// naming a field that does not exist would otherwise sit in the queue looking
// legitimate, collect a human approval, and only fail when someone clicks
// merge — wasting the reviewer's attention on a proposal that could never
// apply. Everything checkable without a time machine is checked before the
// request is allowed to exist.
//
// It returns every problem it finds rather than the first, so a client fixes
// one round-trip's worth of mistakes at a time.
func Validate(ctx context.Context, src SchemaSource, cs *Changeset) (ValidationResult, error) {
	var res ValidationResult

	if strings.TrimSpace(cs.Namespace) == "" {
		res.Errors = append(res.Errors, ValidationError{Index: -1, Msg: "namespace is required"})
	}
	if len(cs.Changes) == 0 {
		res.Errors = append(res.Errors, ValidationError{Index: -1, Msg: "changes must not be empty"})
		return res, nil
	}

	// Pass 1 — everything decidable from the item alone. orbIds that survive
	// go to the graph.
	orbIDs := make([]string, 0, len(cs.Changes))
	seen := make(map[string]int, len(cs.Changes))
	for i, ch := range cs.Changes {
		ns, ok := namespaceOf(ch.OrbID)
		if !ok {
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg:  "orbId must be <namespace>:<key>",
				Hint: "e.g. alaska-dot:server-4FK8K44",
			})
			continue
		}
		// Single-namespace by construction (D2): the base snapshot, the
		// approval policy, and the reviewer's mental model are all
		// namespace-scoped, so a cross-namespace request would have no single
		// policy governing it.
		if cs.Namespace != "" && ns != cs.Namespace {
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg:  fmt.Sprintf("orbId is in namespace %q but the change request declares %q", ns, cs.Namespace),
				Hint: "a change request covers exactly one namespace; open a second one for " + ns,
			})
			continue
		}
		if prev, dup := seen[ch.OrbID]; dup {
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg: fmt.Sprintf("duplicate orbId — already changed at changes[%d]", prev),
				Hint: "merge applies items in order, so two items on one entity make the outcome depend on ordering; " +
					"and an entity has one version, so two ifVersion preconditions on it cannot both hold — the first item bumps it. Combine them",
			})
			continue
		}
		seen[ch.OrbID] = i

		switch ch.Op {
		case OpUpsert, OpUpdate:
			if len(ch.Set) == 0 && len(ch.Clear) == 0 {
				res.Errors = append(res.Errors, ValidationError{
					Index: i, OrbID: ch.OrbID,
					Msg: fmt.Sprintf("op %q requires set or clear", ch.Op),
				})
				continue
			}
		case OpDelete:
			if len(ch.Set) > 0 || len(ch.Clear) > 0 {
				res.Errors = append(res.Errors, ValidationError{
					Index: i, OrbID: ch.OrbID,
					Msg: "op \"delete\" must not carry set or clear",
				})
				continue
			}
		case "":
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg:  "op is required",
				Hint: "one of upsert, update, delete",
			})
			continue
		default:
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg:  fmt.Sprintf("unknown op %q", ch.Op),
				Hint: "one of upsert, update, delete",
			})
			continue
		}

		orbIDs = append(orbIDs, ch.OrbID)
	}

	if len(orbIDs) == 0 {
		return res, nil
	}

	// Edge TARGETS are resolved in the same round-trip as the declared orbIds,
	// because a reference to an orbId that does not exist is not a link — DGraph
	// reads it as a nested CREATE and fails the whole mutation with a message
	// about the target's required fields ("type IdracSettings requires a value
	// for field namespace"). Unresolvable from the changeset, and it surfaces at
	// merge, after a human already approved it.
	lookup := append([]string(nil), orbIDs...)
	lookup = append(lookup, edgeTargets(cs)...)

	resolvedRefs, err := src.ResolveEntities(ctx, dedupeSorted(lookup))
	if err != nil {
		return res, err
	}
	existing := make(map[string]EntityRef, len(orbIDs))
	for _, id := range orbIDs {
		if ref, ok := resolvedRefs[id]; ok {
			existing[id] = ref
			res.Present = append(res.Present, id)
		}
	}
	sort.Strings(res.Present)

	// Pass 2 — resolve each item's type against the graph. Types that survive
	// are introspected together in one round-trip.
	types := make([]string, 0, len(cs.Changes))
	resolved := make(map[int]string, len(cs.Changes))
	for i, ch := range cs.Changes {
		if seen[ch.OrbID] != i {
			continue // failed pass 1
		}
		ref, exists := existing[ch.OrbID]

		// A precondition that cannot be evaluated is refused, never ignored.
		// Silently dropping it would hand back a 201 to a caller who believes
		// their write is guarded — the exact failure mode the guard exists to
		// prevent. Reported here rather than as a conflict because nothing has
		// moved: the proposal is malformed, so it is a 400, not a 409.
		if !exists && ch.IfVersion != nil {
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg:  "ifVersion was supplied but no entity has this orbId, so there is no version to match",
				Hint: "Drop ifVersion if you mean to create it; fix the orbId if you meant an entity that already exists.",
			})
			continue
		}

		switch {
		case exists && ch.Type != "" && ch.Type != ref.Type:
			// A mismatch means the client is looking at something other than
			// what is actually there. Never silently prefer the graph — the
			// reviewer would approve a change to a different entity than the
			// proposal describes.
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg: fmt.Sprintf("type %q does not match the existing entity, which is a %s", ch.Type, ref.Type),
			})
			continue
		case exists:
			resolved[i] = ref.Type
			cs.Changes[i].Type = ref.Type
		case ch.Op == OpDelete:
			// Deleting an absent entity is an idempotent no-op; nothing to check.
			continue
		case ch.Op == OpUpdate:
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg:  "op \"update\" requires an existing entity, and none has this orbId",
				Hint: "use op \"upsert\" to create it",
			})
			continue
		case ch.Type == "":
			// Nothing to resolve from — a create must say what it is creating.
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg:  "type is required when the entity does not exist yet",
				Hint: "orbital resolves type from orbId for existing entities only",
			})
			continue
		default:
			resolved[i] = ch.Type
		}
		types = append(types, resolved[i])
	}

	if len(types) == 0 {
		return res, nil
	}

	schemas, err := src.TypeSchemas(ctx, types)
	if err != nil {
		return res, err
	}

	// Pass 3 — field-level checks against the deployed schema.
	//
	// createdEarlier grows as we walk, so an item may reference an entity a
	// PRECEDING item creates: merge applies items in changeset order, so that
	// link resolves by the time it is needed. A reference to an entity created
	// LATER would not, and is reported.
	createdEarlier := map[string]bool{}
	for i, ch := range cs.Changes {
		typeName, ok := resolved[i]
		if !ok {
			continue
		}
		ts, known := schemas[typeName]
		if !known {
			res.Errors = append(res.Errors, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg: fmt.Sprintf("type %q is not in the deployed schema", typeName),
			})
			continue
		}
		res.Errors = append(res.Errors, validateFields(i, ch, ts, existing, resolvedRefs, createdEarlier)...)
		if _, exists := existing[ch.OrbID]; !exists && ch.Op == OpUpsert {
			createdEarlier[ch.OrbID] = true
		}
	}

	if len(res.Errors) > 0 {
		sort.SliceStable(res.Errors, func(a, b int) bool { return res.Errors[a].Index < res.Errors[b].Index })
	}
	return res, nil
}

func validateFields(i int, ch ChangeItem, ts TypeSchema, existing, allRefs map[string]EntityRef, createdEarlier map[string]bool) []ValidationError {
	var errs []ValidationError

	for _, name := range sortedKeys(ch.Set) {
		fs, ok := ts.Fields[name]
		if !ok {
			errs = append(errs, ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg:  "no such field on " + ch.typeLabel(existing),
				Hint: "settable fields: " + strings.Join(settableNames(ts), ", "),
			})
			continue
		}
		if stampedFields[name] {
			errs = append(errs, ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg: "field is set by orbital and cannot be proposed",
			})
			continue
		}
		if err := checkValue(i, ch, name, fs, ch.Set[name], allRefs, createdEarlier); err != nil {
			errs = append(errs, *err)
		}
	}

	for _, name := range ch.Clear {
		fs, ok := ts.Fields[name]
		if !ok {
			errs = append(errs, ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg: "no such field on " + ch.typeLabel(existing),
			})
			continue
		}
		if stampedFields[name] {
			errs = append(errs, ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg: "field is set by orbital and cannot be cleared",
			})
			continue
		}
		if _, alsoSet := ch.Set[name]; alsoSet {
			errs = append(errs, ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg: "field appears in both set and clear",
			})
		}
		_ = fs
	}

	// A create must carry every non-null field the schema demands. Checking it
	// now means an impossible create cannot reach a reviewer.
	if _, exists := existing[ch.OrbID]; !exists && ch.Op == OpUpsert {
		var missing []string
		for _, req := range ts.RequiredOnCreate {
			if _, ok := ch.Set[req]; !ok {
				missing = append(missing, req)
			}
		}
		if len(missing) > 0 {
			errs = append(errs, ValidationError{
				Index: i, OrbID: ch.OrbID,
				Msg:  fmt.Sprintf("creating a %s requires %s", ch.resolvedType(existing), strings.Join(missing, ", ")),
				Hint: "reference an existing entity by orbId, e.g. \"dataCenter\": {\"orbId\": \"" + namespacePrefix(ch.OrbID) + ":your-dc\"}",
			})
		}
	}

	return errs
}

// checkValue enforces the one rule that DGraph will not enforce for us: an
// edge value may carry ONLY an identity key.
//
// This is the trap D11 was rewritten around. A mutation that nests a child's
// fields under an edge — `"idracSettings": {"firmwareVersion": "9.9.9"}` —
// returns success and writes nothing: DGraph LINKS on an edge, it does not
// deep-write. Measured directly; `firmwareVersion` stayed at its old value
// after a mutation that claimed to set it. Rejecting it here is the only place
// the mistake is visible, because every layer below reports success.
func checkValue(i int, ch ChangeItem, name string, fs FieldSchema, v any, allRefs map[string]EntityRef, createdEarlier map[string]bool) *ValidationError {
	if !fs.IsEdge {
		// A scalar field given an object or array. Most often a JSON-valued
		// String column (DataCenter.assetDataV2 is declared String) sent as
		// structure — DGraph rejects it with "cannot use as String".
		switch v.(type) {
		case map[string]any, []any:
			return &ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg:  fmt.Sprintf("expected a %s value, got a nested object", fs.TypeName),
				Hint: "fields declared String that hold JSON must be sent as a JSON-encoded string",
			}
		}
		return nil
	}

	values := []any{v}
	if fs.IsList {
		list, ok := v.([]any)
		if !ok {
			return &ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg: "expected a list of references",
			}
		}
		values = list
	}

	for _, item := range values {
		m, ok := item.(map[string]any)
		if !ok {
			return &ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg:  "edge value must be a reference object",
				Hint: fmt.Sprintf("%q: {\"orbId\": \"…\"}", name),
			}
		}
		if len(m) == 0 {
			return &ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg: "edge reference is empty",
			}
		}
		for _, k := range sortedKeys(m) {
			if identityKeys[k] {
				continue
			}
			return &ValidationError{
				Index: i, OrbID: ch.OrbID, Field: name,
				Msg:  fmt.Sprintf("edge references may only carry orbId — %q would be silently discarded", k),
				Hint: "DGraph links on an edge, it does not write through it; give that entity its own entry in changes[]",
			}
		}

		target, _ := m["orbId"].(string)
		if target == "" {
			continue // referenced by internal id — nothing to resolve
		}
		if _, ok := allRefs[target]; ok || createdEarlier[target] {
			continue
		}
		return &ValidationError{
			Index: i, OrbID: ch.OrbID, Field: name,
			Msg:  fmt.Sprintf("references %q, which does not exist", target),
			Hint: "an edge to an unknown orbId is read as a nested create and fails the whole mutation; create it in an earlier item, or fix the orbId",
		}
	}
	return nil
}

// edgeTargets returns every orbId referenced from an edge value anywhere in the
// changeset, so they resolve in the same round-trip as the declared targets.
func edgeTargets(cs *Changeset) []string {
	var out []string
	collect := func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if id, ok := m["orbId"].(string); ok && id != "" {
			out = append(out, id)
		}
	}
	for _, ch := range cs.Changes {
		for _, v := range ch.Set {
			switch t := v.(type) {
			case map[string]any:
				collect(t)
			case []any:
				for _, e := range t {
					collect(e)
				}
			}
		}
	}
	return out
}

// namespaceOf splits an orbId. Deliberately only asserts the <namespace>:<key>
// shape, NOT the <kind>-<natural-key> convention: DataCenter, Rack and
// IPAddress still carry legacy bare keys (seeded IdracSettings look like
// "2f-uae:2MLN3D4-idrac"), and rejecting those would refuse change requests
// against most of the existing graph.
func namespaceOf(orbID string) (string, bool) {
	ns, key, found := strings.Cut(orbID, ":")
	if !found || ns == "" || key == "" {
		return "", false
	}
	return ns, true
}

func namespacePrefix(orbID string) string {
	ns, _ := namespaceOf(orbID)
	return ns
}

func (c ChangeItem) resolvedType(existing map[string]EntityRef) string {
	if ref, ok := existing[c.OrbID]; ok {
		return ref.Type
	}
	return c.Type
}

func (c ChangeItem) typeLabel(existing map[string]EntityRef) string {
	if t := c.resolvedType(existing); t != "" {
		return t
	}
	return "this type"
}

func settableNames(ts TypeSchema) []string {
	out := make([]string, 0, len(ts.Fields))
	for n := range ts.Fields {
		if !stampedFields[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
