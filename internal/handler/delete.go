package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/labstack/echo/v4"

	"github.com/armada/orbital/web"
)

const maxDeleteListItems = 5

type DeleteGroup struct {
	Label string   `json:"label"`
	Items []string `json:"items,omitempty"` // named items, truncated to maxDeleteListItems
	Extra int      `json:"extra,omitempty"` // count beyond Items
	Count int      `json:"count,omitempty"` // for groups with no named items
}

type DeletePreview struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	TotalCount int    `json:"totalCount"`
	// Version is the root entity's OCC counter as the preview read it. The
	// modal echoes it back on confirm as ?version=, so a delete is refused if
	// the entity moved while the confirmation dialog sat open — which is
	// precisely the window a confirmation dialog creates.
	Version int `json:"version,omitempty"`
	// TypeLabel is the singular display name of the type being deleted, from
	// the view. The preview prose used to be a three-way `if` on literal type
	// names that called everything else "server".
	TypeLabel string        `json:"typeLabel,omitempty"`
	Groups    []DeleteGroup `json:"groups"`
	Preserved []DeleteGroup `json:"preserved,omitempty"`
}

type DeleteHandler struct {
	dgraphURL     string
	dgraphDQLBase string // dgraphURL with /graphql stripped
	db            *ent.Client
	logger        *slog.Logger
	// gql is here for ONE reason: the approval gate. This endpoint writes via
	// DQL, so it cannot reuse writeToDGraph's chokepoint and has to ask the
	// policy question directly. See guardDelete.
	gql         *GraphQL
	previewTmpl *template.Template
	// views and fields are what the cascade is DERIVED from: the view says which
	// members are part of a page's unit, and the SDL says which edge points back
	// along each one.
	views  ViewsProvider
	fields *SharedFields
}

func NewDeleteHandler(dgraphURL string, db *ent.Client, logger *slog.Logger, gql *GraphQL, opts ...HandlerOption) *DeleteHandler {
	return &DeleteHandler{
		views:         viewsFrom(opts),
		fields:        sharedFieldsFrom(opts),
		dgraphURL:     dgraphURL,
		dgraphDQLBase: strings.TrimSuffix(dgraphURL, "/graphql"),
		db:            db,
		logger:        logger,
		gql:           gql,
		previewTmpl:   parseDeletePreviewTmpl(),
	}
}

func parseDeletePreviewTmpl() *template.Template {
	return template.Must(template.ParseFS(web.Dir(), "templates/orbital/partials/config-item-delete-preview.gohtml"))
}

// Preview returns an HTML fragment describing the impact of the delete without modifying anything.
func (h *DeleteHandler) Preview(c echo.Context) error {
	id := c.QueryParam("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id required")
	}
	ctx := c.Request().Context()
	plan, err := h.planFor(ctx, c.QueryParam("type"), id)
	if err != nil {
		return err
	}
	preview := plan.preview
	tmpl := h.previewTmpl
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return renderHTML(c, tmpl, "", preview)
}

