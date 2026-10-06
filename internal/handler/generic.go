package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/armada/orbital/internal/configitems"
)

// The generic renderer: one list page and one detail page that serve EVERY
// ConfigItem type.
//
// The goal is that defining a type in DGraph yields `/{slug}` and
// `/{slug}/{id}` with no code, no template and no configuration — configuration
// is the exception, not the mechanism. Everything below is therefore derived
// from the deployed schema via the view list.

// genericList builds the list query for a type: its own scalars plus the
// identity fields every ConfigItem carries.
func genericListQuery(v configitems.View, viewOf func(string) configitems.View, display func(string) []string, limit int) string {
	// Display, not Fields: the page shows every scalar, and only EDITING is
	// gated by editorIgnored. Selecting the editable set left types whose
	// fields are all annotated — StorageDevice, NetworkAdapter — rendering
	// nothing but a name.
	sel := "orbId name"
	if v.IsInterface {
		// A sub-interface does not declare orbId or name — those live on
		// ConfigItem — so selecting them bare is rejected at validation.
		sel = "__typename ... on ConfigItem { orbId name }"
	}
	for _, f := range v.Display {
		if f == "name" {
			continue // already selected
		}
		sel += " " + f
	}
	// Reference columns: enough of the target to render a link.
	for _, rc := range v.RefColumns {
		sel += " " + rc.Field + " { " + idNameSel(viewOf(rc.Type)) + " }"
	}
	sel += columnSelection(v)
	// An interface view lists several concrete types, so each implementation
	// contributes its own columns through a fragment. A row whose type lacks a
	// column simply has no value for it and renders as "—" — which is the
	// honest answer, not an omission.
	for _, impl := range v.Implementations {
		own := []string{}
		for _, f := range display(impl) {
			if !containsString(v.Display, f) && f != "name" {
				own = append(own, f)
			}
		}
		if len(own) == 0 {
			continue
		}
		sel += " ... on " + impl + " { " + strings.Join(own, " ") + " }"
	}
	// The total rides along in the SAME request. The page is capped, and a
	// capped table that does not say so reads as the whole set — which is a
	// wrong answer, not a slow one. DGraph generates aggregate<T> for
	// interfaces as well as concrete types, so this needs no special case.
	return fmt.Sprintf("{ query%s(first: %d) { %s } aggregate%s { count } }", v.Type, limit, sel, v.Type)
}

