package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/armada/orbital/ent"
	entapproval "github.com/armada/orbital/ent/approval"
	"slices"

	"github.com/armada/orbital/ent/approvalpolicy"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/graphdiff"
)

// ChangeRequest is the config.mutation facade over the generic approval engine
// (spike 36 §16): the engine owns the lifecycle, this type owns everything
// specific to changing ConfigItems — the changeset payload, the graph-content
// staleness token, and merge.
type ChangeRequest struct {
	db        *ent.Client
	gql       *GraphQL
	dgraphURL string
	schema    approval.SchemaSource
	logger    *slog.Logger
}

func NewChangeRequest(db *ent.Client, gql *GraphQL, dgraphURL string, logger *slog.Logger) *ChangeRequest {
	if logger == nil {
		logger = slog.Default()
	}
	return &ChangeRequest{
		db:        db,
		gql:       gql,
		dgraphURL: dgraphURL,
		schema:    approval.NewDGraphSchemaSource(dgraphURL),
		logger:    logger,
	}
}

// Sentinel errors the REST layer maps to status codes. Kept as values rather
// than inline strings so the mapping lives in one place and a new call site
// cannot invent a status.
var (
	errCRNotFound      = errors.New("change request not found")
	errCRNotOpen       = errors.New("change request is not open")
	errCRNotApproved   = errors.New("change request does not have the required approvals")
	errCRSelfApproval  = errors.New("a change request cannot be approved by its author")
	errCRStale         = errors.New("the intent this change request was written against has changed")
	errCRTargetMissing = errors.New("an entity this change request targets no longer exists")
	errCRForbidden     = errors.New("caller may not perform this action")
)

// crState is everything derived about a change request at one instant. Nothing
// here is stored; every field is recomputed on each read and each write, which
// is what makes staleness and approval validity impossible to get out of sync
// with the graph (D13).
type crState struct {
	Changeset approval.Changeset
	// Scope is the declared orbIds plus their owned subtrees — what the hash covers.
	Scope []string
	// Snapshot is the scope's full content. Populated ONLY by StateWithSnapshot
	// — reading it costs a subtree fetch per request, which is why the render
	// path does not.
	Snapshot graphdiff.Snapshot
	// Versions is the scope's OCC version vector, and the thing CurrentHash is
	// computed from. Always populated.
	Versions map[string]int
	// CurrentHash hashes Scope's version vector right now. Stale is simply
	// CurrentHash != the hash captured at open.
	CurrentHash string
	Stale       bool
	// Approvals is every decision cast, newest last. Valid counts only the
	// approvals whose hash still matches — the rest are shown as "approved an
	// earlier version" rather than silently disappearing.
	Approvals []*ent.Approval
	Valid     int
	Rejected  int
	Required  int
	// BypassRoles are the roles that may write this class of change directly.
	// A caller in this set also skips the proposer != approver rule, since
	// requiring a second pair of eyes from someone who could have bypassed the
	// gate entirely is friction with no security value (D15).
	BypassRoles []string
	// Status is the EFFECTIVE status: the stored value, except that an open
	// request with enough valid approvals reads as approved.
	Status string
	// Missing are orbIds present when the base was captured but absent now.
	// A hard failure at merge, never a silent recreate (D13).
	Missing []string
}

// Create validates a changeset, captures the base it was written against, and
// opens the request.
//
// Validation happens HERE rather than at merge so a proposal that could never
// apply — a misspelled field, a dangling reference, a create with no type —
// never reaches a reviewer. The reviewer's attention is the scarce resource
// this whole feature spends; spending it on a request that will fail anyway is
// the worst outcome available.
func (h *ChangeRequest) Create(ctx context.Context, actor, title, description string, cs *approval.Changeset) (*ent.ApprovalRequest, []approval.ValidationError, error) {
	res, err := approval.Validate(ctx, h.schema, cs)
	if err != nil {
		return nil, nil, fmt.Errorf("validate changeset: %w", err)
	}
	if len(res.Errors) > 0 {
		return nil, res.Errors, nil
	}

	existing, err := h.schema.ResolveEntities(ctx, declaredOrbIDs(cs))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve entities: %w", err)
	}
	scope := baseScope(ctx, h.dgraphURL, declaredOrbIDs(cs), existing)
	versions, err := scopeVersions(ctx, h.dgraphURL, scope)
	if err != nil {
		return nil, nil, fmt.Errorf("capture base: %w", err)
	}

	// Validate normalized every item's type in place, so what is persisted
	// always names its types even when the caller omitted them (D12).
	payload, err := json.Marshal(cs)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal changeset: %w", err)
	}

	// One snapshot, three consumers: the effect summary, the stored ancestor, and
	// the caller's `before` assertions. Fetching it once also means all three
	// describe the same instant.
	snap, snapErr := baseSnapshot(ctx, h.dgraphURL, scope)
	if snapErr != nil {
		// A conditional request cannot be honoured without reading state, so this
		// is fatal when `before` was supplied and merely a lost convenience
		// otherwise. Do NOT downgrade a guarantee to best-effort.
		if changesetAsserts(*cs) {
			return nil, nil, fmt.Errorf("read state for `before` assertions: %w", snapErr)
		}
		h.logger.Warn("could not snapshot base; effect summary and ancestor omitted",
			"namespace", cs.Namespace, "err", snapErr)
	}

	if snap != nil {
		if bad := beforeMismatches(snap, *cs); len(bad) > 0 {
			return nil, nil, &preconditionFailed{Problems: bad}
		}
	}

	// The delta this request would apply, captured with the anchor that says
	// when it stops being true. Best-effort by design — a nil effect falls back
	// to counting the changeset, and losing a display convenience must never
	// cost someone a validated proposal.
	var effect json.RawMessage
	if snap != nil {
		var effErr error
		if effect, effErr = storedEffect(snap, *cs); effErr != nil {
			h.logger.Warn("could not compute effect summary; falling back to scope counts",
				"namespace", cs.Namespace, "err", effErr)
		}
	}
	baseValues := baseValuesFrom(snap, *cs)

	cr, err := h.createNumbered(ctx, cs.Namespace, func(b *ent.ApprovalRequestCreate) *ent.ApprovalRequestCreate {
		b = b.
			SetActionType(approval.ActionTypeConfigMutation).
			SetTitle(title).
			SetDescription(description).
			SetAuthor(actor).
			SetCreatedBy(actor).
			SetBaseHash(versionHash(versions)).
			SetBasePresent(presentInVersions(versions, scope)).
			SetPayload(payload)
		if len(effect) > 0 {
			b = b.SetBaseEffect(effect)
		}
		if len(baseValues) > 0 {
			b = b.SetBaseValues(baseValues)
		}
		return b
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create change request: %w", err)
	}
	return cr, nil, nil
}

