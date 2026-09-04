package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/metrics"
	"github.com/labstack/echo/v4"
)

// knownMutationRe matches any DGraph mutation call on a registered ConfigItem
// type. Derived from `internal/configitems.Types` — adding a type to that
// registry is the single source of truth; this regex updates automatically.
var knownMutationRe = configitems.KnownMutationsRegex()

// orbIdFilterRe extracts orbId values from inline GraphQL filter expressions:
// e.g. filter: { orbId: { eq: "alaska-dot:GRTLY24" } }
var orbIdFilterRe = regexp.MustCompile(`orbId\s*:\s*\{\s*eq\s*:\s*"([^"]+)"`)

var mutationOpRe = regexp.MustCompile(`(?i)^\s*mutation\s+(\w+)`)
var queryOpRe = regexp.MustCompile(`(?i)^\s*query\s+(\w+)`)

// beforeFetchOverrides maps an operation name to the resource type whose
// `before` snapshot should be fetched. The default path derives the resource
// type from the mutation body via extractOperations; this map is for the rare
// case where the operation name implies a different resource than the body
// alone would suggest.
//
// Currently empty: every UI mutation now dispatches canonical
// `update{Kind}($orbId, $set)` via configitem-editor.js, so extractOperations
// finds the right type in the body. The legacy UpdateServerAndIdrac compound
// mutation that required override entries is gone (Server edit now dispatches
// parallel updateServer + updateIdracSettings).
//
// Keep this var as a hook; future compound mutations can register here without
// reshaping the audit pipeline.
var beforeFetchOverrides = map[string]string{}

type GraphQL struct {
	dgraphURL string
	db        *ent.Client
	logger    *slog.Logger
	// rejectInlineSelectors, when true, 400s single-entity update mutations whose
	// selector/set are inline literals instead of variables — the shape the proxy
	// can't stamp. See docs/reference/ERROR-RESPONSES.md.
	rejectInlineSelectors bool
}

func NewGraphQL(dgraphURL string, db *ent.Client, logger *slog.Logger, rejectInlineSelectors bool) *GraphQL {
	return &GraphQL{dgraphURL: dgraphURL, db: db, logger: logger, rejectInlineSelectors: rejectInlineSelectors}
}

// DGraphURL exposes the configured DGraph endpoint for adjacent handlers that
// need to issue point-in-time reads (e.g. the divergence Accept handler's MVCC
// re-fetch). Avoids passing the URL string around redundantly.
func (h *GraphQL) DGraphURL() string { return h.dgraphURL }

type gqlRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
}

