package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	entapproval "github.com/armada/orbital/ent/approval"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/graphdiff"
	"github.com/google/uuid"
)

// mergeTarget is what merge needs to know about one item's entity right now:
// whether it exists, its OCC version, and the current values of any fields the
// item clears (DGraph's `remove` matches on VALUE, not on field name).
type mergeTarget struct {
	Exists  bool
	Version int
	Current map[string]any
}

// Merge applies an approved change request to the graph.
//
// Items are applied ONE AT A TIME, and that is a property of DGraph rather than
// a choice: two root fields in one mutation are not atomic (the first commits
// when the second fails), and a nested object under an edge links rather than
// deep-writes, so a multi-entity changeset cannot be expressed as one atomic
// mutation. Both were measured directly.
//
// The consequence — a merge can partly apply — is handled by making a partial
// merge SELF-CORRECTING rather than a new state to reason about:
//
//   - the request stays open; there is no `merge_failed`
//   - a merge_attempt records what each item did
//   - the items that landed moved the base, so the recomputed diff shows exactly
//     the remainder and re-merging is a no-op for what already applied
//   - approvals survive if and only if the base moved by exactly the items this
//     merge applied (see rebaseOrStale)
//
// So a transient failure costs one retry click, while a genuine third-party
// write still forces re-review. Nothing has to be cleaned up by hand.
func (h *ChangeRequest) Merge(ctx context.Context, id uuid.UUID, actor string, role user.Role, noAuthz bool) (*ent.ApprovalRequest, error) {
	cr, err := h.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if cr.Status != approvalrequest.StatusOpen {
		return nil, fmt.Errorf("%w: %s", errCRNotOpen, cr.Status)
	}

	st, err := h.State(ctx, cr)
	if err != nil {
		return nil, err
	}
	// An entity that existed when the base was captured and is gone now is a
	// HARD failure, never a stale-then-recreate. `op: upsert` against a missing
	// orbId would rebuild the entity out of a field delta, producing an object
	// holding only the fields this request happened to touch — a merge that
	// looks successful and quietly corrupts data.
	//
	// Checked before staleness and before the approval count because it is the
	// most specific thing that can be wrong here, and the only one whose remedy
	// is not "review it again".
	if len(st.Missing) > 0 {
		return nil, fmt.Errorf("%w: %s", errCRTargetMissing, strings.Join(st.Missing, ", "))
	}

	if st.Status != approval.StatusApproved {
		// A stale request is usually also unapproved, because its approvals
		// stopped counting the moment the base moved. Report the more
		// actionable of the two: "the intent changed, re-review" tells an
		// operator what to do; "not enough approvals" does not.
		if st.Stale {
			return nil, errCRStale
		}
		return nil, fmt.Errorf("%w: %d of %d", errCRNotApproved, st.Valid, st.Required)
	}
	if !noAuthz && !(cr.Author == actor || approvedBy(st, actor) || roleIn(role, st.BypassRoles)) {
		return nil, errCRForbidden
	}

	// Final MVCC checkpoint: nothing may have moved since the state was last
	// reviewed. For a governed request the approval count has already
	// established that, because approvals are stamped with the hash they were
	// cast against; this catches a write landing between the last approval and
	// this call, and it is the ONLY guard on an ungoverned request.
	if st.Stale {
		return nil, errCRStale
	}

	targets, err := h.fetchMergeTargets(ctx, st.Changeset.Changes)
	if err != nil {
		return nil, fmt.Errorf("read merge targets: %w", err)
	}

	// Carried into every item write even though those writes are gate-exempt:
	// if the exemption is ever narrowed, the identity is already the right one
	// rather than an empty placeholder someone has to notice and fill in.
	caller := callerRole{Role: role, NoAuthz: noAuthz}

	before := st.Snapshot
	applied := map[string]bool{}
	results := make([]approval.ItemResult, 0, len(st.Changeset.Changes))
	var failure error

	for _, item := range st.Changeset.Changes {
		// Fail fast rather than skipping ahead: items may depend on order (a
		// later item can reference an entity an earlier one creates), so
		// continuing past a failure produces a cascade of errors that say
		// nothing about the original cause.
		if failure != nil {
			break
		}
		err := h.applyItem(ctx, actor, caller, cr.ID, item, targets[item.OrbID])
		results = append(results, approval.ItemResult{
			OrbID:   item.OrbID,
			Applied: err == nil,
			Error:   errText(err),
		})
		if err != nil {
			failure = fmt.Errorf("%s: %w", item.OrbID, err)
			continue
		}
		applied[item.OrbID] = true
	}

	if err := h.recordAttempt(ctx, cr.ID, actor, results, failure); err != nil {
		h.logger.Error("record merge attempt", "change_request", cr.ID, "err", err)
	}

	if failure == nil {
		if _, err := cr.Update().
			SetStatus(approvalrequest.StatusMerged).
			SetExecutedAt(time.Now()).
			SetExecutedBy(actor).
			SetUpdatedAt(time.Now()).
			SetUpdatedBy(actor).
			Save(ctx); err != nil {
			return nil, fmt.Errorf("mark merged: %w", err)
		}
		return h.Get(ctx, id)
	}

	h.rebaseOrStale(ctx, cr, st, before, applied)
	return nil, fmt.Errorf("merge applied %d of %d items: %w", len(applied), len(st.Changeset.Changes), failure)
}