// columnSelection folds the view's computed column paths into the SAME
// selection the rows already use — `kubernetesNode { cluster { ... } role }`.
//
// No extra request and no extra round trip: a path is just more of the tree the
// query was already fetching.
//
// Paths that share a prefix are NOT merged here, and do not need to be: GraphQL
// merges duplicate field selections by definition, and DGraph honours it
// (verified against v25.3.1 — `server { hostname } server { serviceTag }`
// returns one `server` with both). The selection fragment for each path is
// built in configitems.resolveColumns, which is the only place that knows
// whether a hop lands on an interface and therefore needs a type condition.
func columnSelection(v configitems.View) string {
	out := ""
	for _, c := range v.Columns {
		out += " " + c.Selection
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// genericDetailQuery selects the entity's own fields plus, for every
// relationship, just enough to render a link (orbId and name). Deliberately
// shallow: a detail page shows what an entity relates to, not the whole graph
// beneath it, and a deep selection on a cyclic schema does not terminate.
func genericDetailQuery(v configitems.View, viewOf func(string) configitems.View, display func(string) []string, refColumns func(string) []configitems.ViewRefColumn, ownedIDSel func(string) string, orbID string) string {
	// The interface fields are selected alongside the type's own: `version`
	// because the editor must send it or a concurrent edit overwrites silently
	// instead of being refused, and the rest because the metadata box shows
	// them. Neither set is in Display — displayScalars strips the interface out.
	sel := "orbId name"
	seen := map[string]bool{"orbId": true, "name": true}
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			sel += " " + f
		}
	}
	add("version")
	for _, f := range v.Meta {
		add(f)
	}
	for _, f := range v.Display {
		add(f)
	}

	// SUMMARY REF ROWS. They are no longer tabs, so they are no longer selected
	// by the tab walk below — and a ref that is not fetched renders as nothing
	// at all, silently, which is how the Data Center row vanished off every
	// Server page the moment the two surfaces were split.
	for _, rc := range v.SummaryRefs {
		if seen[rc.Field] {
			continue
		}
		seen[rc.Field] = true
		sel += " " + rc.Field + " { " + idNameSel(viewOf(rc.Type)) + " }"
	}

	// Each relationship is selected EXACTLY once, with the union of what the
	// page needs from it. Contained children are edited inline, so they carry
	// their own fields; everything else only needs enough to render a link.
	owned := ownedChildFields(v.Type, viewOf, display, refColumns, ownedIDSel)
	for _, tab := range v.Tabs {
		// A PATH tab selects nothing of its own: its rows are read out of the
		// owned-child subtree this query already fetches for the editor. Its
		// field name contains a dot, which is not a legal selection name — an
		// earlier version emitted it verbatim and DGraph refused the whole
		// query with "Expected Name, found <Invalid>", taking out the page.
		if strings.Contains(tab.Field, ".") {
			continue
		}
		seen[tab.Field] = true
		if childSel, isOwned := owned[tab.Field]; isOwned {
			// An OWNED child is rendered as a row too, so it needs its computed
			// columns as much as a non-owned one. Missing this is why a data
			// centre's Racks tab had a Servers header over empty cells: Rack is
			// owned by DataCenter, so it took this branch and never selected
			// the aggregate.
			sel += " " + tab.Field + " { " + childSel + columnSelection(viewOf(tab.Type)) + " }"
			continue
		}
		// A related entity is rendered as a ROW, so fetch the columns that row
		// shows — the target type's display fields. Selecting only orbId+name
		// made every relationship tab a bare list of names, where the
		// hand-written pages show the child's own data (a DataCenter's servers
		// table carries model, service tag, OOB IP...).
		childSel := idNameSel(viewOf(tab.Type))
		for _, f := range display(tab.Type) {
			if f != "name" {
				childSel += " " + f
			}
		}
		for _, rc := range refColumns(tab.Type) {
			childSel += " " + rc.Field + " { " + idNameSel(viewOf(rc.Type)) + " }"
		}
		// ...and its computed columns, so a Server inside a network device's
		// tab shows the same cluster and role it shows on /servers.
		childSel += columnSelection(viewOf(tab.Type))
		sel += " " + tab.Field + " { " + childSel + " }"
	}

	// OWNED CHILDREN THE PAGE DOES NOT TAB — ids only, nothing renders them.
	//
	// The audit roll-up covers everything the root OWNS and reads it out of
	// this result (ownedSubtreeOrbIDs), so an owned child nobody listed under
	// `tabs:` must still appear or the roll-up silently narrows to whatever the
	// page happens to show. That coupling used to be asserted in a comment here
	// and enforced by nothing.
	for _, oc := range v.Dependents {
		if seen[oc.ChildField] {
			continue
		}
		seen[oc.ChildField] = true
		childSel := "orbId"
		if nested := ownedIDSel(oc.ChildType); nested != "" {
			childSel += " " + nested
		}
		sel += " " + oc.ChildField + " { " + childSel + " }"
	}
	return fmt.Sprintf("{ get%s(orbId: %q) { %s } }", v.Type, orbID, sel)
}

// ownedChildFields returns, per owned child field, the selection to fetch for
// it — enough to render AND edit it inline.
//
// Recurses one level through wrappers. A wrapper is a type with owned children
// and no scalars of its own (ClusterBackup wraps etcd/velero/s3Sync), and
// BuildEditTargets emits its GRANDchildren as the edit targets, so the tree has
// to nest the same way or those targets have no data behind them.
func ownedChildFields(rootType string, viewOf func(string) configitems.View, display func(string) []string, refColumns func(string) []configitems.ViewRefColumn, ownedIDSel func(string) string) map[string]string {
	out := map[string]string{}
	for _, oc := range viewOf(rootType).Dependents {
		// idNameSel, not a bare `orbId name`: an editable member may be typed by
		// an INTERFACE (DataCenter.kubernetesClusters), and `orbId` is declared
		// on ConfigItem rather than on a sub-interface, so selecting it bare is
		// rejected at validation — which fails the WHOLE query and takes the page
		// with it. The non-owned branch below always used idNameSel; this one did
		// not, and no member had been both editable and interface-typed before.
		sel := idNameVersionSel(viewOf(oc.ChildType))
		for _, f := range display(oc.ChildType) {
			if f != "name" {
				sel += " " + f
			}
		}
		// Reference columns too. An owned child that is a LIST still renders as
		// a TABLE, with the same columns any other relationship table has — and
		// the table's headers come from the same RefColumns this selection
		// feeds. Omitting them here printed a header row of "cluster / server /
		// ipv4" above a column of dashes, because the relationship is selected
		// exactly once and this was that once.
		for _, rc := range refColumns(oc.ChildType) {
			sel += " " + rc.Field + " { " + idNameSel(viewOf(rc.Type)) + " }"
		}
		for _, gc := range viewOf(oc.ChildType).Dependents {
			gsel := idNameVersionSel(viewOf(gc.ChildType))
			for _, f := range display(gc.ChildType) {
				if f != "name" {
					gsel += " " + f
				}
			}
			// Reference columns on a GRANDCHILD too. An `include:` path renders
			// grandchildren as a table, and the column naming their parent is
			// one of these — without it the Controller column is blank on every
			// row. Same omission as at the child level, one layer down.
			for _, rc := range refColumns(gc.ChildType) {
				gsel += " " + rc.Field + " { " + idNameSel(viewOf(rc.Type)) + " }"
			}
			// ...and the ids of anything the GRANDCHILD in turn owns. The page
			// renders nothing below this depth, but the audit roll-up covers
			// the whole owned subtree and reads it out of this result — so a
			// three-deep ownership chain would otherwise roll up on the change
			// request's scope pin and not on the audit tab, which is the exact
			// disagreement these two were just unified to end.
			if deeper := ownedIDSel(gc.ChildType); deeper != "" {
				gsel += " " + deeper
			}
			sel += " " + gc.ChildField + " { " + gsel + " }"
		}
		out[oc.ChildField] = sel
	}
	return out
}