// Handle proxies GraphQL requests to DGraph and serves GraphiQL on GET.
// Any mutation touching a known ConfigItem type is recorded as an audit event.
// For single-entity mutations that include ifVersion, an MVCC check is performed.
//
// @Summary     GraphQL endpoint
// @Description POST: proxies GraphQL queries and mutations to DGraph. GET: serves the GraphiQL explorer UI.
// @Tags        graphql
// @Accept      json
// @Produce     json
// @Param       body body string true "GraphQL request body" example("{\"query\": \"{ queryDataCenter { id name } }\"}")
// @Success     200 {object} map[string]interface{}
// @Router      /graphql [post]
func (h *GraphQL) Handle(c echo.Context) error {
	if c.Request().Method != http.MethodPost {
		slog.Info("GET /graphql")
		return c.File("web/shared/static/graphiql.html")
	}

	bodyBytes, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	var req gqlRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil || !isMutation(req.Query) {
		opName := req.OperationName
		if opName == "" {
			if m := queryOpRe.FindStringSubmatch(req.Query); len(m) > 1 {
				opName = m[1]
			}
		}
		if opName != "" {
			c.Set("graphql.operation.name", opName)
		}
		c.Set("graphql.operation.type", "query")
		return h.proxyRaw(c, bodyBytes)
	}

	// Enforce dev-or-admin role for all GraphQL mutations.
	if ok, reason := h.authorizeMutation(c); !ok {
		h.logger.Warn("graphql mutation denied", "reason", reason,
			"actor", actorFromContext(c), "request.id", c.Response().Header().Get(echo.HeaderXRequestID))
		return writeError(c, http.StatusForbidden, CodeForbidden,
			"dev or admin role required for mutations",
			"Ask an admin to grant you the dev role.")
	}

	touchesKnownType := knownMutationRe.MatchString(req.Query)

	opName := mutationOpName(&req)
	c.Set("graphql.operation.name", opName)
	c.Set("graphql.operation.type", "mutation")

	actor := actorFromContext(c)

	// Reject single-entity UPDATE mutations that use inline literals instead of
	// variables. Stamping (version/updatedAt/updatedBy) only fires when the proxy
	// can resolve the target via a variable orbId/id AND inject into a variable
	// `set` map; an inline selector or inline set silently bypasses it. Rather than
	// write an unstamped record, refuse loudly and hand back the variable form.
	// Reads and adds are unaffected. Kill switch: ORBITAL_INLINE_SELECTOR_REJECT.
	// Note: this inline detection is regex-based (extractOperations) — the standing
	// intent is to replace the hand-rolled request parsing with a real GraphQL AST
	// parse; see ROADMAP. See docs/reference/ERROR-RESPONSES.md.
	if h.rejectInlineSelectors {
		ops, _ := extractOperations(req.Query)
		for _, op := range ops {
			if !strings.HasPrefix(op, "update") {
				continue // add/delete use different (or no) stamping paths
			}
			entityID, _ := req.Variables["id"].(string)
			orbID, _ := req.Variables["orbId"].(string)
			_, setIsVar := req.Variables["set"].(map[string]any)
			if (entityID != "" || orbID != "") && setIsVar {
				continue // variable form — stamping will fire
			}
			h.logger.Warn("inline-selector update rejected — bypasses server-side stamping",
				"op", op, "actor", actor,
				"request.id", c.Response().Header().Get(echo.HeaderXRequestID))
			// Build a copy-pasteable variable-form example for the caller's actual
			// type (op is "update<Kind>", e.g. updateIdracSettings → IdracSettings).
			kind := strings.TrimPrefix(op, "update")
			hint := fmt.Sprintf(`Rewrite with variables — query: mutation Update%s($orbId: String!, $set: %sPatch!) { update%s(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } } — variables: { "orbId": "namespace:name", "set": { ...fields to change... } }`, kind, kind, kind)
			return writeError(c, http.StatusBadRequest, CodeVariableFormRequired,
				fmt.Sprintf("update%s must pass both orbId and set as GraphQL variables, not inline literals: orbital resolves the row via orbId to bump version, and stamps updatedAt/updatedBy into set — it can't do either against inline values.", kind),
				hint)
		}
	}

	// The write pre-flight — before-fetch, `ifVersion` check, stamping — is NOT
	// here. It lives in writeToDGraph so that EVERY write gets it and not only
	// the ones that arrive over HTTP; see that function's doc comment.
	//
	// context.Background(), not c.Request().Context(), and deliberately so: the
	// previous http.Post carried no context, so a client disconnect never aborted
	// an in-flight mutation. Propagating the request context here would let a
	// dropped connection cancel a write mid-flight, leaving the caller unable to
	// know whether it applied. Preserving the existing behaviour is both the safer
	// semantics for a mutation and a strict no-op for this refactor.
	res, err := h.writeToDGraph(context.Background(), bodyBytes, actor, resolveCallerRole(c, h.db), gateEnforce, nil)
	if err != nil {
		var gerr *gatedError
		if errors.As(err, &gerr) {
			h.logger.Warn("mutation refused — approval required",
				"policy", gerr.Policy, "actor", actor,
				"request.id", c.Response().Header().Get(echo.HeaderXRequestID))
			return writeError(c, gerr.Status, gerr.Code, gerr.Message, gerr.Hint)
		}
		var perr *preflightError
		if errors.As(err, &perr) {
			return writeError(c, perr.Status, perr.Code, perr.Message, perr.Hint)
		}
		return err
	}

	// res.Variables, not req.Variables: the pre-flight stamped its own parse of
	// the body, so the map Handle unmarshalled is the UNSTAMPED one. Auditing it
	// would record a mutation orbital did not send.
	if touchesKnownType && h.db != nil && !hasGQLErrors(res.Body) {
		operations, resourceTypes := extractOperations(req.Query)
		resourceIDs := extractResourceIDs(req.Query, res.Variables, res.Body)
		go h.auditMutation(opName, operations, resourceTypes, resourceIDs, actor, req.Query, res.Variables, res.Before, res.Bypassed)
	}

	c.Response().Header().Set("Content-Type", "application/json")
	_, err = c.Response().Write(res.Body)
	return err
}

// DispatchMutation runs a server-internal GraphQL mutation against DGraph.
//
// Use it when an orbital-internal action (e.g. accepting a divergence override)
// needs to mutate intent and have the change appear in the audit log just like
// a user-driven mutation. It does NOT enforce role gating — callers must have
// already authz'd.
//
// It DOES get the full write pre-flight — before-fetch, version/updatedAt/
// updatedBy stamping, and the ifVersion check — because all three live in
// writeToDGraph, which every GraphQL write passes through. A caller neither opts
// in nor can forget: the two that existed when this was written each forgot a
// different half, and nothing failed. (The cascade-delete endpoint writes DQL
// and does not come through here; it carries its own checks.)
//
// before is an optional FALLBACK for the audit diff, not a substitute for the
// fetch. writeToDGraph reads current state regardless — it has to, since the
// next version is one more than the current one, and a counter cannot be
// incremented unread — and overlays that read on whatever the caller supplied.
// Pass it only when you hold field values the type's BeforeFields selection
// does not cover; nil is the right answer for most callers.
//
// Returns the raw DGraph response body alongside the error so callers can
// inspect specific GraphQL errors when needed. A non-nil error means the
// mutation did not succeed and any side-effects (e.g. recording a resolution)
// MUST be skipped.
//
// caller is the identity the approval gate judges — an `actor` string cannot
// answer "is this role allowed to bypass". gate says whether the approval check
// applies; pass gateExempt ONLY with a reason at the call site.
func (h *GraphQL) DispatchMutation(ctx context.Context, actor string, caller callerRole, gate gateMode, query string, variables map[string]any, before map[string]any) ([]byte, error) {
	body, err := json.Marshal(gqlRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, fmt.Errorf("marshal mutation: %w", err)
	}
	res, err := h.writeToDGraph(ctx, body, actor, caller, gate, before)
	if err != nil {
		return res.Body, err
	}
	if res.Status != http.StatusOK {
		return res.Body, fmt.Errorf("dgraph returned %d", res.Status)
	}
	if hasGQLErrors(res.Body) {
		return res.Body, errors.New(firstGQLError(res.Body))
	}
	if h.db != nil {
		opName := ""
		if m := mutationOpRe.FindStringSubmatch(query); len(m) > 1 {
			opName = m[1]
		}
		operations, resourceTypes := extractOperations(query)
		resourceIDs := extractResourceIDs(query, res.Variables, res.Body)
		go h.auditMutation(opName, operations, resourceTypes, resourceIDs, actor, query, res.Variables, res.Before, res.Bypassed)
	}
	return res.Body, nil
}

