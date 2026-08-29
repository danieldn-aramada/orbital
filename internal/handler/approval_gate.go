package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/armada/orbital/ent/approvalpolicy"
	"github.com/armada/orbital/internal/approval"
)

// gateMode says whether a write is subject to the approval gate.
//
// An explicit argument rather than a context value, and an enum rather than a
// bool, because `gateExempt` is the one way around a security control: it must
// be spelled out where it is used, findable with a single grep, and impossible
// to acquire by accident from an inherited context.
type gateMode int

const (
	// gateEnforce applies the approval policy. The default for every caller.
	gateEnforce gateMode = iota
	// gateExempt skips it. Legitimate exactly once — merging an already-approved
	// change request, which would otherwise be unable to apply itself.
	gateExempt
)

// gatedError is a refusal from the approval gate, carrying everything the HTTP
// layer needs. A typed error rather than a sentinel because the response has to
// name the policy that refused, and a caller cannot reconstruct that.
type gatedError struct {
	Status  int
	Code    string
	Message string
	Hint    string
	// Policy is "<namespace>" or "<namespace>/<type>" — for logs, not for clients.
	Policy string
}

func (e *gatedError) Error() string { return e.Message }

// checkApprovalPolicy decides whether this mutation may reach the graph.
//
// The order of the escape hatches matters, and each is a deliberate promise:
//
//  1. No policy store (orb runs this same handler with a nil db) — inert.
//  2. Not a ConfigItem mutation — orbital only governs config intent.
//  3. No enabled policy matches — THE OPT-IN PROMISE. Installing this feature
//     changes nothing until an admin declares a protected class.
//  4. Caller's role is in the policy's bypass_roles — a privileged write, allowed
//     and recorded (D15: bypass belongs to the policy, not to the user).
//
// Only after all four does it refuse.
// Returns the label of the policy the caller BYPASSED, or "" when no policy was
// in play. Callers stamp that onto the audit event: a bypass has to be
// queryable after the fact, not merely visible in a log stream someone would
// have to already suspect something to go looking through.
func (h *GraphQL) checkApprovalPolicy(ctx context.Context, body []byte, caller callerRole) (bypassed string, err error) {
	if h.db == nil {
		return "", nil
	}

	var req gqlRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return "", nil // not a shape we can judge; the proxy will reject it downstream
	}
	if !knownMutationRe.MatchString(req.Query) {
		return "", nil
	}

	_, types := extractOperations(req.Query)
	if len(types) == 0 {
		return "", nil
	}

	// respBody is nil on purpose: this runs BEFORE the write, so the only
	// available orbIds are the ones the caller supplied.
	orbIDs := extractResourceIDs(req.Query, req.Variables, nil)
	namespaces := namespacesOf(orbIDs)

	pols, err := h.matchingPolicies(ctx, namespaces, types)
	if err != nil {
		return "", err
	}

	// A mutation on a known type whose namespace we could not determine — the
	// fully-inline `add{Type}(input: [{...}])` shape, which yields a type but no
	// orbId. Refusing only when a policy exists somewhere keeps ungated
	// deployments untouched while closing the bypass for governed ones. See
	// rejectUndeterminable for why this fails closed.
	if len(namespaces) == 0 {
		return "", h.rejectUndeterminable(ctx, types)
	}

	if len(pols) == 0 {
		return "", nil
	}

	// Strictest match wins, same composition as resolvePolicy: a request must not
	// dodge a strict policy by touching a leniently-governed type as well.
	strictest := pols[0]
	bypass := map[string]bool{}
	for _, r := range strictest.BypassRoles {
		bypass[r] = true
	}
	for _, p := range pols[1:] {
		if p.RequiredApprovals > strictest.RequiredApprovals {
			strictest = p
		}
		next := map[string]bool{}
		for _, r := range p.BypassRoles {
			if bypass[r] {
				next[r] = true
			}
		}
		bypass = next
	}

	if caller.NoAuthz {
		return "", nil // no authz backend at all — see callerRole.NoAuthz
	}
	if bypass[string(caller.Role)] {
		// A privileged write. Allowed without friction and recorded as such —
		// D15's whole point is that the frictionless path is the audited one,
		// not a silent one. The label is returned so it lands on the audit
		// event alongside the mutation itself; the log line is for the operator
		// watching in real time, the audit row is for the one asking later.
		label := policyLabel(strictest.Namespace, strictest.TypeName)
		h.logger.Warn("privileged write — bypassed an approval policy",
			"policy", label,
			"role", string(caller.Role),
			"types", strings.Join(types, ","),
			"orb_ids", strings.Join(orbIDs, ","))
		return label, nil
	}

	label := policyLabel(strictest.Namespace, strictest.TypeName)
	return "", &gatedError{
		Status: http.StatusForbidden,
		Code:   CodeApprovalRequired,
		Message: fmt.Sprintf("changes to %s require approval (%d)",
			label, strictest.RequiredApprovals),
		Hint:   "Open a change request: POST /api/v1/change-requests with this change as its changeset.",
		Policy: label,
	}
}

