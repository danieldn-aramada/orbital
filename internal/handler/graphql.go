package handler

import (
	"bytes"
	"encoding/json"
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

// singleEntityTypes maps DGraph getter names to resource type labels, used for
// best-effort resource_id extraction on single-entity mutations.
var singleEntityTypes = map[string]string{
	"UpdateDataCenter":        "DataCenter",
	"UpdateServer":            "Server",
	"UpdateServerAndIdrac":    "Server",
	"UpdateKubernetesCluster": "KubernetesCluster",
	"UpdateEksaConfig":        "EksaConfig",
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

	actor := actorFromContext(c)

	// Fetch before-state for all known single-entity mutations (used for MVCC and audit diff).
	var before map[string]any
	resourceType, isSingleEntity := singleEntityTypes[opName]
	if isSingleEntity {
		getter := "get" + resourceType
		entityID, _ := req.Variables["id"].(string)
		orbID, _ := req.Variables["orbId"].(string)

		var fetchErr error
		if entityID != "" {
			before, fetchErr = h.fetchBeforeByID(getter, resourceType, entityID)
		} else if orbID != "" {
			before, fetchErr = h.fetchBeforeByOrbID("query"+resourceType, resourceType, orbID)
		}
		if fetchErr != nil {
			h.logger.Warn("before-fetch failed", "op", opName, "err", fetchErr)
			before = nil
		}
	}

	// MVCC check — opt-in via ifVersion variable
	ifVersion, hasIfVersion := req.Variables["ifVersion"]
	if hasIfVersion && before != nil {
		if int(toFloat64(before["version"])) != int(toFloat64(ifVersion)) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error": "This record was modified by someone else. Please reload and try again.",
			})
		}
	}

	// Strip orbital-meta variables before forwarding to DGraph.
	// ifVersion is always orbital-only (MVCC). orbId is orbital-only unless the
	// query itself declares $orbId as a variable — in that case DGraph needs it.
	auditOrbID, _ := req.Variables["orbId"].(string)
	orbIdIsQueryVar := strings.Contains(req.Query, "$orbId")
	shouldStripOrbID := auditOrbID != "" && !orbIdIsQueryVar
	needsReMarshal := hasIfVersion || shouldStripOrbID
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
	_, err = c.Response().Writer.Write(respBytes)
	return err
}

func (h *GraphQL) proxyRaw(c echo.Context, body []byte) error {
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("proxy to dgraph: %w", err)
	}
	defer resp.Body.Close()
	c.Response().Header().Set("Content-Type", "application/json")
	_, err = io.Copy(c.Response().Writer, resp.Body)
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
func extractOperations(query string) (operations []string, resourceTypes []string) {
	matches := knownMutationRe.FindAllStringSubmatch(query, -1)
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