// writeResult is what the write path actually did.
//
// Before and Variables come back because the pre-flight below produces them and
// the caller cannot: Variables is the map AS SENT (stamped, ifVersion removed),
// and Before is the state the write replaced. A caller that rebuilt either from
// its own inputs would be auditing something other than what happened — which
// is the bug this whole function exists to make impossible.
type writeResult struct {
	Body      []byte         // raw DGraph response, unjudged
	Status    int            // DGraph's HTTP status
	Bypassed  string         // approval policy this caller bypassed, or ""
	Before    map[string]any // pre-write state: the fetch overlaid on the caller's fallback
	Variables map[string]any // variables as forwarded, post-stamp, with orbId restored
}

// preflightError is a refusal from the concurrency guard: a stale ifVersion, a
// malformed one, or a predicate that could not be applied.
//
// Named for where it USED to fire. One case is post-write: a compare-and-swap
// that matched no row is detected from the response. Name kept because both
// entry points already unwrap this type.
//
// A typed error for the same reason gatedError is one — the status and code are
// decided where the refusal happens, so the two entry points cannot each invent
// their own answer to "what does a conflict look like".
type preflightError struct {
	Status  int
	Code    string
	Message string
	Hint    string
}

func (e *preflightError) Error() string { return e.Message }

// writeToDGraph is THE single place a mutation reaches the graph. Both entry
// points — the client-facing Handle and the internal DispatchMutation — go
// through here.
//
// Why one function rather than two call sites: Spike 36 D14. `/graphql` is a
// chokepoint for CLIENTS, not for WRITES — Handle and DispatchMutation each used
// to POST independently, so a policy check placed in Handle alone was bypassable
// via divergence-Accept (which dispatches update{Type} to make intent match the
// edge). Gating both call sites separately means two checks to keep in sync and a
// third path, added later, that nobody remembers to gate. The approval gate
// (Session 2) installs its check HERE, making it structurally unbypassable rather
// than unbypassable by convention.
//
// The WRITE PRE-FLIGHT is here for exactly the same reason, and was moved down
// out of Handle on 2026-09-03. It is three things over one read of the target:
// the ifVersion comparison, the version auto-increment plus updatedBy/updatedAt
// stamping, and the before-state the audit diff needs. While they lived in
// Handle, DispatchMutation got none of them and said so in its doc comment,
// leaving each to the caller — and both callers forgot a different one. Merge
// passed an incomplete before, so its audit row carried no diff; divergence-
// Accept wrote intent with no version bump, which made an Accept invisible to
// change-request staleness (an approval cast before it kept counting). Neither
// failed anything. Correct-by-default beats correct-by-remembering.
//
// Order is load-bearing and unchanged from Handle's: fetch → ifVersion → stamp →
// strip → approval gate → POST. The gate sees the body as DGraph will, and an
// MVCC conflict is refused before the gate, so a stale write is told to reload
// rather than told to open a change request it would then have to redo.
//
// Deliberately does NOT judge the response: it returns the raw body and the HTTP
// status and lets callers decide. Handle passes DGraph's body through to the
// client untouched (including GraphQL `errors`); DispatchMutation treats a non-200
// or a GraphQL error as a Go error. Encoding either policy here would change the
// other caller's behaviour.
//
// ctx: callers pass context.Background() for client-facing mutations — see the
// note at Handle's call site.
//
// caller and gate exist because a chokepoint that cannot see WHO is writing and
// WHY can only decide nothing. gate is an explicit argument rather than a
// context value so every exemption is visible at its call site and greppable in
// one command — a reviewer must be able to enumerate the ways around a security
// control without reading call graphs.
func (h *GraphQL) writeToDGraph(ctx context.Context, body []byte, actor string, caller callerRole, gate gateMode, beforeFallback map[string]any) (writeResult, error) {
	res := writeResult{Before: beforeFallback}

	var req gqlRequest
	guarded := false
	if json.Unmarshal(body, &req) == nil {
		current := h.fetchCurrentState(&req)
		res.Before = mergeBefore(beforeFallback, current)
		res.Variables = req.Variables

		// Pre-flight first — it refuses the ordinary stale case before any write,
		// with a better message. The predicate below catches only the race.
		if perr := checkIfVersion(req.Variables, current); perr != nil {
			return res, perr
		}
		if perr := injectVersionPredicate(&req); perr != nil {
			return res, perr
		}
		_, guarded = req.Variables["ifVersion"]
		body = forwardBody(&req, body, stampMutation(&req, current, actor) || guarded)
	}

	if gate == gateEnforce {
		bypassed, err := h.checkApprovalPolicy(ctx, body, caller)
		if err != nil {
			return res, err
		}
		res.Bypassed = bypassed
	}

	// Retried because the version predicate makes aborts MORE likely: two writers
	// filtering the same predicate contend where an unfiltered write would not.
	// An abort means the transaction did NOT commit, so re-sending is safe.
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphURL, bytes.NewReader(body))
		if err != nil {
			return res, fmt.Errorf("build dgraph request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")

		dgStart := time.Now()
		resp, err := http.DefaultClient.Do(httpReq)
		metrics.ObserveDGraphCall("mutation", time.Since(dgStart))
		if err != nil {
			return res, fmt.Errorf("proxy to dgraph: %w", err)
		}
		res.Status = resp.StatusCode
		res.Body, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return res, fmt.Errorf("read dgraph response: %w", err)
		}
		if !isTxnAborted(res.Body) {
			break
		}
		if attempt >= txnAbortRetries {
			// Distinct from MVCC_CONFLICT on purpose: that means "your data is
			// stale, re-read". This means "the same request is still valid, the
			// store was busy" — different remedy, so a different code.
			h.logger.Warn("dgraph write abandoned after repeated transaction aborts", "attempts", attempt+1)
			return res, &preflightError{
				Status: http.StatusServiceUnavailable, Code: CodeWriteContention,
				Message: "the write could not be committed because another write kept landing on the same records",
				Hint:    "Retry the same request.",
			}
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}

	// A guarded write that matched nothing lost the race. Without this it is a
	// 200 with `numUids: 0` — success-shaped, and the silent lost update the
	// predicate exists to prevent. Gated on `guarded` so an unguarded update
	// that legitimately matches nothing still returns 200.
	if guarded && res.Status == http.StatusOK && !hasGQLErrors(res.Body) && casMissed(res.Body) {
		return res, &preflightError{
			Status: http.StatusConflict, Code: CodeMVCCConflict,
			Message: "This record was modified by someone else. Please reload and try again.",
		}
	}
	return res, nil
}