// Execute performs the cascade delete for the given config item.
//
// This REST endpoint exists specifically to back the UI's cascade-delete flow:
// a DataCenter or Server delete must (a) gather a single before-state and
// audit record, (b) remove the node together with its dependent children in
// one transaction, and (c) pair with `GET /config-items/delete-preview` for
// the impact summary. None of that fits a single auto-generated GraphQL
// mutation cleanly.
//
// Single-entity, non-cascading mutations (CRUD on individual ConfigItems) go
// through GraphQL at `/graphql`. This endpoint is not a general-purpose REST
// CRUD surface — see CLAUDE.md § Settled Decisions, REST API convention.
//
// @Summary     Cascade-delete a config item (UI flow)
// @Description Deletes a DataCenter or Server together with its dependent
// @Description children. Bound to the UI delete modal's confirm action.
// @Description Single-entity (non-cascading) deletes go through GraphQL.
// @Tags        config-items
// @Produce     json
// @Param       type path string true "Config item type" Enums(DataCenter, Server)
// @Param       id   path string true "DGraph node id"
// @Success     200 {object} map[string]int "{ \"deleted\": N }"
// @Failure     400 {object} errorResponse
// @Failure     500 {object} errorResponse
// @Router      /api/v1/config-items/{type}/{id} [delete]
func (h *DeleteHandler) Execute(c echo.Context) error {
	// Path-param decoding is handled by middleware.DecodePathParams.
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id required")
	}
	ctx := c.Request().Context()
	actor := actorFromContext(c)
	typeName := c.Param("type")
	caller := resolveCallerRole(c, h.db)

	// Before planning: a caller holding a stale view should be told to reload,
	// not have a cascade computed on their behalf.
	if err := h.checkDeleteVersion(ctx, id, c.QueryParam("version")); err != nil {
		return h.refuse(c, err)
	}

	plan, err := h.planFor(ctx, typeName, id)
	if err != nil {
		return err
	}
	if err := h.guardDelete(ctx, caller, actor, plan.orbID, plan.uids); err != nil {
		return h.refuse(c, err)
	}
	if err := h.bulkDeleteGuarded(ctx, plan.uids, plan.versions, plan.dangling); err != nil {
		var perr *preflightError
		if errors.As(err, &perr) {
			return h.refuse(c, err) // concurrent edit — a decision, not a failure
		}
		h.logger.Error("cascade delete failed", "type", typeName, "orbId", plan.orbID, "err", err)
		return fmt.Errorf("delete %s: %w", typeName, err)
	}
	// One audit event per delete, naming the type the caller actually asked for.
	// It used to be three near-identical blocks differing only in a literal.
	op := "delete" + typeName
	writeAuditEvent(h.db, h.logger, "data", actor, op,
		[]string{op}, []string{typeName}, []string{plan.orbID},
		map[string]any{
			"input":  map[string]any{"orbId": plan.orbID},
			"before": plan.before,
			"result": map[string]any{"totalDeleted": len(plan.uids), "breakdown": plan.preview.Groups},
		},
		originFromContext(c, "rest"),
	)
	return c.JSON(http.StatusOK, map[string]any{"deleted": len(plan.uids)})
}

// planFor resolves the views once and plans the cascade.
//
// It replaced a three-way switch on literal type names. Any ConfigItem type is
// deletable now, because a cascade is derived from the view rather than written
// out per root — which is also what made `deletableType`'s interface special
// case unnecessary.
func (h *DeleteHandler) planFor(ctx context.Context, typeName, orbID string) (*cascadePlan, error) {
	if typeName == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "type required")
	}
	if h.views == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable,
			"orbital cannot read its view configuration, so it does not know what a delete would remove")
	}
	views, err := h.views(ctx)
	if err != nil {
		h.logger.Warn("delete: cannot resolve views", "type", typeName, "err", err)
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable,
			"orbital cannot read the schema right now, so it does not know what a delete would remove")
	}
	return h.planCascade(ctx, views, typeName, orbID)
}

// ── plan types ────────────────────────────────────────────────────────────────
type danglingEdge struct {
	ParentUID string // the surviving node holding the edge
	Predicate string // DGraph predicate, e.g. "DataCenter.kubernetesClusters"
	ChildUID  string // the node being deleted
}

func (h *DeleteHandler) gqlQuery(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dgraph: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		Data   json.RawMessage            `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("dgraph: %s", result.Errors[0].Message)
	}
	return result.Data, nil
}

// planVersions reads the version of every node a plan intends to delete, as of
// planning time. It is the BASELINE for the compare-and-swap below.
//
// Read separately rather than threaded through the three plan queries: those
// select four levels of owned children (server → controller → device → volume)
// and adding `version` to each selection and its struct would touch far more
// code for the same value. One DQL read of the already-collected uid set gives
// the same snapshot at the same instant.
func (h *DeleteHandler) planVersions(ctx context.Context, uids []string) (map[string]int, error) {
	out := make(map[string]int, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	q := fmt.Sprintf(`{ q(func: uid(%s)) { uid ConfigItem.version } }`, strings.Join(uids, ","))
	body, _ := json.Marshal(map[string]any{"query": q})
	resp, err := http.Post(h.dgraphDQLBase+"/query", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("read plan versions: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("read plan versions %d: %s", resp.StatusCode, raw)
	}
	var parsed struct {
		Data struct {
			Q []struct {
				UID     string `json:"uid"`
				Version *int   `json:"ConfigItem.version"`
			} `json:"q"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode plan versions: %w", err)
	}
	for _, n := range parsed.Data.Q {
		if n.Version != nil {
			out[n.UID] = *n.Version
		}
	}
	return out, nil
}