// rebaseOrStale decides whether the approvals on a partly-merged request
// survive.
//
// The rule: approvals survive if and only if the base moved by exactly the
// items this merge applied. If orbital caused every difference, the reviewers'
// judgement still holds — the remainder is a strict subset of what they
// approved — so the base is rebased onto the new state and the approvals are
// re-stamped with it. If anything ELSE changed, a third party wrote to a
// covered entity and the request goes stale by construction: the base is left
// where it was, so the next read recomputes a different hash and the approvals
// stop counting.
//
// This is what makes retry-after-transient-failure free while keeping
// retry-after-someone-else-edited gated. A network error mid-merge should not
// cost a re-approval; a colleague changing the same server must.
//
// Conservative in one case, deliberately: writing an edge also updates the
// inverse edge on its target, so if the target is itself in scope it shows as
// an unapplied change and the request goes stale. Rare (both ends must be in
// the same changeset), and the cost is one extra approval — the safe direction.
func (h *ChangeRequest) rebaseOrStale(ctx context.Context, cr *ent.ApprovalRequest, st crState, before graphdiff.Snapshot, applied map[string]bool) {
	after, err := baseSnapshot(ctx, h.dgraphURL, st.Scope)
	if err != nil {
		h.logger.Warn("rebase check: re-read failed, leaving base as-is",
			"change_request", cr.ID, "err", err)
		return
	}
	for _, ch := range graphdiff.Compare(before, after).Changes {
		if !applied[ch.OrbID] {
			h.logger.Info("partial merge: base moved by something we did not apply — approvals will not carry",
				"change_request", cr.ID, "orb_id", ch.OrbID)
			return
		}
	}

	newHash := after.ContentHash()
	if newHash == cr.BaseHash {
		return // nothing actually changed; approvals already valid
	}
	if _, err := cr.Update().SetBaseHash(newHash).Save(ctx); err != nil {
		h.logger.Error("rebase base hash", "change_request", cr.ID, "err", err)
		return
	}
	// Carry forward only the approvals that were valid a moment ago. One cast
	// against an older hash stays stale — rebasing is not an amnesty.
	if _, err := h.db.Approval.Update().
		Where(
			entapproval.ApprovalRequestID(cr.ID),
			entapproval.ApprovedAtHashEQ(st.CurrentHash),
		).
		SetApprovedAtHash(newHash).
		Save(ctx); err != nil {
		h.logger.Error("rebase approvals", "change_request", cr.ID, "err", err)
	}
}