// mutationOpName is the operation name orbital uses for logs, audit rows and the
// beforeFetchOverrides lookup. Derived identically wherever it is needed, so the
// override map cannot be keyed on a string one call site would not produce.
func mutationOpName(req *gqlRequest) string {
	if req.OperationName != "" {
		return req.OperationName
	}
	if m := mutationOpRe.FindStringSubmatch(req.Query); len(m) > 1 {
		return m[1]
	}
	ops, _ := extractOperations(req.Query)
	return strings.Join(ops, ",")
}

// fetchCurrentState reads the entity a single-entity mutation targets, or nil
// when the mutation is not a single-entity shape (a bulk add, a multi-type
// mutation, an inline selector) or the read failed.
//
// One read, three consumers: the ifVersion comparison, the version increment,
// and the audit diff's before-state. They are not separable — all three need
// the same row as of the same instant.
//
// The target is resolved from the `id` or `orbId` VARIABLE. A mutation that
// hides its selector anywhere else — inline literals, or a $filter object —
// resolves to nothing here and is therefore neither guarded nor stamped. That
// is why the inline-selector rejection exists on the client path, and why
// internal dispatchers must use the canonical update{Kind}($orbId, $set) shape.
func (h *GraphQL) fetchCurrentState(req *gqlRequest) map[string]any {
	opName := mutationOpName(req)

	resourceType, hasOverride := beforeFetchOverrides[opName]
	if !hasOverride {
		_, resourceTypes := extractOperations(req.Query)
		if len(resourceTypes) == 1 {
			resourceType = resourceTypes[0]
		}
	}
	if resourceType == "" {
		return nil
	}

	entityID, _ := req.Variables["id"].(string)
	orbID, _ := req.Variables["orbId"].(string)

	var (
		current  map[string]any
		fetchErr error
	)
	switch {
	case entityID != "":
		current, fetchErr = h.fetchBeforeByID("get"+resourceType, resourceType, entityID)
	case orbID != "":
		current, fetchErr = h.fetchBeforeByOrbID("query"+resourceType, resourceType, orbID)
	default:
		return nil
	}
	if fetchErr != nil {
		h.logger.Warn("before-fetch failed", "op", opName, "err", fetchErr)
		return nil
	}
	return current
}

// mergeBefore overlays the fetched state on the caller's fallback, field by
// field, with the fetch winning.
//
// Not "the fetch replaces the fallback": BeforeFields is a curated per-type
// selection, so a caller can legitimately hold a field the fetch never asked
// for — divergence-Accept resolves a field by name at runtime and may name one
// outside the list. Discarding the fallback would silently empty the audit diff
// for exactly those fields. Returns nil when there is nothing on either side, so
// "no before-state" stays distinguishable from "before-state was empty".
func mergeBefore(fallback, fetched map[string]any) map[string]any {
	if len(fallback) == 0 && len(fetched) == 0 {
		return nil
	}
	merged := make(map[string]any, len(fallback)+len(fetched))
	for k, v := range fallback {
		merged[k] = v
	}
	for k, v := range fetched {
		merged[k] = v
	}
	return merged
}

