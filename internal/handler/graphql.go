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

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/labstack/echo/v4"
)

// knownMutationRe matches any DGraph mutation call on a known ConfigItem type.
// Catches addX, updateX, deleteX for all registered types regardless of operation name.
var knownMutationRe = regexp.MustCompile(`(?i)\b(add|update|delete)(DataCenter|Server|IdracSettings|KubernetesCluster|EksaConfig|IPAddress|Rack)\b`)

// orbIdFilterRe extracts orbId values from inline GraphQL filter expressions:
// e.g. filter: { orbId: { eq: "alaska-dot:GRTLY24" } }
var orbIdFilterRe = regexp.MustCompile(`orbId\s*:\s*\{\s*eq\s*:\s*"([^"]+)"`)

var mutationOpRe = regexp.MustCompile(`(?i)^\s*mutation\s+(\w+)`)
var queryOpRe = regexp.MustCompile(`(?i)^\s*query\s+(\w+)`)

// beforeFetchOverrides maps an operation name to the resource type whose
// `before` snapshot should be fetched. ONLY needed for compound or nested
// mutations where the body's resource type is not the one we want to diff
// against — e.g. iDRAC-only edits call addIdracSettings but the diff source
// is the parent Server (idracSettings is nested in the Server snapshot).
//
// The default path (no override) derives the resource type from the mutation
// body via extractOperations and fetches when variables carry `id` or `orbId`.
// New simple `update{Type}($id, $set)` flows therefore audit with no edits
// here — the override map shrinks to the entries that are genuine exceptions.
var beforeFetchOverrides = map[string]string{
	"UpdateServerAndIdrac": "Server", // body touches Server + IdracSettings; diff source is Server (idrac nested in snapshot).
	"UpdateIdracSettings":  "Server", // body touches only IdracSettings; idrac variables carry no fetchable orbId — fetch parent Server, event.go's nested-idrac block consumes it.
}

// typeBeforeFields lists the DGraph fields to fetch in before-snapshots per type.
var typeBeforeFields = map[string]string{
	"DataCenter":        "id orbId name version assetDataV2",
	"Server":            "id orbId name version hostname model manufacturer serviceTag rackPosition oobMAC idracSettings { firmwareVersion sshEnabled ipmiEnabled lockdownModeEnabled osToIdracPassThroughEnabled usbManagementPortEnabled dhcpEnabled racadmEnabled }",
	"KubernetesCluster": "id orbId name version provider",
	"EksaConfig":        "id orbId name version clusterType",
}

type GraphQL struct {
	dgraphURL string
	db        *ent.Client
	logger    *slog.Logger
}

