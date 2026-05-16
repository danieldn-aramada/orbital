package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/armada/orbital/ent"
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
		return c.File("web/static/graphiql.html")
	}

	bodyBytes, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	var req gqlRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil || !isMutation(req.Query) {
		return h.proxyRaw(c, bodyBytes)
	}

	touchesKnownType := knownMutationRe.MatchString(req.Query)

	opName := req.OperationName
	if opName == "" {
		if m := mutationOpRe.FindStringSubmatch(req.Query); len(m) > 1 {
			opName = m[1]
		}
	}

	var actor string
	if v, ok := req.Variables["updatedBy"]; ok {
		actor, _ = v.(string)
	}
	if actor == "" {
		actor, _ = c.Get("user_name").(string)
	}

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

	// Strip non-DGraph variables before forwarding
	auditOrbID, _ := req.Variables["orbId"].(string)
	needsReMarshal := hasIfVersion || auditOrbID != ""
	if hasIfVersion {
		delete(req.Variables, "ifVersion")
	}
	if auditOrbID != "" {
		delete(req.Variables, "orbId")
	}
	if needsReMarshal {
		if modified, err := json.Marshal(req); err == nil {
			bodyBytes = modified
		}
		// Restore orbId so extractResourceIDs can find it after the DGraph call
		if auditOrbID != "" {
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
	writeAuditEvent(h.db, h.logger, actor, opName, operations, resourceTypes, resourceIDs, details)
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

func isMutation(query string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(query)), "mutation")
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