// rejectUndeterminable refuses a mutation whose namespace orbital cannot work
// out, but only where approvals are in use at all.
//
// The shape is a fully inline `add{Type}(input: [{orbId: "…"}])`: the type
// resolves, the orbId does not, so no policy can be looked up — a silent bypass
// of every policy in the system. Failing OPEN would make the gate optional for
// anyone who writes their mutations inline, which is not a control at all.
//
// Failing closed is affordable because nothing in production emits that shape:
// orbital's editor and orbctl both use the variable form, and the only inline
// adds in the tree are tests that POST straight to DGraph. It also mirrors the
// shipped inline-selector guard, which refuses inline updates for the same
// underlying reason — inline literals defeat server-side processing.
func (h *GraphQL) rejectUndeterminable(ctx context.Context, types []string) error {
	n, err := h.db.ApprovalPolicy.Query().
		Where(
			approvalpolicy.ActionTypeEQ(approval.ActionTypeConfigMutation),
			approvalpolicy.EnabledEQ(true),
		).Count(ctx)
	if err != nil {
		return fmt.Errorf("count approval policies: %w", err)
	}
	if n == 0 {
		return nil
	}
	kind := types[0]
	return &gatedError{
		Status:  http.StatusBadRequest,
		Code:    CodeVariableFormRequired,
		Message: fmt.Sprintf("add%s must pass its input as a GraphQL variable, not inline literals: approval policies are in force and orbital resolves the governing policy from the entity's orbId, which it cannot read out of an inline literal.", kind),
		Hint:    fmt.Sprintf(`Rewrite with variables — query: mutation Add%s($input: [Add%sInput!]!) { add%s(input: $input, upsert: true) { numUids } } — variables: { "input": [{ "orbId": "namespace:name", ...fields... }] }`, kind, kind, kind),
		Policy:  "(namespace undeterminable)",
	}
}

// matchingPolicies returns every enabled policy covering any of these
// namespaces, filtered to the types actually touched. A policy with an empty
// type_name governs its whole namespace.
func (h *GraphQL) matchingPolicies(ctx context.Context, namespaces, types []string) ([]*approvalPolicyMatch, error) {
	if len(namespaces) == 0 {
		return nil, nil
	}
	rows, err := h.db.ApprovalPolicy.Query().
		Where(
			approvalpolicy.ActionTypeEQ(approval.ActionTypeConfigMutation),
			approvalpolicy.EnabledEQ(true),
			approvalpolicy.NamespaceIn(namespaces...),
		).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve approval policy: %w", err)
	}

	touched := make(map[string]bool, len(types))
	for _, t := range types {
		touched[t] = true
	}

	var out []*approvalPolicyMatch
	for _, p := range rows {
		if p.TypeName != "" && !touched[p.TypeName] {
			continue
		}
		roles := p.BypassRoles
		if roles == nil {
			roles = []string{}
		}
		out = append(out, &approvalPolicyMatch{
			Namespace:         p.Namespace,
			TypeName:          p.TypeName,
			RequiredApprovals: p.RequiredApprovals,
			BypassRoles:       roles,
		})
	}
	return out, nil
}

// approvalPolicyMatch is a policy reduced to what the gate needs.
type approvalPolicyMatch struct {
	Namespace         string
	TypeName          string
	RequiredApprovals int
	BypassRoles       []string
}

// namespacesOf takes the namespace prefix of each orbId, deduped and sorted.
func namespacesOf(orbIDs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range orbIDs {
		ns, key, found := strings.Cut(id, ":")
		if !found || ns == "" || key == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func policyLabel(namespace, typeName string) string {
	if typeName == "" {
		return namespace
	}
	return namespace + "/" + typeName
}