// crNumberRetries bounds the allocation retry. Each retry costs one round trip
// and only happens when two creates in the SAME namespace interleave, which for
// human-authored change requests is rare — three attempts is generous.
const crNumberRetries = 3

// createNumbered allocates the next per-namespace number and inserts the row.
//
// Allocation is max(number)+1 for the namespace, and correctness comes from the
// UNIQUE index on (namespace, number) rather than from the read: two concurrent
// creates can both read the same max, but only one insert survives and the other
// retries against the now-higher max. A counter table with an upsert-returning
// would avoid the retry, but it is a second source of truth for a number the
// requests themselves already carry — and this way a row deleted by hand cannot
// leave the counter pointing past reality.
//
// Numbers are NOT reused after a delete: max()+1 skips the gap, which is what
// you want, because an id that once meant one change must never come to mean
// another.
func (h *ChangeRequest) createNumbered(
	ctx context.Context,
	namespace string,
	build func(*ent.ApprovalRequestCreate) *ent.ApprovalRequestCreate,
) (*ent.ApprovalRequest, error) {
	var lastErr error
	for attempt := 0; attempt < crNumberRetries; attempt++ {
		next, err := h.nextCRNumber(ctx, namespace)
		if err != nil {
			return nil, err
		}
		cr, err := build(h.db.ApprovalRequest.Create()).
			SetNamespace(namespace).
			SetNumber(next).
			Save(ctx)
		if err == nil {
			return cr, nil
		}
		if !ent.IsConstraintError(err) {
			return nil, err
		}
		// Someone took this number between the read and the insert. Re-read.
		lastErr = err
	}
	return nil, fmt.Errorf("allocate change request number for %q after %d attempts: %w",
		namespace, crNumberRetries, lastErr)
}

func (h *ChangeRequest) nextCRNumber(ctx context.Context, namespace string) (int, error) {
	last, err := h.db.ApprovalRequest.Query().
		Where(approvalrequest.NamespaceEQ(namespace)).
		Order(ent.Desc(approvalrequest.FieldNumber)).
		First(ctx)
	if ent.IsNotFound(err) {
		return 1, nil // first change request for this data center
	}
	if err != nil {
		return 0, fmt.Errorf("read last change request number for %q: %w", namespace, err)
	}
	return last.Number + 1, nil
}

// Get loads a change request with its approvals and merge attempts.
func (h *ChangeRequest) Get(ctx context.Context, id int64) (*ent.ApprovalRequest, error) {
	cr, err := h.db.ApprovalRequest.Query().
		Where(approvalrequest.ID(id)).
		WithApprovals().
		WithMergeAttempts().
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errCRNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load change request: %w", err)
	}
	return cr, nil
}

// State recomputes everything derived about a request. Called from read paths
// to DISPLAY and from approve/merge to ENFORCE — one function, so what an
// operator sees and what the write path acts on can never disagree.
func (h *ChangeRequest) State(ctx context.Context, cr *ent.ApprovalRequest) (crState, error) {
	var st crState
	if err := json.Unmarshal(cr.Payload, &st.Changeset); err != nil {
		return st, fmt.Errorf("decode changeset: %w", err)
	}

	declared := declaredOrbIDs(&st.Changeset)
	existing, err := h.schema.ResolveEntities(ctx, declared)
	if err != nil {
		return st, fmt.Errorf("resolve entities: %w", err)
	}
	st.Scope = baseScope(ctx, h.dgraphURL, declared, existing)
	// Versions, not content. State answers "has this moved?" and is called once
	// per request RENDERED — the nav badge renders the whole open queue on every
	// page load — so it must not fetch and normalize every node in scope. The
	// paths that genuinely need node content (the diff view, merge) ask for the
	// snapshot separately via StateWithSnapshot.
	st.Versions, err = scopeVersions(ctx, h.dgraphURL, st.Scope)
	if err != nil {
		return st, fmt.Errorf("read current state: %w", err)
	}
	st.CurrentHash = versionHash(st.Versions)
	st.Stale = st.CurrentHash != cr.BaseHash

	// Present at open, gone now. Detected here so both the detail view and
	// merge see it — the view can warn before anyone spends a review on it.
	for _, id := range cr.BasePresent {
		if _, ok := st.Versions[id]; !ok {
			st.Missing = append(st.Missing, id)
		}
	}
	sort.Strings(st.Missing)

	st.Approvals = cr.Edges.Approvals
	if st.Approvals == nil {
		st.Approvals, err = cr.QueryApprovals().Order(ent.Asc(entapproval.FieldCreatedAt)).All(ctx)
		if err != nil {
			return st, fmt.Errorf("load approvals: %w", err)
		}
	}
	// Staleness and approval-currency are questions about a request that can
	// still be acted on. Once it is merged, rejected or closed they are not
	// merely irrelevant — they are actively wrong: a merge MOVES the graph, so
	// a just-merged request would report itself stale with zero approvals, and
	// the record of who approved it would read as "approved an earlier
	// version". For a terminal request the approvals are historical facts, so
	// they are counted as cast.
	terminal := cr.Status != approvalrequest.StatusOpen
	if terminal {
		st.Stale = false
		st.Missing = nil
	}
	for _, a := range st.Approvals {
		switch {
		case a.Decision == entapproval.DecisionRejected:
			st.Rejected++
		case terminal || a.ApprovedAtHash == st.CurrentHash:
			st.Valid++
		}
	}

	pol, err := h.resolvePolicy(ctx, cr.ActionType, &st.Changeset)
	if err != nil {
		return st, err
	}
	st.Required, st.BypassRoles = pol.required, pol.bypassRoles

	st.Status = string(cr.Status)
	if st.Status == approval.StatusOpen && st.Valid >= st.Required {
		// Required 0 means no policy governs this change, so an open request
		// reads as approved straight away. That is the opt-in property: a
		// voluntarily-opened request in an ungoverned namespace must still be
		// mergeable, and installing the engine must not make anything harder
		// than it was. Its only guard is then the staleness check at merge.
		st.Status = approval.StatusApproved
	}
	return st, nil
}