// runGraphQL executes a query against DGraph and returns the named root field.
func runGraphQL(ctx context.Context, url, query, rootField string) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, fmt.Errorf("marshal query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("query dgraph: %w", err)
	}
	defer resp.Body.Close()

	var env struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(env.Errors) > 0 {
		return nil, fmt.Errorf("%s", env.Errors[0].Message)
	}
	return env.Data[rootField], nil
}

// runGraphQLAll is runGraphQL for a query with more than one root field.
//
// The list page asks for a page of rows AND the total count, and they belong in
// ONE request: two round trips to answer one question about one page is latency
// nobody asked for, and the two answers could disagree if a write landed
// between them.
func runGraphQLAll(ctx context.Context, url, query string) (map[string]json.RawMessage, error) {
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, fmt.Errorf("marshal query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("query dgraph: %w", err)
	}
	defer resp.Body.Close()

	var env struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(env.Errors) > 0 {
		return nil, fmt.Errorf("%s", env.Errors[0].Message)
	}
	return env.Data, nil
}

// viewBySlug finds the view a URL segment addresses.
func viewBySlug(views []configitems.View, slug string) (configitems.View, bool) {
	for _, v := range views {
		if v.Slug == slug {
			return v, true
		}
	}
	return configitems.View{}, false
}

// stringify renders a decoded JSON value for a table cell. Kept here rather
// than in the template because Go templates cannot tell a JSON number from a
// string, and `1.05e+08` for a capacity is not a useful thing to show anyone.
// viewByType finds the view for a concrete type name.
func viewByType(views []configitems.View, typeName string) (configitems.View, bool) {
	for _, v := range views {
		if v.Type == typeName {
			return v, true
		}
	}
	return configitems.View{}, false
}

func stringify(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case map[string]any:
		// An entity REFERENCE is not a value: a map carrying orbId is another
		// record, and the page renders it as a link. JSON-encoding it here would
		// put `{"orbId":"…","name":"…"}` in a table cell.
		if _, isRef := t["orbId"]; isRef {
			// A reference renders as a link, and the link needs text. `name` is
			// optional on ConfigItem and genuinely empty on some types — an
			// IPAddress is identified by its address, a Server by its hostname
			// — so a cell keyed on `name` alone rendered blank and looked like
			// a broken join. Fall back to the orbId, which every ConfigItem has.
			if n, _ := t["name"].(string); n == "" {
				if id, ok := t["orbId"].(string); ok {
					t["name"] = refLabel(id)
				}
			}
			return t
		}
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	case []any:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	default:
		return t
	}
}

func stringifyRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = stringify(v)
	}
	return out
}