func NewGraphQL(dgraphURL string, db *ent.Client, logger *slog.Logger) *GraphQL {
	return &GraphQL{dgraphURL: dgraphURL, db: db, logger: logger}
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
	if h.db != nil {
		userID, _ := c.Get("user_id").(int)
		if userID == 0 {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "dev or admin role required for mutations"})
		}
		u, err := h.db.User.Get(c.Request().Context(), userID)
		if err != nil || !RoleAtLeast(u.Role, user.RoleDev) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "dev or admin role required for mutations"})
		}
	}

	touchesKnownType := knownMutationRe.MatchString(req.Query)

	opName := req.OperationName
	if opName == "" {
		if m := mutationOpRe.FindStringSubmatch(req.Query); len(m) > 1 {
			opName = m[1]
		}
	}
	if opName == "" {
		ops, _ := extractOperations(req.Query)
		opName = strings.Join(ops, ",")
	}
	c.Set("graphql.operation.name", opName)
	c.Set("graphql.operation.type", "mutation")

	actor := actorFromContext(c)

	// Fetch before-state for any single-entity mutation (used for MVCC and audit diff).
	//
	// Default rule: derive the resource type from the mutation body. If exactly
	// one ConfigItem type is touched AND variables carry `id` or `orbId`, fetch
	// before. Override map kicks in only when the diff source differs from the
	// body's resource type (compound/nested cases — see beforeFetchOverrides).
	var before map[string]any
	resourceType, hasOverride := beforeFetchOverrides[opName]
	if !hasOverride {
		_, resourceTypes := extractOperations(req.Query)
		if len(resourceTypes) == 1 {
			resourceType = resourceTypes[0]
		}
	}
	if resourceType != "" {
		entityID, _ := req.Variables["id"].(string)
		orbID, _ := req.Variables["orbId"].(string)

		var fetchErr error
		if entityID != "" {
			before, fetchErr = h.fetchBeforeByID("get"+resourceType, resourceType, entityID)
		} else if orbID != "" {
			before, fetchErr = h.fetchBeforeByOrbID("query"+resourceType, resourceType, orbID)
		}
		if fetchErr != nil {
			h.logger.Warn("before-fetch failed", "op", opName, "err", fetchErr)
			before = nil
		}
	}

	// MVCC check — opt-in via ifVersion variable. Auto-increment of `version`
	// below is mandatory and server-managed; MVCC race detection here is the
	// opt-in layer on top. See docs/reference/DIVERGENCE.md "MVCC" section.
	ifVersion, hasIfVersion := req.Variables["ifVersion"]
	if hasIfVersion && before != nil {
		if int(toFloat64(before["version"])) != int(toFloat64(ifVersion)) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error": "This record was modified by someone else. Please reload and try again.",
			})
		}
	}

	// Auto-increment version. Two patterns handled, both via top-level variables:
	//   - UPDATE with `set` map variable → inject `set.version = before.version + 1`
	//     when set lacks version. Skipped if caller explicitly set it.
	//   - ADD with any array-of-maps variable → inject `version: 1` into each
	//     entry that lacks one. The variable name doesn't matter (callers use
	//     `input`, `idracInput`, etc.) — every array-of-maps payload is treated
	//     as an add/upsert input. Caller-set version is preserved.
	autoIncremented := false
	if before != nil {
		if setMap, ok := req.Variables["set"].(map[string]any); ok {
			if _, has := setMap["version"]; !has {
				setMap["version"] = int(toFloat64(before["version"])) + 1
				req.Variables["set"] = setMap
				autoIncremented = true
			}
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
				autoIncremented = true
			}
		}
	}

	// Strip orbital-meta variables before forwarding to DGraph.
	// ifVersion is always orbital-only (MVCC). orbId is orbital-only unless the
	// query itself declares $orbId as a variable — in that case DGraph needs it.
	auditOrbID, _ := req.Variables["orbId"].(string)
	orbIdIsQueryVar := strings.Contains(req.Query, "$orbId")
	shouldStripOrbID := auditOrbID != "" && !orbIdIsQueryVar
	needsReMarshal := hasIfVersion || shouldStripOrbID || autoIncremented
	if hasIfVersion {
		delete(req.Variables, "ifVersion")
	}
	if shouldStripOrbID {
		delete(req.Variables, "orbId")
	}
	if needsReMarshal {
		if modified, err := json.Marshal(req); err == nil {
			bodyBytes = modified
		}
		// Restore orbId so extractResourceIDs can find it after the DGraph call
		if shouldStripOrbID {
			req.Variables["orbId"] = auditOrbID
		}
	}

	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("proxy to dgraph: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read dgraph response: %w", err)
	}

	if touchesKnownType && h.db != nil && !hasGQLErrors(respBytes) {
		operations, resourceTypes := extractOperations(req.Query)
		resourceIDs := extractResourceIDs(req.Query, req.Variables, respBytes)
		go h.writeEvent(opName, operations, resourceTypes, resourceIDs, actor, req.Query, req.Variables, before)
	}

	c.Response().Header().Set("Content-Type", "application/json")
	_, err = c.Response().Write(respBytes)
	return err
}

// DispatchMutation runs a server-internal GraphQL mutation against DGraph.
//
// Use it when an orbital-internal action (e.g. accepting a divergence override)
// needs to mutate intent and have the change appear in the audit log just like
// a user-driven mutation. It does NOT enforce role gating — callers must have
// already authz'd. It does NOT perform MVCC checks or auto before-fetches;
// callers that want a diff in the audit row must supply `before` directly
// (e.g. the divergence-accept path passes the entry's intended_value).
//
// Returns the raw DGraph response body alongside the error so callers can
// inspect specific GraphQL errors when needed. A non-nil error means the
// mutation did not succeed and any side-effects (e.g. recording a resolution)
// MUST be skipped.
func (h *GraphQL) DispatchMutation(ctx context.Context, actor, query string, variables map[string]any, before map[string]any) ([]byte, error) {
	body, err := json.Marshal(gqlRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, fmt.Errorf("marshal mutation: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post dgraph: %w", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return respBytes, fmt.Errorf("dgraph returned %d", resp.StatusCode)
	}
	if hasGQLErrors(respBytes) {
		return respBytes, errors.New(firstGQLError(respBytes))
	}
	if h.db != nil {
		opName := ""
		if m := mutationOpRe.FindStringSubmatch(query); len(m) > 1 {
			opName = m[1]
		}
		operations, resourceTypes := extractOperations(query)
		resourceIDs := extractResourceIDs(query, variables, respBytes)
		go h.writeEvent(opName, operations, resourceTypes, resourceIDs, actor, query, variables, before)
	}
	return respBytes, nil
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
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("proxy to dgraph: %w", err)
	}
	defer resp.Body.Close()
	c.Response().Header().Set("Content-Type", "application/json")
	_, err = io.Copy(c.Response(), resp.Body)
	return err
}

func (h *GraphQL) fetchBeforeByID(getter, resourceType, id string) (map[string]any, error) {
	fields := typeBeforeFields[resourceType]
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
	fields := typeBeforeFields[resourceType]
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

func (h *GraphQL) writeEvent(opName string, operations, resourceTypes, resourceIDs []string, actor, query string, variables map[string]any, before map[string]any) {
	details := map[string]any{
		"operationName": opName,
		"query":         query,
		"variables":     variables,
	}
	if before != nil {
		details["before"] = before
	}
	writeAuditEvent(h.db, h.logger, "data", actor, opName, operations, resourceTypes, resourceIDs, details)
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
		t := m[2]                           // e.g. "Server"
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

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