// StateWithSnapshot is State plus the scope's full content.
//
// Separate from State because the snapshot is the expensive half — a subtree
// fetch and a normalize per request — and only two callers need it: the diff
// view, which renders field-level changes, and merge, which needs the before
// state to decide what actually applied. Everything that merely displays a
// request's STATUS goes through State and never pays for it.
func (h *ChangeRequest) StateWithSnapshot(ctx context.Context, cr *ent.ApprovalRequest) (crState, error) {
	st, err := h.State(ctx, cr)
	if err != nil {
		return st, err
	}
	st.Snapshot, err = baseSnapshot(ctx, h.dgraphURL, st.Scope)
	if err != nil {
		return st, fmt.Errorf("read current state: %w", err)
	}
	return st, nil
}

// resolvedPolicy is the policy governing a changeset, if any.
type resolvedPolicy struct {
	required    int
	bypassRoles []string
	namespace   string
	found       bool
}

// resolvePolicy finds the one policy governing a changeset.
//
// One, never several: policies are unique per namespace and carry their own
// type list, so there is nothing to compose. The composed alternative produced
// outcomes neither policy stated — most sharply, intersecting bypass_roles
// [admin] with [dev] yielded "nobody bypasses", a rule an admin never wrote and
// would meet as an unexplained refusal.
//
// With no matching policy the changeset is ungoverned: required is 0, so it
// reads as approved immediately and merges without review. That is the opt-in
// property — installing the engine changes nothing until an admin declares a
// protected class.
// policyRow returns the one enabled policy governing a namespace, or nil.
//
// Separated from resolvePolicy because WHICH row applies depends only on
// (actionType, namespace) — one policy per namespace — while whether it
// governs a given changeset depends on that changeset's types. Splitting them
// is what makes the row memoisable.
//
// Resolution is FALLBACK, in two steps, and the order carries the semantics:
//
//  1. The namespace's OWN row, read regardless of `enabled`. If one exists it
//     is the answer — including when it is disabled, in which case the answer
//     is "not gated". A disabled row SHADOWS the global rather than falling
//     through to it: `enabled=false` means "this namespace is deliberately
//     exempt", and it is the only per-namespace off switch there is.
//  2. Only when the namespace has NO row of its own, the enabled global
//     (`all_namespaces`) row, if there is one.
//
// Exactly one row is ever returned, which is what keeps "which policy did
// this?" answerable with a single name. A global is therefore a DEFAULT, never
// a floor: a namespace row overrides it even when it is weaker.
func (h *ChangeRequest) policyRow(ctx context.Context, actionType, namespace string) (*ent.ApprovalPolicy, error) {
	memo := policyMemoFrom(ctx)
	key := actionType + "\x00" + namespace
	if memo != nil {
		if p, ok := memo.get(key); ok {
			return p, nil
		}
	}

	p, err := governingPolicy(ctx, h.db, actionType, namespace)
	if err != nil {
		return nil, err
	}
	if memo != nil {
		memo.put(key, p)
	}
	return p, nil
}

// governingPolicy is THE resolution rule, and there is exactly one of it.
//
// Both callers that need "which policy governs this namespace" go through here
// — the change-request engine via policyRow, and the write gate via
// matchingPolicy. They used to each run their own query, which is how the
// all-namespaces feature initially shipped working in the engine and invisible
// to the gate: the engine refused to merge without approval while a direct
// mutation wrote straight through. Two implementations of one authorization
// rule fail in exactly that direction — open — so do not add a third.
func governingPolicy(ctx context.Context, db *ent.Client, actionType, namespace string) (*ent.ApprovalPolicy, error) {
	// Step 1 — the namespace's own row. Deliberately NOT filtered on enabled:
	// the disabled case has to be distinguishable from the absent case, because
	// they resolve to opposite answers. Disabled means "this namespace is
	// deliberately exempt" and SHADOWS the global; absent falls through to it.
	own, err := db.ApprovalPolicy.Query().
		Where(
			approvalpolicy.ActionTypeEQ(actionType),
			approvalpolicy.NamespaceEQ(namespace),
		).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("resolve approval policy: %w", err)
	}
	if err == nil {
		if !own.Enabled {
			return nil, nil
		}
		return own, nil
	}

	// Step 2 — no row of its own, so the global applies if one is enabled.
	global, err := db.ApprovalPolicy.Query().
		Where(
			approvalpolicy.ActionTypeEQ(actionType),
			approvalpolicy.AllNamespacesEQ(true),
			approvalpolicy.EnabledEQ(true),
		).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("resolve global approval policy: %w", err)
	}
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return global, nil
}