// applyItem writes one change item.
//
// Every write goes through DispatchMutation, which means it lands in the audit
// log exactly like a user-driven mutation and goes through the same single
// DGraph-write function everything else uses. A merge is not a privileged
// side-channel into the graph; it is an ordinary write with a change request
// behind it.
func (h *ChangeRequest) applyItem(ctx context.Context, actor string, caller callerRole, crID uuid.UUID, item approval.ChangeItem, target mergeTarget) error {
	now := time.Now().UTC().Format(time.RFC3339)

	// gateExempt, and this is the ONE place it is legitimate.
	//
	// The approval gate lives in writeToDGraph, which is where these mutations
	// land. Enforcing it here would mean an approved change request cannot apply
	// itself: the merge is refused for lacking the approval it already has, and
	// the request is permanently unmergeable by anyone outside bypass_roles.
	// Circular by construction.
	//
	// Exempting is correct rather than merely convenient — the gate exists to
	// ensure a human approved this change, and reaching this line is the proof
	// that one did. Merge re-checks the approval count and the MVCC guard
	// immediately before getting here.
	const gate = gateExempt

	switch {
	case item.Op == approval.OpDelete && !target.Exists:
		return nil // deleting what is not there is an idempotent success

	case item.Op == approval.OpDelete:
		query := fmt.Sprintf(`mutation Delete%s($orbId: String!) { delete%s(filter: {orbId: {eq: $orbId}}) { numUids } }`,
			item.Type, item.Type)
		_, err := h.gql.DispatchMutation(ctx, actor, caller, gate, query,
			map[string]any{"orbId": item.OrbID}, nil)
		return err

	case !target.Exists:
		// A create. `add(upsert: true)` rather than plain add so a retry after a
		// partial merge is idempotent instead of a duplicate-key failure.
		input := map[string]any{}
		for k, v := range item.Set {
			input[k] = v
		}
		ns, _, _ := strings.Cut(item.OrbID, ":")
		input["orbId"] = item.OrbID
		input["namespace"] = ns
		input["version"] = 1
		input["createdBy"] = actor
		input["createdAt"] = now
		input["updatedBy"] = actor
		input["updatedAt"] = now

		query := fmt.Sprintf(`mutation Add%s($input: [Add%sInput!]!) { add%s(input: $input, upsert: true) { numUids } }`,
			item.Type, item.Type, item.Type)
		_, err := h.gql.DispatchMutation(ctx, actor, caller, gate, query,
			map[string]any{"input": []any{input}}, nil)
		return err

	default:
		// An update. Uses update{Type} even when the item says `upsert`,
		// because add(upsert) has no way to REMOVE a field — and orbital's
		// clear semantics need one: setting a field to null is a no-op in
		// DGraph, so clearing requires `remove` with the field's exact current
		// value.
		set := map[string]any{}
		for k, v := range item.Set {
			set[k] = v
		}
		set["version"] = target.Version + 1
		set["updatedBy"] = actor
		set["updatedAt"] = now

		remove := map[string]any{}
		for _, f := range item.Clear {
			cur, ok := target.Current[f]
			if !ok || cur == nil {
				continue // already unset
			}
			remove[f] = cur
		}

		vars := map[string]any{"orbId": item.OrbID, "set": set}
		mutationBody := "set: $set"
		decl := fmt.Sprintf("$orbId: String!, $set: %sPatch!", item.Type)
		if len(remove) > 0 {
			vars["remove"] = remove
			mutationBody += ", remove: $remove"
			decl += fmt.Sprintf(", $remove: %sPatch!", item.Type)
		}
		query := fmt.Sprintf(`mutation Update%s(%s) { update%s(input: {filter: {orbId: {eq: $orbId}}, %s}) { numUids } }`,
			item.Type, decl, item.Type, mutationBody)

		before := map[string]any{"version": target.Version}
		for k, v := range target.Current {
			before[k] = v
		}
		_, err := h.gql.DispatchMutation(ctx, actor, caller, gate, query, vars, before)
		return err
	}
}

// fetchMergeTargets reads the version and to-be-cleared field values for every
// item in one aliased GraphQL round-trip.
//
// The clear values are the reason this exists: DGraph's `remove` matches on the
// field's VALUE, not its name, so clearing a field requires knowing what is
// currently in it. Reading them here — immediately before the write, in the
// same request the MVCC check just passed — keeps that window as small as it
// can be.
func (h *ChangeRequest) fetchMergeTargets(ctx context.Context, items []approval.ChangeItem) (map[string]mergeTarget, error) {
	out := make(map[string]mergeTarget, len(items))
	if len(items) == 0 {
		return out, nil
	}

	var b strings.Builder
	b.WriteString("{")
	for i, item := range items {
		fields := map[string]bool{"version": true}
		for _, f := range item.Clear {
			fields[f] = true
		}
		names := make([]string, 0, len(fields))
		for f := range fields {
			names = append(names, f)
		}
		sort.Strings(names)
		orbID, err := json.Marshal(item.OrbID)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, " i%d: get%s(orbId: %s) { %s }", i, item.Type, orbID, strings.Join(names, " "))
	}
	b.WriteString(" }")

	body, err := json.Marshal(map[string]any{"query": b.String()})
	if err != nil {
		return nil, err
	}
	respBytes, status, err := h.gql.readFromDGraph(ctx, body)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("dgraph returned %d", status)
	}

	var env struct {
		Data map[string]map[string]any `json:"data"`
	}
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return nil, fmt.Errorf("decode merge targets: %w", err)
	}
	for i, item := range items {
		node := env.Data[fmt.Sprintf("i%d", i)]
		if node == nil {
			out[item.OrbID] = mergeTarget{}
			continue
		}
		t := mergeTarget{Exists: true, Current: map[string]any{}}
		for k, v := range node {
			if k == "version" {
				if f, ok := toFloat64(v); ok {
					t.Version = int(f)
				}
				continue
			}
			t.Current[k] = v
		}
		out[item.OrbID] = t
	}
	return out, nil
}

func (h *ChangeRequest) recordAttempt(ctx context.Context, crID uuid.UUID, actor string, results []approval.ItemResult, failure error) error {
	raw, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}
	return h.db.MergeAttempt.Create().
		SetApprovalRequestID(crID).
		SetAttemptedBy(actor).
		SetCreatedBy(actor).
		SetResults(raw).
		SetError(errText(failure)).
		Exec(ctx)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
