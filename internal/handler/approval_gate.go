package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/armada/orbital/ent"
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
	// Feature switched off entirely: policies may still sit in the database, and
	// none of them applies. Checked first and before any work.
	if !changeControlEnabled {
		return "", nil
	}
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

	return h.checkPolicyFor(ctx, orbIDs, types, caller)
}

// checkPolicyFor is the policy decision with no mutation body in sight.
//
// Extracted 2026-09-03 so the cascade-delete endpoint can ask the same question.
// That path never had a body to parse — it plans a cascade over N entities and
// several types, then POSTs a DQL delete — so it walked straight past a check
// built around `req.Query`, and `DELETE` succeeded on an entity whose `update`
// was refused seconds earlier. A control with a shape that only one caller can
// satisfy is a control with a hole in it.
//
// Same rules, same order, same return contract as checkApprovalPolicy: the
// label of the policy the caller BYPASSED, or "" when none was in play.
func (h *GraphQL) checkPolicyFor(ctx context.Context, orbIDs, types []string, caller callerRole) (bypassed string, err error) {
	if !changeControlEnabled || h.db == nil || len(types) == 0 {
		return "", nil
	}
	namespaces := namespacesOf(orbIDs)

	pol, err := h.matchingPolicy(ctx, namespaces, types)
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

	if pol == nil {
		return "", nil
	}

	if caller.NoAuthz {
		return "", nil // no authz backend at all — see callerRole.NoAuthz
	}
	if slices.Contains(pol.BypassRoles, string(caller.Role)) {
		// A privileged write. Allowed without friction and recorded as such —
		// D15's whole point is that the frictionless path is the audited one,
		// not a silent one. The label is returned so it lands on the audit
		// event alongside the mutation itself.
		h.logger.Warn("privileged write — bypassed an approval policy",
			"policy", policyLabel(pol),
			"role", string(caller.Role),
			"types", strings.Join(types, ","),
			"orb_ids", strings.Join(orbIDs, ","))
		return policyLabel(pol), nil
	}

	return "", &gatedError{
		Status: http.StatusForbidden,
		Code:   CodeApprovalRequired,
		Message: fmt.Sprintf("changes to %s require approval (%d)",
			policyLabel(pol), pol.RequiredApprovals),
		Hint:   "Open a change request: POST /api/v1/change-requests with this change as its changeset.",
		Policy: policyLabel(pol),
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
// orbital's editor and orbctl both use the variable form. It also mirrors the
// shipped inline-selector guard, which refuses inline updates for the same
// underlying reason — inline literals defeat server-side processing.
//
// It DOES catch test fixtures that took the shortcut: cluster-delete.spec.ts
// seeded an inline `addEksaKubernetesCluster` through /graphql and passed only
// while the dev stack had no policies, then failed the moment a developer
// configured one. That is the guard working. Write fixtures in the variable
// form like every real client does.
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

// matchingPolicy returns the one enabled policy governing this mutation, or nil.
//
// ONE, not a set. Policies are unique per namespace and carry their own type
// list, so there is nothing to compose — no max(), no intersection, and no
// composed outcome that neither policy stated. It also means "why was this
// gated?" has exactly one answer, which is what lets the UI name it.
//
// A mutation spanning several namespaces takes the first policy in namespace
// order. That shape does not occur through orbital's own clients — a changeset
// is single-namespace and the editor dispatches per entity — but "first row
// returned" would make a SECURITY control's answer depend on Postgres's row
// order, which is unordered by definition. Sorting costs nothing at this size
// and makes the outcome reproducible, which is the minimum a gate owes anyone
// asking why a write was refused.
func (h *GraphQL) matchingPolicy(ctx context.Context, namespaces, types []string) (*ent.ApprovalPolicy, error) {
	if len(namespaces) == 0 {
		return nil, nil
	}
	// One resolution rule, shared with the change-request engine — see
	// governingPolicy. A mutation may touch several namespaces, so each is
	// resolved separately and the first governing answer wins; namespacesOf
	// returns them sorted, so which one that is stays deterministic.
	for _, ns := range namespaces {
		p, err := governingPolicy(ctx, h.db, approval.ActionTypeConfigMutation, ns)
		if err != nil {
			return nil, err
		}
		if p == nil {
			continue
		}
		// AllTypes matches anything, including ConfigItem types that did not
		// exist when the policy was written.
		if p.AllTypes {
			return p, nil
		}
		for _, t := range types {
			if slices.Contains(p.Types, t) {
				return p, nil
			}
		}
	}
	return nil, nil
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