// policyLabel names a policy in prose — a refusal message, a privileged-write
// warning, the `bypassedPolicy` audit field.
//
// A global row has no namespace, and an empty string there would render
// "changes to  require approval". Distinct from the audit RESOURCE ID, which
// uses `*`: that one is a filter key people type into `?resource_id=`, this one
// is a sentence.
func policyLabel(p *ent.ApprovalPolicy) string {
	if p.AllNamespaces {
		return "all namespaces"
	}
	return p.Namespace
}

// policyMemo caches policy rows for the lifetime of ONE request.
//
// Rendering a change request derives its status, and status depends on the
// policy — so a list of N requests asked PostgreSQL for the same row N times,
// and on the queue page those rows are overwhelmingly one namespace. The memo
// makes that one query per distinct namespace in the response.
//
// Not a TTL cache and deliberately not one: it is created per request and
// discarded with it, so PostgreSQL is re-read on the next request and a policy
// an admin just changed applies immediately. Install it (withPolicyMemo) only
// where a single request renders many rows; without it every lookup queries, as
// before.
type policyMemo struct {
	mu sync.Mutex
	m  map[string]*ent.ApprovalPolicy
}

func (p *policyMemo) get(key string) (*ent.ApprovalPolicy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.m[key]
	return v, ok
}

func (p *policyMemo) put(key string, v *ent.ApprovalPolicy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[key] = v
}

type policyMemoCtxKey struct{}

func withPolicyMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, policyMemoCtxKey{}, &policyMemo{m: map[string]*ent.ApprovalPolicy{}})
}

func policyMemoFrom(ctx context.Context) *policyMemo {
	m, _ := ctx.Value(policyMemoCtxKey{}).(*policyMemo)
	return m
}

func (h *ChangeRequest) resolvePolicy(ctx context.Context, actionType string, cs *approval.Changeset) (resolvedPolicy, error) {
	p, err := h.policyRow(ctx, actionType, cs.Namespace)
	if err != nil || p == nil {
		return resolvedPolicy{}, err
	}

	// Whether the row GOVERNS this particular changeset is a property of the
	// changeset, so it is decided here rather than memoised with the row.
	governs := p.AllTypes
	if !governs {
		for _, ch := range cs.Changes {
			if ch.Type != "" && slices.Contains(p.Types, ch.Type) {
				governs = true
				break
			}
		}
	}
	if !governs {
		return resolvedPolicy{}, nil
	}

	roles := p.BypassRoles
	if roles == nil {
		roles = []string{}
	}
	return resolvedPolicy{
		required:    p.RequiredApprovals,
		bypassRoles: roles,
		namespace:   policyLabel(p),
		found:       true,
	}, nil
}

// Approve records a reviewer's approval, stamped with the hash it was cast
// against.
//
// Approving a STALE request is allowed and is the intended way to re-review
// after the base moved: the new decision stamps the current hash and starts
// counting, while the earlier one stops. Blocking it would leave an operator
// with a request they can neither advance nor fix.
func (h *ChangeRequest) Approve(ctx context.Context, id int64, actor string, role user.Role, comment string) (*ent.ApprovalRequest, error) {
	return h.decide(ctx, id, actor, role, comment, entapproval.DecisionApproved)
}

// Reject records a reviewer's rejection, which is terminal.
func (h *ChangeRequest) Reject(ctx context.Context, id int64, actor string, role user.Role, comment string) (*ent.ApprovalRequest, error) {
	return h.decide(ctx, id, actor, role, comment, entapproval.DecisionRejected)
}

func (h *ChangeRequest) decide(ctx context.Context, id int64, actor string, role user.Role, comment string, decision entapproval.Decision) (*ent.ApprovalRequest, error) {
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

	// Peer review: the author cannot be the reviewer. Waived for a caller whose
	// role may bypass the gate outright — demanding a second pair of eyes from
	// someone who could have written directly is friction, not control (D15).
	if cr.Author == actor && !roleIn(role, st.BypassRoles) {
		return nil, errCRSelfApproval
	}

	// Re-deciding replaces this approver's previous row, so a reviewer can
	// change their mind and N-of-M still counts distinct people rather than
	// distinct clicks. Delete-then-create (matching how divergence resolutions
	// are replaced) because the unique index would otherwise reject the second
	// decision outright.
	if _, err := h.db.Approval.Delete().
		Where(
			entapproval.ApprovalRequestID(cr.ID),
			entapproval.ApproverEQ(actor),
		).Exec(ctx); err != nil {
		return nil, fmt.Errorf("clear previous decision: %w", err)
	}
	if err := h.db.Approval.Create().
		SetApprovalRequestID(cr.ID).
		SetApprover(actor).
		SetCreatedBy(actor).
		SetDecision(decision).
		SetComment(comment).
		SetApprovedAtHash(st.CurrentHash).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("record decision: %w", err)
	}

	switch decision {
	case entapproval.DecisionRejected:
		cr, err = cr.Update().
			SetStatus(approvalrequest.StatusRejected).
			SetUpdatedAt(time.Now()).
			SetUpdatedBy(actor).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("mark rejected: %w", err)
		}
	case entapproval.DecisionApproved:
		// An approval re-anchors the base onto what the reviewer just looked
		// at. The diff they review is always computed against CURRENT intent,
		// so approving IS an attestation of the current state — and without
		// this, a request that went stale could never leave that state: the
		// re-review would make its approvals count again while `stale` stayed
		// true forever and the merge guard refused it.
		//
		// This does not touch approval validity, which is compared against the
		// current hash, not the base. It only moves what "unchanged since it
		// was last reviewed" means — which is what stale should mean.
		//
		// The ANCESTOR moves with the anchor. Leaving base_values behind would
		// make the field-level guard permanently disagree with the entity-level
		// one: the reviewer has just attested to current state, so current state
		// is what the merge must be checked against. A re-anchor that moved only
		// the hash would clear `stale` while every moved field stayed a conflict
		// forever.
		//
		// The ancestor is recomputed UNCONDITIONALLY, not only when the hash
		// moved. base_hash is a version-vector fingerprint, so a write that
		// changes a value without bumping `version` leaves it matching while
		// base_values goes stale — and gating the recompute on the hash made
		// that conflict unclearable by any action: approving returned 200,
		// changed nothing, and merge kept refusing. The only escape was closing
		// the request and proposing again. Approving IS the act of attesting to
		// current state, so current state is what the ancestor must become.
		needsRebase := cr.BaseHash != st.CurrentHash || len(cr.BaseValues) > 0
		if needsRebase {
			upd := cr.Update().SetBaseHash(st.CurrentHash)
			if len(cr.BaseValues) > 0 {
				snap, snapErr := baseSnapshot(ctx, h.dgraphURL, st.Scope)
				if snapErr != nil {
					return nil, fmt.Errorf("re-anchor ancestor: %w", snapErr)
				}
				upd = upd.SetBaseValues(baseValuesFrom(snap, st.Changeset))
			}
			if cr, err = upd.Save(ctx); err != nil {
				return nil, fmt.Errorf("re-anchor base: %w", err)
			}
		}
	}
	return h.Get(ctx, cr.ID)
}