// checkIfVersion is the opt-in MVCC guard. Auto-increment of `version` is
// mandatory and server-managed; this race detection sits on top of it and fires
// only when the caller supplies the version it read.
// See docs/reference/DIVERGENCE.md "MVCC".
//
// current == nil means no single entity resolved, so there is nothing to compare
// and the write proceeds unguarded — the caller asked for a check it did not
// get. Preserved as-is in the 2026-09-03 move rather than quietly hardened;
// tracked in docs/planning/debt.md.
func checkIfVersion(variables map[string]any, current map[string]any) *preflightError {
	ifVersion, hasIfVersion := variables["ifVersion"]
	if !hasIfVersion || current == nil {
		return nil
	}
	want, ok := toFloat64(ifVersion)
	if !ok {
		// A malformed concurrency token is a client error, not a conflict —
		// 409 would tell the caller to reload and retry, but retrying the
		// same garbage loops forever. Reject as bad input. (audit A.3)
		return &preflightError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadUserInput,
			Message: "ifVersion must be an integer",
		}
	}
	cur, _ := toFloat64(current["version"]) // server-stamped, reliably numeric
	if int(cur) != int(want) {
		return &preflightError{
			Status:  http.StatusConflict,
			Code:    CodeMVCCConflict,
			Message: "This record was modified by someone else. Please reload and try again.",
		}
	}
	return nil
}

// stampMutation writes orbital's server-authoritative metadata into the
// mutation variables, in place. Reports whether it changed anything.
//
// Two patterns, both via top-level variables:
//   - UPDATE with a `set` map → inject set.version = current.version + 1 when
//     `set` lacks version. A caller-set version is preserved, which is what
//     lets change-request merge stamp its own without double-incrementing.
//   - ADD with any array-of-maps variable → inject version: 1 into each entry
//     that lacks one. The variable name does not matter (callers use `input`,
//     `idracInput`, …) — every array-of-maps payload is an add/upsert input.
//
// `version` is the OCC counter; updatedBy/updatedAt (and createdBy/createdAt on
// create) are stamped from the authenticated identity and the server clock so
// they are consistent and unspoofable for EVERY caller — orbital's own UI,
// orbctl, direct API clients, and orbital's internal dispatchers alike. Clients
// must NOT supply them. See docs/reference/AUDIT.md.
//
// The UPDATE branch is gated on a resolved current state, not merely on a
// non-nil before: without a current version there is no next version, and
// writing one anyway would reset the counter to 1 and make every open change
// request against the entity read as unchanged.
func stampMutation(req *gqlRequest, current map[string]any, actor string) bool {
	now := time.Now().UTC().Format(time.RFC3339)
	stamped := false

	if current != nil {
		if setMap, ok := req.Variables["set"].(map[string]any); ok {
			if _, has := setMap["version"]; !has {
				if cur, ok := toFloat64(current["version"]); ok {
					setMap["version"] = int(cur) + 1
				}
			}
			setMap["updatedBy"] = actor
			setMap["updatedAt"] = now
			req.Variables["set"] = setMap
			stamped = true
		}
	}

	for _, v := range req.Variables {
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if _, has := m["version"]; !has {
				m["version"] = 1
			}
			m["createdBy"] = actor
			m["createdAt"] = now
			m["updatedBy"] = actor
			m["updatedAt"] = now
			stamped = true
		}
	}

	return stamped
}

// casFilterRe matches the ONE filter shape orbital's write path allows:
// `filter: { orbId: { eq: $orbId } }`, with or without whitespace. Both the
// client's canonical update and the two queries change-request merge builds
// itself use it — merge's delete puts it directly on the field rather than
// inside `input:`, which does not change the target.
var casFilterRe = regexp.MustCompile(`filter:\s*\{\s*orbId:\s*\{\s*eq:\s*\$orbId\s*\}\s*\}`)

// mutationVarsRe finds a mutation's variable-definition list so the injected
// variable can be declared. Anchored on `mutation` so it cannot match a
// selection-set argument list.
var mutationVarsRe = regexp.MustCompile(`(?s)^\s*mutation\b[^(){]*\(([^)]*)\)`)

// injectVersionPredicate moves the version comparison INSIDE the write, making
// the concurrency check a compare-and-swap rather than check-then-act.
//
// FAILS CLOSED: if the predicate cannot be placed, the write is refused rather
// than sent unguarded — a caller that asked for a check and silently did not get
// one is the failure being fixed. That is also what makes the regex acceptable
// here: a miss is a loud refusal, not a quiet bypass.
//
// Requires EXACTLY ONE filter match — zero means an unrecognised shape, more
// than one means other targets would be left unguarded.
//
// checkIfVersion's pre-flight stays; it catches the ordinary stale case with a
// better message. This catches only the race the pre-flight cannot see.
func injectVersionPredicate(req *gqlRequest) *preflightError {
	raw, ok := req.Variables["ifVersion"]
	if !ok {
		return nil // unconditional write — nothing to inject, nothing to refuse
	}

	// Validated here too: checkIfVersion returns early when no current state
	// resolved, so a malformed token can reach this point unchecked.
	want, ok := toFloat64(raw)
	if !ok {
		return &preflightError{
			Status: http.StatusBadRequest, Code: CodeBadUserInput,
			Message: "ifVersion must be an integer",
		}
	}

	refuse := func(why string) *preflightError {
		return &preflightError{
			Status: http.StatusBadRequest, Code: CodeBadUserInput,
			Message: "ifVersion cannot be applied to this mutation, so it was not sent",
			Hint:    why + ` ifVersion guards a single existing entity: update{Kind}($orbId: String!, $set: {Kind}Patch!) { update{Kind}(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }.`,
		}
	}

	if n := len(casFilterRe.FindAllStringIndex(req.Query, -1)); n != 1 {
		if n == 0 {
			return refuse("No `filter: { orbId: { eq: $orbId } }` was found in the mutation.")
		}
		return refuse("The mutation has more than one orbId filter, so guarding one would leave the others unguarded.")
	}
	varDecl := mutationVarsRe.FindStringSubmatchIndex(req.Query)
	if varDecl == nil {
		return refuse("The mutation declares no variables, so `$ifVersion` cannot be declared.")
	}

	// ReplaceAllLiteralString, NOT ReplaceAllString: `$` is a capture-group
	// reference in a Go replacement template, so `$orbId` would expand to the
	// empty group of that name and forward `eq: ` to DGraph.
	q := casFilterRe.ReplaceAllLiteralString(req.Query,
		`filter: { orbId: { eq: $orbId }, version: { eq: $ifVersion } }`)
	// Insert before the closing paren of the variable list. Index 3 is the end
	// of the captured group, i.e. immediately before `)`.
	end := varDecl[3]
	// The replace above shifted everything after the filter; the variable list
	// comes first in a mutation, so its offsets are still valid.
	q = q[:end] + ", $ifVersion: Int!" + q[end:]

	req.Query = q
	req.Variables["ifVersion"] = int(want)
	return nil
}

