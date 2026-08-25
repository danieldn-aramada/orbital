# Design Proposal: AEP Change Management — the write half (Spike 31)

**Status:** WIP — §3 (guarded Apply) is **shipped**; §2 (selective revert) and §4 (attribution) are still proposals. This doc stays until those land, then folds into `docs/reference/` and is deleted (spike-lifecycle rule, CLAUDE.md).
**Date:** 2026-08-21
**Scope:** orbital only. Depends on **Spike 30** (shipped — see `docs/reference/OCI.md` § "Export preview"), which is the read half.
**Relates to:** ROADMAP Spike 31; configbundle `.local/design-change-management.md` (AEP change-management, Approach 2 / Option C).

---

## 1. Problem & framing

The AEP change-management flow (Approach 2) is: multiple PIM-elevated users write intent directly into Orbital → the data center owner **reviews the diff**, **selectively reverts** individual changes, then **Applies** (export + publish) — and the edge only ever receives reviewed intent.

**Spike 30 delivers the read half** (the diff preview). This spike is the **write half** — the three capabilities that turn a read-only preview into a governed review-and-ship workflow:

1. **Selective revert** (§2) — discard individual changes before Apply.
2. **Guarded Apply** (§3) — ensure what ships is what was reviewed (TOCTOU).
3. **Attribution** (§4) — "who changed this" on each diff row.

Nothing here weakens the intent-only invariant: Orbital stays the single committed source of desired state; PIM is an access concern handled at the IdP, not modeled in Orbital.

---

## 2. Selective revert

After reviewing the Spike 30 diff and *before* Apply, the owner may discard individual changes — "revert this field / this node back to what was last published." This is a **write** (setting a field back to its baseline value). The Spike 30 preview response already carries each changed field's baseline in `changes[].fields[].before`, so no extra fetch is needed to know what to write.

### Path 1 — Minimal, no new Orbital API (recommended for MVP)

AEP issues the **existing** GraphQL `update{Type}` mutations, setting each reverted field to the `before` value from the preview response. Zero new Orbital surface area; reuses the proven mutation + authz + audit path verbatim. A "revert" is indistinguishable from any other intent edit — correctly, because it *is* one (writing the old value back).

### Path 2 — Batch revert endpoint (optional follow-on)

```
POST /api/v1/export/revert
{ "orbId": "<dc>", "changes": [ { "orbId": "<node>", "fields": ["Server.hostname", …] } ] }
```

Orbital resets the listed fields to their last-published baseline in a single atomic (all-or-nothing) call. Justified only if bulk revert (many nodes at once) or transactional semantics become a real need — until then it is pure surface area over Path 1. It would require Orbital itself to fetch the baseline (pull the last artifact by digest, as the preview does); Path 1 instead reuses the values AEP already holds from the preview, which is why Path 1 is lighter.

**Recommendation:** ship **Path 1** for MVP; add **Path 2** only if a batch/atomic revert UX is demanded.

---

## 3. Guarded Apply (TOCTOU) — **BUILT 2026-08-24**

> **Status: shipped.** Optional `expectedContentHash` on `POST /api/v1/export`; 409 + no job row on mismatch; orbital's UI sends it and re-opens the preview with the fresh diff on 409. Contract + what the hash covers is documented in `docs/reference/OCI.md` § "Guarded Apply". Guarded by `internal/handler/export_guarded_apply_integration_test.go` (stale hash → 409, no job); the no-hash back-compat path is covered by every other export integration test. One deviation from the design below: the check runs **synchronously in `Trigger`**, not in the export goroutine — an async goroutine can only fail the *job*, whereas the operator needs a real 409 to act on, and rejecting pre-creation avoids junk failed-job rows. The window difference is seconds against a click gap of minutes.

Between the owner reviewing the diff and clicking Apply, another PIM-elevated user can mutate intent — so the edge could receive something different from what was reviewed. This is the `expectedContentHash` guard **designed in Spike 30 §6 and deferred there**; it is built here.

- The Spike 30 preview response returns `current.contentHash` — a stable hash over the normalized current subgraph.
- AEP passes it back as an **optional `expectedContentHash`** on `POST /api/v1/export`.
- When present, the export goroutine — after loading the subgraph into scratch and **before** pushing — recomputes the same normalized hash and **aborts with `409 Conflict`** if it does not match. AEP prompts the owner to re-review.
- Opt-in, last-writer-wins by default, strict when the caller asks — the export analog of `ifVersion` optimistic concurrency (`docs/reference/DIVERGENCE.md`).

**Apply = export AND publish**, not export alone — publish is what reaches OCI / the edge. The guard fires on the export leg (before any bytes are produced).

---

## 4. Attribution — "who changed this"

The Spike 30 diff answers *what* changed. Attribution answers *who / when*, and the audit log is the right source for that (it is **not** the right source for the diff — see Spike 30 §2).

- For each changed node, AEP joins the diff to the audit log by orbId: `GET /api/v1/audit-log?orbId=<node>` returns the `Event`s (actor, timestamp, operation) touching it.
- **Node-level attribution is clean** ("this Server was last modified by X at T"). **Field-level attribution is best-effort**: the audit `Event` records the mutation *input* (`details.variables`), not a per-field owner, so "who set *this field*" is inferred from the most recent Event whose variables include it. Good enough for a review UI; not a guarantee.
- No new Orbital API — the audit-log endpoint already exists (`GET /api/v1/audit-log`, filters incl. `orbId`).

---

## 5. PIM / authz / audit (no special-casing)

All three capabilities are ordinary writes or reads and reuse existing machinery:

- **Authz** — `RequireRole` (dev/admin) gates the revert writes, the same as any `update{Type}`. PIM elevation is how AEP obtains a write-role token; **Orbital does not model PIM** — it enforces the role on the token (Spike 11).
- **Audit** — every revert emits an audit `Event` (`actor` via `actorFromContext`, input in `details`) exactly like any mutation, so a revert is itself attributable.
- **Concurrency** — reverts are subject to the per-node `version` optimistic-concurrency counter; the review → revert → Apply sequence is protected end-to-end by §3's guard.

**No new state:** no field-history table, no staging layer, no per-entity export-version field. Orbital stays intent-only and committed.

---

## 6. Open questions

1. **Batch revert (Path 2) — build now or defer?** Recommendation: defer to Path 1 until a bulk/atomic revert need is demonstrated.
2. **Field-level vs node-level attribution.** §4 gives node-level cleanly and field-level best-effort. If the UI needs guaranteed field-level "who," that is a larger change (it is the Option-A field-history antipattern Spike 30/Spike 25 rejected — so the answer is probably "node-level is enough").
3. **Should `expectedContentHash` be required (not optional) when the caller is AEP?** Leaning optional (last-writer-wins default), strict on request — but a policy that AEP-originated Applies must always carry it is worth considering.

---

## 7. ROADMAP

This is the design for **Spike 31 — AEP change-management: guarded Apply + selective revert + attribution** (depends on Spike 30). It consolidates material already specified in Spike 30 (§6 guarded Apply) and the configbundle change-management findings (revert paths, attribution), plus §2's revert design. No new spike; this closes Spike 31's design phase.