// relatedRows normalises a relationship value into rows. DGraph returns a list
// for `[X]` and a single object for `X`; the page renders both as a table.
// rowsForTab returns a tab's rows, following a dotted PATH when the tab names
// one.
//
// A path tab (`storageControllers.storageDevices`) flattens: every device of
// every controller becomes one row, which is the shape the hand-written page
// showed — one row per disk, with the controller named in a column. That column
// needs no special handling: the grandchild declares the inverse edge
// (`StorageDevice.storageController`), so it is already one of its reference
// columns.
//
// Nothing is fetched for this. An owned child's grandchildren are already in
// the detail query — the editor needs the subtree — so a path tab is a second
// reading of data the page has, not a second trip.
func rowsForTab(entity map[string]any, field string) []map[string]any {
	mid, leaf, isPath := strings.Cut(field, ".")
	if !isPath {
		return relatedRows(entity[field])
	}
	var out []map[string]any
	for _, parent := range relatedRows(entity[mid]) {
		out = append(out, relatedRows(parent[leaf])...)
	}
	return out
}

func relatedRows(raw any) []map[string]any {
	switch t := raw.(type) {
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{t}
	default:
		return nil
	}
}

// humanFieldLabel turns a GraphQL field name into a tab heading:
// "networkInterfaces" → "Network Interfaces".
// humanFieldLabel turns a camelCase field name into a heading: "uHeight" →
// "U Height", "tinkerbellIP" → "Tinkerbell IP".
//
// A run of capitals is an acronym and stays together — splitting on every
// capital rendered "Tinkerbell I P", which reads as a typo rather than a label.
// A capital that STARTS a run still breaks the previous word, and the last
// capital of a run starts the next word when a lowercase follows it
// ("oobIPAddress" → "Oob IP Address").
// tabLabel names a relationship panel. A PATH tab is labelled by its LEAF —
// "Storage Devices", not "Storage Controllers.storage Devices" — because the
// path is how the rows are reached, not what they are.
func tabLabel(field string, label func(string) string) string {
	if _, leaf, isPath := strings.Cut(field, "."); isPath {
		return label(leaf)
	}
	return label(field)
}

// humanFieldLabel title-cases a field name. It lives in configitems because the
// VIEW carries the resolved labels now, and the fallback has to be the same rule
// on both sides of that boundary — an integrator reading the response and
// orbital rendering its own header must not disagree about "oobIP".
func humanFieldLabel(field string) string { return configitems.HumanFieldLabel(field) }

// refLabel is the link text for a reference with no name of its own.
//
// The namespace prefix is dropped: every orbId on a page shares it, the
// metadata box already states it, and "colo:10.20.22.208" reads as an
// identifier where "10.20.22.208" reads as the address it is. Only the PREFIX
// goes — the rest of an orbId is its natural key, which is the part that
// identifies the thing.
func refLabel(orbID string) string {
	if i := strings.Index(orbID, ":"); i >= 0 && i+1 < len(orbID) {
		return orbID[i+1:]
	}
	return orbID
}

// idNameVersionSel is idNameSel plus the OCC counter, which an editable member
// needs so the editor can version-guard it.
//
// `version` is declared on ConfigItem like `orbId` and `name`, so it has to go
// INSIDE the type condition — appending it outside fails validation exactly the
// same way, and fails the whole query with it.
func idNameVersionSel(v configitems.View) string {
	if v.IsInterface {
		return "__typename ... on ConfigItem { orbId name version }"
	}
	return "orbId name version"
}

// idNameSel is how a RELATED entity's identity is selected.
//
// orbId and name are declared on ConfigItem, not on a sub-interface, so
// selecting them bare off an interface-typed field is rejected at validation.
// The inline fragment is the only spelling that works for both.
func idNameSel(v configitems.View) string {
	if v.IsInterface {
		return "__typename ... on ConfigItem { orbId name }"
	}
	return "orbId name"
}

// resolveConcreteType answers "what kind of thing is this orbId?".
//
// Needed because an interface view has no detail query of its own: DGraph
// generates get<T> only for a type with an @id field, and orbId is declared on
// ConfigItem, not on a sub-interface. queryConfigItem CAN filter on orbId, so
// one lookup by __typename turns /clusters/<id> into the right concrete page.
//
// Returns "" when nothing holds that orbId — the caller renders a 404, which is
// the right answer and distinct from an error.
func resolveConcreteType(ctx context.Context, dgraphURL, orbID string) (string, error) {
	q := fmt.Sprintf(`{ queryConfigItem(filter: { orbId: { eq: %q } }, first: 1) { __typename } }`, orbID)
	raw, err := runGraphQL(ctx, dgraphURL, q, "queryConfigItem")
	if err != nil {
		return "", err
	}
	var rows []struct {
		Typename string `json:"__typename"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return "", fmt.Errorf("decode __typename: %w", err)
		}
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Typename, nil
}