// casMissed reports whether a version-filtered mutation matched nothing.
//
// Only meaningful when the predicate was injected — an unguarded update that
// legitimately matches nothing must keep returning 200.
//
// Two shapes, because callers select differently: merge asks for `numUids`, the
// editor for the payload. A miss is `numUids: 0` or an empty payload array.
func casMissed(body []byte) bool {
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Data) == 0 {
		return false
	}
	for _, raw := range env.Data {
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
			continue
		}
		if n, present := payload["numUids"]; present {
			if v, ok := toFloat64(n); ok && int(v) == 0 {
				return true
			}
			continue
		}
		for _, v := range payload {
			if arr, ok := v.([]any); ok && len(arr) == 0 {
				return true
			}
		}
	}
	return false
}

// forwardBody returns the body to POST, re-marshalling only when something
// changed: stamping ran, the version predicate was injected, or orbId has to
// come out.
//
// ifVersion is NO LONGER stripped. It used to be an orbital-only variable the
// proxy consumed; now it is either injected into the query — which declares and
// references it — or the write is refused. There is no path where it survives
// unused.
//
// orbId is orbital-only UNLESS the query declares $orbId; stripping it then
// makes DGraph reject the request for an undefined variable. req.Variables is
// left with orbId RESTORED: it is what the caller audits, and extractResourceIDs
// reads the entity id out of it.
func forwardBody(req *gqlRequest, body []byte, changed bool) []byte {
	orbID, _ := req.Variables["orbId"].(string)
	stripOrbID := orbID != "" && !strings.Contains(req.Query, "$orbId")

	if !changed && !stripOrbID {
		return body
	}
	if stripOrbID {
		delete(req.Variables, "orbId")
	}
	if modified, err := json.Marshal(req); err == nil {
		body = modified
	}
	if stripOrbID {
		req.Variables["orbId"] = orbID
	}
	return body
}

// readFromDGraph POSTs a GraphQL READ and returns the raw body and status.
//
// Not part of writeToDGraph's chokepoint and deliberately separate: that
// function exists so every WRITE passes one policy check, and folding reads
// into it would mean either checking reads too or adding a flag that says "skip
// the check" — which is exactly the hole the chokepoint closes.
func (h *GraphQL) readFromDGraph(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build dgraph request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	dgStart := time.Now()
	resp, err := http.DefaultClient.Do(req)
	metrics.ObserveDGraphCall("query", time.Since(dgStart))
	if err != nil {
		return nil, 0, fmt.Errorf("query dgraph: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read dgraph response: %w", err)
	}
	return respBytes, resp.StatusCode, nil
}

// txnAbortRetries bounds the retry above. Three attempts covers the contention
// the predicate introduces without turning a persistent problem into a hang.
const txnAbortRetries = 2

// isTxnAborted reports DGraph's retryable "Transaction has been aborted" — a
// write that lost a commit race and did not apply, as opposed to one that was
// refused.
func isTxnAborted(body []byte) bool {
	if !hasGQLErrors(body) {
		return false
	}
	return strings.Contains(firstGQLError(body), "Transaction has been aborted")
}

// firstGQLError extracts the first error.message from a DGraph response body,
// or returns a generic string when none is parseable.
func firstGQLError(body []byte) string {
	var r struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &r); err != nil || len(r.Errors) == 0 {
		return "graphql error"
	}
	return r.Errors[0].Message
}

func (h *GraphQL) proxyRaw(c echo.Context, body []byte) error {
	dgStart := time.Now()
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	metrics.ObserveDGraphCall("query", time.Since(dgStart))
	if err != nil {
		return fmt.Errorf("proxy to dgraph: %w", err)
	}
	defer resp.Body.Close()
	c.Response().Header().Set("Content-Type", "application/json")
	_, err = io.Copy(c.Response(), resp.Body)
	return err
}

func (h *GraphQL) fetchBeforeByID(getter, resourceType, id string) (map[string]any, error) {
	fields := configitems.BeforeFields(resourceType)
	if fields == "" {
		fields = "id orbId name version"
	}
	query := fmt.Sprintf(`query BeforeFetch($id: ID!) { %s(id: $id) { %s } }`, getter, fields)
	body, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"id": id},
	})
	return h.doFetch(getter, body)
}