// Close withdraws a request. Author-only, or a caller who could bypass the gate.
func (h *ChangeRequest) Close(ctx context.Context, id int64, actor string, role user.Role) (*ent.ApprovalRequest, error) {
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
	if cr.Author != actor && !roleIn(role, st.BypassRoles) {
		return nil, errCRForbidden
	}
	if _, err := cr.Update().
		SetStatus(approvalrequest.StatusClosed).
		SetUpdatedAt(time.Now()).
		SetUpdatedBy(actor).
		Save(ctx); err != nil {
		return nil, fmt.Errorf("close: %w", err)
	}
	return h.Get(ctx, id)
}

// Amend replaces an open request's title, description, or changeset.
//
// A changed changeset re-captures the base, which invalidates every existing
// approval automatically: they were stamped against the old hash and stop
// counting. No dismissal step to remember, and no window in which a reviewer's
// approval of one proposal silently carries over to a different one.
func (h *ChangeRequest) Amend(ctx context.Context, id int64, actor string, role user.Role, title, description *string, cs *approval.Changeset) (*ent.ApprovalRequest, []approval.ValidationError, error) {
	cr, err := h.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if cr.Status != approvalrequest.StatusOpen {
		return nil, nil, fmt.Errorf("%w: %s", errCRNotOpen, cr.Status)
	}
	st, err := h.State(ctx, cr)
	if err != nil {
		return nil, nil, err
	}
	if cr.Author != actor && !roleIn(role, st.BypassRoles) {
		return nil, nil, errCRForbidden
	}

	upd := cr.Update().SetUpdatedAt(time.Now()).SetUpdatedBy(actor)
	if title != nil {
		upd = upd.SetTitle(*title)
	}
	if description != nil {
		upd = upd.SetDescription(*description)
	}

	if cs != nil {
		res, err := approval.Validate(ctx, h.schema, cs)
		if err != nil {
			return nil, nil, fmt.Errorf("validate changeset: %w", err)
		}
		if len(res.Errors) > 0 {
			return nil, res.Errors, nil
		}
		existing, err := h.schema.ResolveEntities(ctx, declaredOrbIDs(cs))
		if err != nil {
			return nil, nil, fmt.Errorf("resolve entities: %w", err)
		}
		scope := baseScope(ctx, h.dgraphURL, declaredOrbIDs(cs), existing)
		versions, err := scopeVersions(ctx, h.dgraphURL, scope)
		if err != nil {
			return nil, nil, fmt.Errorf("capture base: %w", err)
		}
		payload, err := json.Marshal(cs)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal changeset: %w", err)
		}
		snap, snapErr := baseSnapshot(ctx, h.dgraphURL, scope)
		if snapErr != nil {
			if changesetAsserts(*cs) {
				return nil, nil, fmt.Errorf("read state for `before` assertions: %w", snapErr)
			}
			h.logger.Warn("could not snapshot base on amend; effect and ancestor omitted",
				"change_request", crHumanID(cr), "err", snapErr)
		}
		if snap != nil {
			if bad := beforeMismatches(snap, *cs); len(bad) > 0 {
				return nil, nil, &preconditionFailed{Problems: bad}
			}
		}

		upd = upd.SetPayload(payload).SetBaseHash(versionHash(versions)).SetBasePresent(presentInVersions(versions, scope))
		// Recomputed with the anchor: an amended request is a new plan against a
		// newly captured base, so carrying the old delta forward would describe
		// changes the request no longer proposes. The ancestor moves with it, for
		// the same reason.
		if snap != nil {
			effect, effErr := storedEffect(snap, *cs)
			if effErr != nil {
				h.logger.Warn("could not recompute effect summary on amend",
					"change_request", crHumanID(cr), "err", effErr)
			}
			if len(effect) > 0 {
				upd = upd.SetBaseEffect(effect)
			}
			if bv := baseValuesFrom(snap, *cs); len(bv) > 0 {
				upd = upd.SetBaseValues(bv)
			}
		}
	}

	if _, err := upd.Save(ctx); err != nil {
		return nil, nil, fmt.Errorf("amend: %w", err)
	}
	out, err := h.Get(ctx, id)
	return out, nil, err
}