// bulkDeleteGuarded deletes the planned set as a COMPARE-AND-SWAP: the delete
// is applied only if every node is still at the version planning saw.
//
// Why this exists. The previous implementation posted `{"delete": [{uid}…]}`
// with no predicate at all, so a cascade destroyed whatever was there at commit
// time. `checkDeleteVersion` guards the PARENT and does so check-then-act, with
// planning and an approval-gate round trip inside the window; children were
// never checked at any point. That made DELETE — the irreversible operation —
// weaker than UPDATE, where injectVersionPredicate puts the version inside the
// write and a loser gets a 409.
//
// Grouped by version value rather than one block per node, and that is EXACT,
// not an approximation: `func: uid(…)` pins the set so no foreign node can
// enter a group, and versions are server-stamped and monotonic so nothing moves
// to a lower one. Any edit therefore strictly drops its group's count. Most
// nodes sit at version 1, so a large cascade is a handful of blocks.
//
// All-or-nothing: one conditional mutation, so a refusal can never leave a
// half-collapsed tree.
func (h *DeleteHandler) bulkDeleteGuarded(ctx context.Context, uids []string, versions map[string]int, dangling []danglingEdge) error {
	if len(uids) == 0 {
		return nil
	}
	// A node we cannot guard is a node we would delete blind — which is the bug
	// being fixed. Refuse instead. Every ConfigItem is stamped on create, so this
	// fires only on data written before stamping existed.
	var unversioned []string
	byVersion := map[int][]string{}
	for _, uid := range uids {
		v, ok := versions[uid]
		if !ok {
			unversioned = append(unversioned, uid)
			continue
		}
		byVersion[v] = append(byVersion[v], uid)
	}
	if len(unversioned) > 0 {
		h.logger.Warn("cascade delete refused — planned nodes carry no version, so the delete cannot be guarded",
			"count", len(unversioned), "uids", strings.Join(unversioned, ","))
		return &preflightError{
			Status: http.StatusConflict, Code: CodeMVCCConflict,
			Message: fmt.Sprintf("%d of the %d records in this delete carry no version, so the delete cannot be checked for concurrent edits and was not performed.", len(unversioned), len(uids)),
			Hint:    "This usually means the records predate version stamping. Re-save them, or contact an administrator.",
		}
	}

	var blocks, conds []string
	want := map[string]int{}
	i := 0
	for v, group := range byVersion {
		name := fmt.Sprintf("v%d", i)
		i++
		blocks = append(blocks, fmt.Sprintf(`%s as var(func: uid(%s)) @filter(eq(ConfigItem.version, %d))`,
			name, strings.Join(group, ","), v))
		blocks = append(blocks, fmt.Sprintf(`c%s(func: uid(%s)) { count(uid) }`, name, name))
		conds = append(conds, fmt.Sprintf("eq(len(%s), %d)", name, len(group)))
		want["c"+name] = len(group)
	}
	// Stable order: map iteration is random, and a query that differs run to run
	// is one nobody can diff against a log line.
	sort.Strings(blocks)
	sort.Strings(conds)

	delNodes := make([]map[string]any, 0, len(uids)+len(dangling))
	for _, uid := range uids {
		delNodes = append(delNodes, map[string]any{"uid": uid})
	}
	// Clear the surviving parents' edges in the SAME guarded transaction. Doing
	// it afterwards would leave a window where the node is gone and the edge is
	// not, which is the broken state this prevents.
	for _, e := range dangling {
		delNodes = append(delNodes, map[string]any{
			"uid":       e.ParentUID,
			e.Predicate: map[string]string{"uid": e.ChildUID},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"query": "{ " + strings.Join(blocks, " ") + " }",
		"mutations": []map[string]any{{
			"cond":   "@if(" + strings.Join(conds, " AND ") + ")",
			"delete": delNodes,
		}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphDQLBase+"/mutate?commitNow=true", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build dql upsert: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("dql upsert: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("dql upsert %d: %s", resp.StatusCode, raw)
	}

	// DGraph answers an unmet `@if` with a 200 and an empty mutation — success-
	// shaped, exactly like the numUids:0 case the GraphQL CAS had to learn to
	// read. The per-group counts come back in the same response, so a miss is
	// detected without a second round trip.
	var parsed struct {
		Data struct {
			Queries map[string][]struct {
				Count int `json:"count"`
			} `json:"queries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("decode dql upsert: %w", err)
	}
	for name, n := range want {
		got := 0
		if rows := parsed.Data.Queries[name]; len(rows) > 0 {
			got = rows[0].Count
		}
		if got != n {
			h.logger.Warn("cascade delete refused — a record changed between planning and deletion",
				"group", name, "want", n, "got", got, "planned", len(uids))
			return &preflightError{
				Status: http.StatusConflict, Code: CodeMVCCConflict,
				Message: h.describeStaleNodes(ctx, uids, versions),
				Hint:    "Reload and try again — the delete preview is out of date.",
			}
		}
	}
	return nil
}

// describeStaleNodes names what changed, for the refusal message. Runs only on
// the rare conflict path: "this record was modified" is not actionable when a
// cascade spans a hundred entities and the caller cannot see which one moved.
func (h *DeleteHandler) describeStaleNodes(ctx context.Context, uids []string, planned map[string]int) string {
	generic := "Part of this delete was modified by someone else. Nothing was deleted — reload and try again."
	current, err := h.planVersions(ctx, uids)
	if err != nil {
		return generic
	}
	q := fmt.Sprintf(`{ q(func: uid(%s)) { uid ConfigItem.orbId dgraph.type } }`, strings.Join(uids, ","))
	body, _ := json.Marshal(map[string]any{"query": q})
	resp, err := http.Post(h.dgraphDQLBase+"/query", "application/json", bytes.NewReader(body))
	if err != nil {
		return generic
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Data struct {
			Q []struct {
				UID   string   `json:"uid"`
				OrbID string   `json:"ConfigItem.orbId"`
				Types []string `json:"dgraph.type"`
			} `json:"q"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return generic
	}
	var names []string
	for _, n := range parsed.Data.Q {
		if cur, ok := current[n.UID]; !ok || cur != planned[n.UID] {
			label := n.OrbID
			if len(n.Types) > 0 && label != "" {
				label = n.Types[len(n.Types)-1] + " " + label
			}
			if label != "" {
				names = append(names, label)
			}
		}
	}
	if len(names) == 0 {
		return generic
	}
	sort.Strings(names)
	if len(names) > 3 {
		return fmt.Sprintf("%s and %d more were modified by someone else. Nothing was deleted — reload and try again.",
			strings.Join(names[:3], ", "), len(names)-3)
	}
	return fmt.Sprintf("%s was modified by someone else. Nothing was deleted — reload and try again.",
		strings.Join(names, ", "))
}

// refuse renders a guard's decision, or passes a genuine error through.
//
// The guards return a DECISION and never write. They used to call writeError
// directly, which was a fail-open bug worth remembering: writeError returns the
// result of c.JSON, which is nil on success, so `if err != nil { return err }`
// never fired — the 409 was written to the response AND the cascade went ahead
// and deleted. Status said refused, body said refused, entity gone. Caught only
// because the test asserted the entity still existed rather than stopping at
// the status code.
func (h *DeleteHandler) refuse(c echo.Context, err error) error {
	var pe *preflightError
	if errors.As(err, &pe) {
		return writeError(c, pe.Status, pe.Code, pe.Message, pe.Hint)
	}
	return err
}

// typesOfUIDs reads the ConfigItem types of a planned cascade, straight from the
// nodes it is about to delete.
//
// Derived rather than declared, deliberately. The alternative — tagging each
// plan builder's branches with the type they append — puts the gate's input in
// nine hand-maintained places across three functions, and the failure mode of
// forgetting one is SILENT UNDER-GATING: a protected child quietly stops being
// protected the day someone adds a branch. Asking DGraph what the uids actually
// are cannot drift, and costs one read on a rare, destructive, human-driven
// operation.
//
// Returns an error rather than an empty list when the read fails: an empty list
// would read as "no protected types here" and wave the delete through.
func (h *DeleteHandler) typesOfUIDs(ctx context.Context, uids []string) ([]string, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	dql := fmt.Sprintf(`{ nodes(func: uid(%s)) { dgraph.type } }`, strings.Join(uids, ", "))
	body, err := json.Marshal(map[string]string{"query": dql})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphDQLBase+"/query", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read cascade types: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("read cascade types (%d): %s", resp.StatusCode, raw)
	}
	var decoded struct {
		Data struct {
			Nodes []struct {
				Types []string `json:"dgraph.type"`
			} `json:"nodes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode cascade types: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range decoded.Data.Nodes {
		for _, t := range n.Types {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// guardDelete is the pair of checks a cascade delete has to pass before
// anything is removed: the caller's optimistic-concurrency precondition, and
// the approval policy.
//
// The gate half closes a MEASURED bypass (debt.md Track A2): this endpoint
// plans a cascade and POSTs a DQL delete, so it never passed through
// writeToDGraph and checkApprovalPolicy never ran. Under a policy with no
// bypass roles, `updateDataCenter` was refused 403 while DELETE of that same
// entity returned 200 seconds later — a rename gated, a cascade delete not.
//
// Ordering: the version check runs BEFORE planning (a stale caller should be
// told to reload, not have a cascade computed for them), the gate AFTER, so it
// can see every type the cascade would remove rather than only the declared one.
func (h *DeleteHandler) guardDelete(ctx context.Context, caller callerRole, actor, orbID string, uids []string) error {
	if h.gql == nil {
		// Fail closed. A missing dependency must not silently disable a
		// security control — that is how the bypass above went unnoticed.
		return echo.NewHTTPError(http.StatusInternalServerError, "approval gate not configured")
	}
	types, err := h.typesOfUIDs(ctx, uids)
	if err != nil {
		return err
	}
	bypassed, err := h.gql.checkPolicyFor(ctx, []string{orbID}, types, caller, actor)
	if err != nil {
		var gerr *gatedError
		if errors.As(err, &gerr) {
			// Same status, code and hint the /graphql refusal produces. A caller
			// must not have to learn two refusals for one control.
			return &preflightError{Status: gerr.Status, Code: gerr.Code, Message: gerr.Message, Hint: gerr.Hint}
		}
		return err
	}
	if bypassed != "" {
		h.logger.Warn("privileged delete — bypassed an approval policy",
			"policy", bypassed, "actor", actor, "role", string(caller.Role), "orb_id", orbID,
			"types", strings.Join(types, ","), "entities", len(uids))
	}
	return nil
}

// checkDeleteVersion enforces `?version=` on a delete.
//
// A query parameter rather than an If-Match header: orbital already spells this
// precondition `version` on /graphql and in a changeset item, and a third
// spelling for the same question is exactly the API cost this whole change set
// exists to remove. DELETE carries no body by convention, so a parameter is
// where it goes.
//
// Absent means unconditional, matching every other path. Present and
// unparseable is a 400, not a 409 — retrying the same garbage would loop.
func (h *DeleteHandler) checkDeleteVersion(ctx context.Context, orbID, raw string) error {
	if raw == "" {
		return nil
	}
	want, err := strconv.Atoi(raw)
	if err != nil {
		return &preflightError{Status: http.StatusBadRequest, Code: CodeBadUserInput, Message: "version must be an integer"}
	}
	// queryConfigItem, NOT get{Type}: `KubernetesCluster` is an INTERFACE and
	// DGraph generates no `getKubernetesCluster`, so the typed form 500s on
	// exactly the delete this guards. Every ConfigItem carries orbId and version
	// by definition, so the interface query works for concrete and interface
	// types alike — it is what planClusterDelete already uses.
	data, err := h.gqlQuery(ctx,
		`query GetVersion($orbId: String!) { queryConfigItem(filter: { orbId: { eq: $orbId } }, first: 1) { version } }`,
		map[string]any{"orbId": orbID})
	if err != nil {
		return err
	}
	var resp struct {
		Items []struct {
			Version *int `json:"version"`
		} `json:"queryConfigItem"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("decode version: %w", err)
	}
	if len(resp.Items) == 0 || resp.Items[0].Version == nil {
		// No version to compare. Refused rather than waved through: a caller
		// that asked for a check and did not get one believes it is protected.
		return &preflightError{Status: http.StatusConflict, Code: CodeMVCCConflict,
			Message: "cannot verify this entity's version", Hint: "Reload the entity and try again."}
	}
	if *resp.Items[0].Version != want {
		return &preflightError{Status: http.StatusConflict, Code: CodeMVCCConflict,
			Message: fmt.Sprintf("This record was modified by someone else (you saw version %d, it is now %d). Please reload and try again.", want, *resp.Items[0].Version),
			Hint:    "Reload the entity and delete again if you still want to."}
	}
	return nil
}

// ── Group builders ────────────────────────────────────────────────────────────

func namedGroup(label string, names []string) DeleteGroup {
	if len(names) <= maxDeleteListItems {
		return DeleteGroup{Label: label, Items: names}
	}
	return DeleteGroup{Label: label, Items: names[:maxDeleteListItems], Extra: len(names) - maxDeleteListItems}
}

func countGroup(label string, count int) DeleteGroup {
	return DeleteGroup{Label: label, Count: count}
}