func (h *GraphQL) fetchBeforeByOrbID(querier, resourceType, orbID string) (map[string]any, error) {
	fields := configitems.BeforeFields(resourceType)
	if fields == "" {
		fields = "id orbId name version"
	}
	query := fmt.Sprintf(`query BeforeFetch($orbId: String!) { %s(filter: { orbId: { eq: $orbId } }) { %s } }`, querier, fields)
	body, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"orbId": orbID},
	})

	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dgraph fetch: %w", err)
	}
	defer func() { io.Copy(io.Discard, resp.Body); resp.Body.Close() }() //nolint:errcheck

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	data, _ := result["data"].(map[string]any)
	list, _ := data[querier].([]any)
	if len(list) == 0 {
		return nil, fmt.Errorf("entity not found (querier=%s orbId=%s)", querier, orbID)
	}
	entity, _ := list[0].(map[string]any)
	return entity, nil
}

func (h *GraphQL) doFetch(getter string, body []byte) (map[string]any, error) {
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dgraph fetch: %w", err)
	}
	defer func() { io.Copy(io.Discard, resp.Body); resp.Body.Close() }() //nolint:errcheck

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	data, _ := result["data"].(map[string]any)
	entity, _ := data[getter].(map[string]any)
	if entity == nil {
		return nil, fmt.Errorf("entity not found (getter=%s)", getter)
	}
	return entity, nil
}

// auditMutation builds the `details` payload for a GraphQL mutation and hands
// it to writeAuditEvent, which persists the row. Two names because these are
// two acts, not one: this assembles what a mutation-shaped audit record needs
// (query, variables, before-state), writeAuditEvent knows how to store any
// audit record. The old name (`writeEvent`) read as a duplicate of
// `writeAuditEvent` and hid that layering.
func (h *GraphQL) auditMutation(opName string, operations, resourceTypes, resourceIDs []string, actor, query string, variables map[string]any, before map[string]any, bypassedPolicy string) {
	details := map[string]any{
		"operationName": opName,
		"query":         query,
		"variables":     variables,
	}
	// A write that skipped an approval policy is marked in the DURABLE record,
	// not only in the log stream. "Who bypassed review, on what, and when" is a
	// question asked long after the fact — from the audit API, by someone who
	// does not already suspect anything. The policy name is carried rather than
	// a bare boolean so the row says WHICH control was skipped.
	if bypassedPolicy != "" {
		details["privileged"] = true
		details["bypassedPolicy"] = bypassedPolicy
	}
	if before != nil {
		details["before"] = stripDGraphIDs(before)
	}
	writeAuditEvent(h.db, h.logger, "data", actor, opName, operations, resourceTypes, resourceIDs, details)
}

// stripDGraphIDs returns a deep copy of v with every "id" key removed. DGraph
// UIDs are internal and reassigned on restore/reimport, so they must never be
// persisted to the audit event or exposed via the API — clients key on orbId.
// Copy-based so the caller's before-state map is left intact (auditMutation runs in
// a goroutine); recurses into nested maps and arrays.
func stripDGraphIDs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "id" {
				continue
			}
			out[k] = stripDGraphIDs(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripDGraphIDs(val)
		}
		return out
	default:
		return v
	}
}

// extractOperations returns deduplicated DGraph operation names and resource type
// names from all mutation calls in the query body.
// e.g. two updateServer calls → operations: ["updateServer"], types: ["Server"]
//
// Scans only the body (everything after the first `{`), not the operation
// signature — otherwise `mutation UpdateIdracSettings(...) { addIdracSettings(...) }`
// would record both `updateIdracSettings` (from the operation name) and
// `addIdracSettings` (from the body call).
func extractOperations(query string) (operations []string, resourceTypes []string) {
	body := query
	if i := strings.Index(query, "{"); i >= 0 {
		body = query[i:]
	}
	matches := knownMutationRe.FindAllStringSubmatch(body, -1)
	seenOp := map[string]bool{}
	seenType := map[string]bool{}
	for _, m := range matches {
		op := strings.ToLower(m[1]) + m[2] // e.g. "update" + "Server" → "updateServer"
		t := m[2]                          // e.g. "Server"
		if !seenOp[op] {
			seenOp[op] = true
			operations = append(operations, op)
		}
		if !seenType[t] {
			seenType[t] = true
			resourceTypes = append(resourceTypes, t)
		}
	}
	return
}

// extractResourceIDs collects orbIds from three sources, merged and deduplicated:
//  1. mutation variables (single orbId field, or input array for bulk adds)
//  2. inline filter expressions in the query body (orbId: { eq: "..." })
//  3. the DGraph mutation response body — every "orbId" value found anywhere in
//     the returned JSON tree (covers nested creates and any entity the client
//     selected orbId for in the response selection set)
func extractResourceIDs(query string, variables map[string]any, respBody []byte) []string {
	seen := map[string]bool{}
	var ids []string

	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	// Variables: single orbId field (update/delete by orbId)
	if v, ok := variables["orbId"].(string); ok {
		add(v)
	}

	// Variables: input array (bulk add mutations)
	// Each element may carry an orbId field.
	if input, ok := variables["input"]; ok {
		switch v := input.(type) {
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					if id, ok := m["orbId"].(string); ok {
						add(id)
					}
				}
			}
		case map[string]any:
			if id, ok := v["orbId"].(string); ok {
				add(id)
			}
		}
	}

	// Variables: typed filter passed as $filter (DGraph-generated {Type}Filter).
	// Shape: {"filter": {"orbId": {"eq": "..."}}} or {"filter": {"orbId": {"in": [...]}}}.
	// This is the shape used by update{Type}/delete{Type} when the caller wants
	// the audit-log expanded row to show variables instead of inlined values.
	if filter, ok := variables["filter"].(map[string]any); ok {
		if orbIdF, ok := filter["orbId"].(map[string]any); ok {
			if eq, ok := orbIdF["eq"].(string); ok {
				add(eq)
			}
			if in, ok := orbIdF["in"].([]any); ok {
				for _, v := range in {
					if s, ok := v.(string); ok {
						add(s)
					}
				}
			}
		}
	}

	// Inline filter expressions: orbId: { eq: "..." }
	for _, m := range orbIdFilterRe.FindAllStringSubmatch(query, -1) {
		add(m[1])
	}

	// Response body: recursively collect every orbId value in the returned JSON.
	// Covers nested creates and any entity the client selected orbId for.
	if len(respBody) > 0 {
		var respJSON any
		if json.Unmarshal(respBody, &respJSON) == nil {
			collectOrbIDs(respJSON, add)
		}
	}

	sort.Strings(ids)
	return ids
}