// availableActions is the caller-relative verdict on what this caller may do
// right now. Computed server-side and returned in every response so no client
// re-implements eligibility — orbital's own UI renders buttons from this list
// exactly as an external client would.
func availableActions(cr *ent.ApprovalRequest, st crState, actor string, role user.Role, noAuthz bool) []string {
	if !noAuthz && !RoleAtLeast(role, user.RoleDev) {
		return nil // readonly can look, not act
	}
	bypass := noAuthz || roleIn(role, st.BypassRoles)
	isAuthor := cr.Author == actor

	var out []string
	switch st.Status {
	case approval.StatusOpen:
		if !isAuthor || bypass {
			out = append(out, "approve", "reject")
		}
		if isAuthor || bypass {
			out = append(out, "edit", "close")
		}
	case approval.StatusApproved:
		if isAuthor || approvedBy(st, actor) || bypass {
			out = append(out, "merge")
		}
		if !isAuthor || bypass {
			out = append(out, "reject")
		}
		if isAuthor || bypass {
			out = append(out, "close")
		}
	}
	sort.Strings(out)
	return out
}

func approvedBy(st crState, actor string) bool {
	for _, a := range st.Approvals {
		if a.Approver == actor && a.Decision == entapproval.DecisionApproved {
			return true
		}
	}
	return false
}

func roleIn(role user.Role, roles []string) bool {
	for _, r := range roles {
		if string(role) == r {
			return true
		}
	}
	return false
}

func declaredOrbIDs(cs *approval.Changeset) []string {
	out := make([]string, 0, len(cs.Changes))
	for _, ch := range cs.Changes {
		out = append(out, ch.OrbID)
	}
	return out
}

// interfaceFields are declared on the ConfigItem interface rather than on the
// concrete type, so their DQL predicate is prefixed ConfigItem., not <Type>.
var interfaceFields = map[string]bool{
	"namespace": true, "orbId": true, "name": true,
	"createdBy": true, "createdAt": true,
	"updatedBy": true, "updatedAt": true, "version": true,
}

// predicateFor maps a GraphQL field name to the DQL predicate a snapshot is
// keyed by.
func predicateFor(typeName, field string) string {
	if interfaceFields[field] {
		return "ConfigItem." + field
	}
	return typeName + "." + field
}

// applyChangesetTo simulates a changeset against the current snapshot and
// returns what the graph WOULD look like after a merge.
//
// This is what makes the diff endpoint a plan rather than a description: the
// reviewer sees the same before/after shape the export preview and guarded
// Apply produce, computed by the same graphdiff core, so "what will this do"
// has one answer across the whole product. Nothing is written; the target
// snapshot exists only for the length of the comparison.
// sameValue compares two ancestor/current values by their JSON encoding.
//
// Not reflect.DeepEqual: base_values round-trips through jsonb, so an edge's
// []string comes back as []any{string} and an int as float64, while the live
// snapshot side is decoded straight from DGraph. Encoding both and comparing
// bytes makes those representations agree — Go sorts map keys when marshalling,
// so the encoding is deterministic.
func sameValue(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false // unencodable: treat as different rather than as equal
	}
	return bytes.Equal(ja, jb)
}

// currentValue reads a predicate off a snapshot node, looking in fields then
// edges. Absent reads as nil, which is a legitimate ancestor value.
func currentValue(node *graphdiff.Node, pred string) any {
	if node == nil {
		return nil
	}
	if v, ok := node.Fields[pred]; ok {
		return v
	}
	if e, ok := node.Edges[pred]; ok {
		return e
	}
	return nil
}

// planMerge decides, per field, what a merge would do — and narrows the write to
// match.
//
// Three outcomes per field:
//
//   - CONFLICT: the ancestor says X, the field is not X now, and it is not at
//     the proposed value either. Someone else moved it to a third value and
//     writing would destroy their edit.
//   - SATISFIED: already at the proposed value. Dropped from the write. It stays
//     in the stored changeset — that is the author's declared intent and orbital
//     does not edit it — but writing it again would only bump the version and
//     emit an audit row for a change that changed nothing.
//   - APPLIES: written.
//
// Narrowing the write is not an optimisation, it is what makes the field-level
// guard SAFE. applyItem writes the whole `set`, so a guard that only checks the
// fields a request changes, paired with a write that touches every field it
// names, would silently push a stale status-quo value over someone else's edit.
// Guard narrowly and write narrowly, or guard widely and write widely; the
// mixture is the one combination that loses data.
//
// Items with no ancestor recorded (created before base_values existed, or whose
// snapshot failed) are passed through untouched and stay governed by the
// entity-level base_hash alone.
func planMerge(snap graphdiff.Snapshot, cs approval.Changeset, baseValues map[string]map[string]any) ([]approval.ChangeItem, []approval.ValidationError) {
	res := graphdiff.Compare(snap, applyChangesetTo(snap, cs))
	satisfied := make(map[string]map[string]bool, len(cs.Changes))
	for _, ch := range satisfiedItems(snap, cs, res) {
		m := make(map[string]bool, len(ch.Fields))
		for _, f := range ch.Fields {
			m[f.Field] = true
		}
		satisfied[ch.OrbID] = m
	}

	var conflicts []approval.ValidationError
	out := make([]approval.ChangeItem, 0, len(cs.Changes))

	for i, item := range cs.Changes {
		base := baseValues[item.OrbID]
		if len(base) == 0 || item.Op == approval.OpDelete {
			out = append(out, item)
			continue
		}
		node := snap[item.OrbID]
		typeName := item.Type
		if typeName == "" && node != nil && len(node.Types) > 0 {
			typeName = node.Types[0]
		}

		narrowed := item
		narrowed.Set = map[string]any{}
		narrowed.Clear = nil

		classify := func(f string, keep func()) {
			pred := predicateFor(typeName, f)
			if satisfied[item.OrbID][pred] {
				return // already at the proposed value: drop from the write
			}
			if want, recorded := base[pred]; recorded && !sameValue(want, currentValue(node, pred)) {
				conflicts = append(conflicts, approval.ValidationError{
					Index: i, OrbID: item.OrbID, Field: pred,
					Msg: fmt.Sprintf("changed since this was proposed: was %v, is now %v",
						want, currentValue(node, pred)),
					Hint: "Re-review the request, or amend it to propose against the current value.",
				})
				return
			}
			keep()
		}

		for f, v := range item.Set {
			f, v := f, v
			classify(f, func() { narrowed.Set[f] = v })
		}
		for _, f := range item.Clear {
			f := f
			classify(f, func() { narrowed.Clear = append(narrowed.Clear, f) })
		}

		if len(narrowed.Set) == 0 && len(narrowed.Clear) == 0 {
			continue // nothing left to write for this entity
		}
		sort.Strings(narrowed.Clear)
		out = append(out, narrowed)
	}

	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].OrbID != conflicts[j].OrbID {
			return conflicts[i].OrbID < conflicts[j].OrbID
		}
		return conflicts[i].Field < conflicts[j].Field
	})
	return out, conflicts
}

// preconditionFailed carries the `before` assertions that did not match. It is
// a 409, not a 400: nothing about the request is malformed — the world moved.
type preconditionFailed struct{ Problems []approval.ValidationError }

func (e *preconditionFailed) Error() string {
	return fmt.Sprintf("precondition failed on %d field(s)", len(e.Problems))
}

// changesetAsserts reports whether any item carries a `before`, i.e. whether
// this is a conditional request.
func changesetAsserts(cs approval.Changeset) bool {
	for _, item := range cs.Changes {
		if len(item.Before) > 0 {
			return true
		}
	}
	return false
}

// baseValuesFrom projects the ANCESTOR out of a snapshot: for every field the
// changeset writes or clears, the value that field holds right now.
//
// Predicate-keyed and taken straight off the normalized snapshot, so a merge-time
// comparison is between two values that went through the same normalizer. Storing
// the caller's raw input instead would put a hand-written value on one side of
// that comparison and a DGraph round-trip on the other.
//
// Scoped to the fields the changeset touches, not the whole subtree: a six-field
// changeset stores six values.
func baseValuesFrom(snap graphdiff.Snapshot, cs approval.Changeset) map[string]map[string]any {
	out := make(map[string]map[string]any, len(cs.Changes))
	for _, item := range cs.Changes {
		node := snap[item.OrbID]
		if node == nil {
			continue // a create has no ancestor
		}
		typeName := item.Type
		if typeName == "" && len(node.Types) > 0 {
			typeName = node.Types[0]
		}
		vals := map[string]any{}
		record := func(f string) {
			pred := predicateFor(typeName, f)
			if v, ok := node.Fields[pred]; ok {
				vals[pred] = v
				return
			}
			if e, ok := node.Edges[pred]; ok {
				vals[pred] = e
				return
			}
			vals[pred] = nil // absent now; "still absent" is a meaningful ancestor
		}
		for f := range item.Set {
			record(f)
		}
		for _, f := range item.Clear {
			record(f)
		}
		if len(vals) > 0 {
			out[item.OrbID] = vals
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// beforeMismatches reports the caller's `before` assertions that do not match
// current state — the fields that moved while the caller was composing.
//
// Implemented by running the assertions through the diff engine as if they were
// a proposal: if asserting "this field is already X" would CHANGE anything, the
// assertion is wrong. That borrows the one normalizer rather than writing a
// second notion of equality, the same reasoning as satisfiedItems.
func beforeMismatches(snap graphdiff.Snapshot, cs approval.Changeset) []approval.ValidationError {
	probe := approval.Changeset{Namespace: cs.Namespace}
	index := map[string]int{}
	for i, item := range cs.Changes {
		if len(item.Before) == 0 || item.Op == approval.OpDelete {
			continue
		}
		index[item.OrbID] = i
		probe.Changes = append(probe.Changes, approval.ChangeItem{
			OrbID: item.OrbID, Type: item.Type, Op: approval.OpUpdate, Set: item.Before,
		})
	}
	if len(probe.Changes) == 0 {
		return nil
	}

	res := graphdiff.Compare(snap, applyChangesetTo(snap, probe))
	var out []approval.ValidationError
	for _, ch := range res.Changes {
		if ch == nil {
			continue
		}
		if len(ch.Fields) == 0 {
			// The whole entity differs — it does not exist, so there was nothing
			// to have read.
			out = append(out, approval.ValidationError{
				Index: index[ch.OrbID], OrbID: ch.OrbID,
				Msg:  "before refers to an entity that does not exist",
				Hint: "Reload the entity and propose again.",
			})
			continue
		}
		for _, f := range ch.Fields {
			out = append(out, approval.ValidationError{
				Index: index[ch.OrbID], OrbID: ch.OrbID, Field: f.Field,
				Msg:  fmt.Sprintf("value moved since you read it: you saw %v, it is now %v", f.After, f.Before),
				Hint: "Someone changed this while you were composing. Reload and propose again.",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OrbID != out[j].OrbID {
			return out[i].OrbID < out[j].OrbID
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// satisfiedItems reports the part of a changeset that would do nothing: fields
// whose current value already equals the proposed one, and deletes whose target
// is already gone.
//
// Derived FROM the computed diff rather than by re-comparing values. A second
// comparison would need its own view of what "equal" means — DGraph round-trips
// some scalars as strings, and graphdiff normalizes before comparing — so two
// implementations would disagree on exactly the edge cases this exists to
// surface. Absence from the diff IS the definition of satisfied.
//
// Entries carry equal before/after so a client can render them with the same
// table it uses for changes.
func satisfiedItems(current graphdiff.Snapshot, cs approval.Changeset, res *graphdiff.Result) []*graphdiff.Change {
	// Predicates the diff already reports as changing, per orbId.
	changed := make(map[string]map[string]bool, len(res.Changes))
	for _, ch := range res.Changes {
		if ch == nil {
			continue
		}
		m := make(map[string]bool, len(ch.Fields))
		for _, f := range ch.Fields {
			m[f.Field] = true
		}
		changed[ch.OrbID] = m
	}

	var out []*graphdiff.Change
	for _, item := range cs.Changes {
		node := current[item.OrbID]

		if item.Op == approval.OpDelete {
			// Already gone: the delete is a no-op. An entity still present is a
			// real removal and belongs in `changes`, not here.
			if node == nil {
				out = append(out, &graphdiff.Change{OrbID: item.OrbID, Type: item.Type, Change: "satisfied"})
			}
			continue
		}
		// A create has no current node, so every field is genuinely new and the
		// diff reports the whole entity as added. Nothing to report here.
		if node == nil {
			continue
		}

		typeName := item.Type
		if typeName == "" && len(node.Types) > 0 {
			typeName = node.Types[0]
		}

		var fields []graphdiff.FieldChange
		for f := range item.Set {
			pred := predicateFor(typeName, f)
			if changed[item.OrbID][pred] {
				continue // the diff says this one moves
			}
			cur, isField := node.Fields[pred]
			if !isField {
				// Edge references live in Edges, not Fields. An edge the diff
				// does not report is likewise already pointing where the
				// changeset wants it.
				if _, isEdge := node.Edges[pred]; !isEdge {
					continue // neither field nor edge: nothing to say about it
				}
				cur = nil
			}
			fields = append(fields, graphdiff.FieldChange{Field: pred, Before: cur, After: cur})
		}
		for _, f := range item.Clear {
			pred := predicateFor(typeName, f)
			if changed[item.OrbID][pred] {
				continue
			}
			if _, present := node.Fields[pred]; present {
				continue // still set, so clearing it would change something
			}
			fields = append(fields, graphdiff.FieldChange{Field: pred, Before: nil, After: nil})
		}
		if len(fields) == 0 {
			continue
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Field < fields[j].Field })
		out = append(out, &graphdiff.Change{
			OrbID: item.OrbID, Type: typeName, Change: "satisfied", Fields: fields,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OrbID < out[j].OrbID })
	return out
}

func applyChangesetTo(current graphdiff.Snapshot, cs approval.Changeset) graphdiff.Snapshot {
	target := make(graphdiff.Snapshot, len(current))
	for id, n := range current {
		clone := &graphdiff.Node{
			OrbID:  n.OrbID,
			Types:  append([]string(nil), n.Types...),
			Fields: make(map[string]any, len(n.Fields)),
			Edges:  make(map[string][]string, len(n.Edges)),
		}
		for k, v := range n.Fields {
			clone.Fields[k] = v
		}
		for k, v := range n.Edges {
			clone.Edges[k] = append([]string(nil), v...)
		}
		target[id] = clone
	}

	for _, item := range cs.Changes {
		if item.Op == approval.OpDelete {
			delete(target, item.OrbID)
			continue
		}
		node := target[item.OrbID]
		if node == nil {
			node = &graphdiff.Node{
				OrbID:  item.OrbID,
				Types:  []string{item.Type},
				Fields: map[string]any{},
				Edges:  map[string][]string{},
			}
			target[item.OrbID] = node
		}
		typeName := item.Type
		if typeName == "" && len(node.Types) > 0 {
			typeName = node.Types[0]
		}
		for f, v := range item.Set {
			pred := predicateFor(typeName, f)
			if targets, ok := edgeTargetOrbIDs(v); ok {
				node.Edges[pred] = targets
				continue
			}
			node.Fields[pred] = v
		}
		for _, f := range item.Clear {
			pred := predicateFor(typeName, f)
			delete(node.Fields, pred)
			delete(node.Edges, pred)
		}
	}
	return target
}

// edgeTargetOrbIDs recognises an edge value — a reference object, or a list of
// them — and returns the orbIds it points at.
func edgeTargetOrbIDs(v any) ([]string, bool) {
	switch t := v.(type) {
	case map[string]any:
		if id, ok := t["orbId"].(string); ok && id != "" {
			return []string{id}, true
		}
	case []any:
		var out []string
		for _, e := range t {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, false
			}
			id, ok := m["orbId"].(string)
			if !ok || id == "" {
				return nil, false
			}
			out = append(out, id)
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out, true
		}
	}
	return nil, false
}

// roleBypassesAnyPolicy reports whether the caller's role appears in the
// bypass_roles of ANY enabled policy.
//
// Used to decide whether "awaiting my review" can exclude the caller's own
// requests in SQL. Bypass makes approve available on your own request, so the
// exclusion is only safe when no policy grants it — and one query answers that
// for every row at once, where the per-row answer would need the namespace.
//
// Errs toward false (no exclusion, render everything) on an unresolvable
// caller: a slower correct answer beats a fast wrong one.
func (h *ChangeRequest) roleBypassesAnyPolicy(ctx context.Context, caller callerRole) (bool, error) {
	if caller.NoAuthz {
		return true, nil // no authz backend means everything is available
	}
	if caller.Role == "" {
		return true, nil
	}
	rows, err := h.db.ApprovalPolicy.Query().
		Where(approvalpolicy.EnabledEQ(true)).
		Select(approvalpolicy.FieldBypassRoles).
		All(ctx)
	if err != nil {
		return false, fmt.Errorf("read bypass roles: %w", err)
	}
	for _, p := range rows {
		if slices.Contains(p.BypassRoles, string(caller.Role)) {
			return true, nil
		}
	}
	return false, nil
}