// collectOrbIDs recursively walks an arbitrary JSON value and calls add for
// every string value found under an "orbId" key.
func collectOrbIDs(v any, add func(string)) {
	switch node := v.(type) {
	case map[string]any:
		for k, val := range node {
			if k == "orbId" {
				if s, ok := val.(string); ok {
					add(s)
				}
			} else {
				collectOrbIDs(val, add)
			}
		}
	case []any:
		for _, item := range node {
			collectOrbIDs(item, add)
		}
	}
}

// authorizeMutation reports whether the caller may run a GraphQL mutation
// (dev role minimum). Caller shapes are resolved by resolveCallerRole (authz.go);
// dev mode with no authz backend passes.
func (h *GraphQL) authorizeMutation(c echo.Context) (bool, string) {
	cr := resolveCallerRole(c, h.db)
	switch {
	case cr.NoAuthz:
		return true, ""
	case cr.Role == "":
		return false, cr.Reason
	case !RoleAtLeast(cr.Role, user.RoleDev):
		return false, cr.Source + " role " + string(cr.Role) + " below dev"
	}
	return true, ""
}

// isMutation reports whether a GraphQL request body contains a mutation
// operation. It is the security boundary for write authorization: this
// function returning true triggers the dev+ role check in Handle.
//
// Conservative by design: a mutation keyword surviving comment/string
// stripping anywhere in the body returns true, even if a non-mutation
// operation is selected via operationName. False positives (readonly users
// sending bodies that contain a mutation operation but execute a query)
// are acceptable; false negatives are not.
//
// Bypasses guarded against:
//   - Leading # line comments: # foo\nmutation Bar { ... }
//   - String literals containing the word: { field(arg: "mutation") }
//   - Block strings: """mutation"""\nmutation Bar { ... }
//   - Multi-operation requests where the first op is a query
//
// Alternative: github.com/vektah/gqlparser/v2 for a full AST parse — more
// accurate (knows operationName selection, fragments) but slower and adds
// a dependency. Switch if false positives become a real problem.
func isMutation(query string) bool {
	return mutationKeywordRe.MatchString(stripCommentsAndStrings(query))
}

// mutationKeywordRe matches the literal token "mutation" surrounded by
// word boundaries — case-insensitive. Cannot smuggle past via substring
// like "mutationOfThings" (no boundary between 'n' and 'O').
var mutationKeywordRe = regexp.MustCompile(`(?i)\bmutation\b`)

// stripCommentsAndStrings removes GraphQL line comments (#...) and double-
// quoted string literals (both regular "..." and block """...""") from the
// input. The result preserves byte positions for safe regex application.
func stripCommentsAndStrings(query string) string {
	var sb strings.Builder
	sb.Grow(len(query))
	i := 0
	for i < len(query) {
		ch := query[i]
		switch {
		case ch == '#':
			// Line comment: skip to end-of-line, keep the newline so token
			// boundaries on the next line remain intact.
			for i < len(query) && query[i] != '\n' {
				i++
			}
		case ch == '"' && i+2 < len(query) && query[i+1] == '"' && query[i+2] == '"':
			// Block string """..."""
			i += 3
			for i+2 < len(query) && !(query[i] == '"' && query[i+1] == '"' && query[i+2] == '"') {
				i++
			}
			if i+2 < len(query) {
				i += 3 // skip closing """
			} else {
				i = len(query)
			}
		case ch == '"':
			// Single-line string "..."
			i++ // opening quote
			for i < len(query) && query[i] != '"' {
				if query[i] == '\\' && i+1 < len(query) {
					i += 2 // skip escape sequence
					continue
				}
				i++
			}
			if i < len(query) {
				i++ // closing quote
			}
		default:
			sb.WriteByte(ch)
			i++
		}
	}
	return sb.String()
}

func hasGQLErrors(body []byte) bool {
	var r struct {
		Errors []any `json:"errors"`
	}
	return json.Unmarshal(body, &r) == nil && len(r.Errors) > 0
}

// toFloat64 coerces a JSON-decoded numeric value to float64. The second return
// is false when v is not a numeric type (nil, string, bool) or is a json.Number
// that fails to parse. Callers MUST check it — treating a failed parse as 0
// silently passes the MVCC check on a malformed ifVersion (audit A.3).
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
