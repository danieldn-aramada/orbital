# Orbital API Cheatsheet — Change Control

Copy-paste REST for change requests and approval policies. Companion to `docs/api-cheatsheet.md` — setup, auth and the GraphQL half live there; this covers the change-control surface only. Deeper context: `docs/reference/CHANGE-CONTROL.md`.

## Setup

Same as the main cheatsheet — `$ORBITAL_URL`, `$TOKEN`, `Authorization: Bearer`, everything under `/api/v1`.

```bash
export ORBITAL_URL=http://ilb.devnew.armada.internal/orbital
export TOKEN=<your bearer token>
```

- Change request ids are `<namespace>-<number>` — `colo-42`. Quotable, and always the first column in a queue.
- Every endpoint here is public — orbital's own UI uses exactly these.

**Full API reference — Swagger UI.** Authoritative for every param, type, and required/optional flag. The `#/<tag>` anchor jumps straight to the section:

```
$ORBITAL_URL/swagger/index.html#/change-requests
$ORBITAL_URL/swagger/index.html#/approval-policies

# local dev
http://localhost:8001/swagger/index.html#/change-requests
```

This sheet is the common paths; Swagger is the complete surface.

---

## Change requests

### Open a change request

Example creates a change request with 2 changes to 2 nodes (by orbId) related to target server `CWJHDX3`.

```bash
curl -s -X POST $ORBITAL_URL/api/v1/change-requests -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{
  "title": "Enable SSH + maintenance on CWJHDX3",
  "namespace": "colo",
  "description": "Field ops request for the Nov window.",
  "changes": [
    { "op": "update", "orbId": "colo:CWJHDX3-idrac",              "set": { "sshEnabled": true } },
    { "op": "update", "orbId": "colo:server-maintenance-CWJHDX3", "set": { "enabled": true } }
  ]
}' | jq '{id, status, requiredApprovals}'
```
Response
```json
{ "id": "colo-42", "status": "open", "requiredApprovals": 1 }
```

### Change object 

```jsonc
{ "op":     "update",                   // upsert | update | delete
  "orbId":  "colo:CWJHDX3-idrac",       // required — identifies the entity
  "type":   "IdracSettings",            // only when CREATING something new
  "set":    { "sshEnabled": true },     // fields to write
  "clear":  ["windowStart"],            // field NAMES to unset
  "before": { "sshEnabled": false } }   // optional — the values you READ
```

- One entity per item. Two entities = two items.
- **Copy orbIds, do not derive them.** `IdracSettings` predates the `<ns>:<kind>-<natural-key>` convention and is still `<ns>:<serviceTag>-idrac` — `colo:CWJHDX3-idrac`, *not* `colo:idrac-CWJHDX3` (`DGRAPH.md` § legacy). Every example here was wrong in the second direction until 2026-09-02, so it fails as a guess even when it reads right.
- Point at another entity by orbId, never nest it: `"dataCenter": { "orbId": "colo:dc-01" }`. A nested entity is rejected `400`.
- Bad fields are caught when you create the request, not at merge.

### `before` — make an item conditional

Send the values you read alongside the values you want. Orbital then refuses if the world moved under you, **twice**: when you create the request, and again at merge.

```
409 MVCC_CONFLICT
{ "error": "state moved since you read it",
  "problems": [{ "orbId": "colo:CWJHDX3-idrac", "field": "IdracSettings.sshEnabled",
                 "message": "value moved since you read it: you saw false, it is now true",
                 "hint": "Someone changed this while you were composing. Reload and propose again." }] }
```

- **Omit it** and the item is unconditional — guarded only at entity level, which cannot see a write that changes a value without bumping `version`.
- **Send it** and you also get the merge-time check: a field moved to a *third* value is a conflict and refuses the whole merge; a field already at your proposed value is dropped from the write, so merging costs no version bump and writes no audit row for a change that changed nothing.
- Read it back on `GET /{id}` — it round-trips in `changes[]`.
- For full design see `docs/reference/CHANGE-CONTROL.md` § "Changeset contract".

### List change requests

Continuing the example above — what is in flight for the two entities you just changed:

```bash
curl -s "$ORBITAL_URL/api/v1/change-requests?status=active\
&orbId=colo:CWJHDX3-idrac&orbId=colo:server-maintenance-CWJHDX3" \
  -H "Authorization: Bearer $TOKEN" | jq '{total, items: [.items[] | {id, effect, author, status}]}'
```
Response
```json
{ "total": 1,
  "items": [ { "id": "colo-42",
               "effect": { "entities": 2, "fields": 2 },
               "//": "one field only -> effect also carries field, before, value",
               "author": "dev@armada.ai",
               "status": "open" } ] }
```

```bash
# what this caller can still review — the nav badge count
curl -s "$ORBITAL_URL/api/v1/change-requests?awaiting_review=true" -H "Authorization: Bearer $TOKEN" | jq .total
```

| Filter | |
|---|---|
| `status` | `open` `approved` `active` `rejected` `merged` `closed` — repeatable, OR-ed |
| `orbId` | repeatable, OR-ed, max 128 |
| `namespace` · `author` · `mine` · `awaiting_review` | |

- **Ask about the whole owned subtree.** An edit to an owned child records the *child's* orbId — the maintenance change above is filed under `colo:server-maintenance-…`, so asking only about `colo:server-…` finds nothing.
- Use `status=active`, not `open` — `approved` is derived, so `open` alone misses approved-but-unmerged requests.
- **Render rows from `effect`, not by walking `changes`.** It is what the request would DO: the delta is computed once at creation against the same snapshot the staleness anchor comes from, so posting a complete end-state where one field differs still yields `fields: 1` — with `field`, `before` and `value` naming it. `orbId`/`type` appear whenever one entity is touched. `/diff` stays the field-by-field authority for a review view.
- An unknown `status`, or over 128 `orbId`s, is refused `400` — never silently truncated.

### Fetch one, with its diff

```bash
curl -s $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" \
  | jq '{id, status, approvals, requiredApprovals, availableActions, missingTargets, mergeAttempts}'

curl -s $ORBITAL_URL/api/v1/change-requests/colo-42/diff -H "Authorization: Bearer $TOKEN" \
  | jq '{stale, baseHash, contentHash, summary, changes, satisfied}'
```

Render your buttons straight from `availableActions` — it is caller-relative and already accounts for role, authorship and prior approval. Don't re-derive it.

`changes` is flat — one entry per changed entity. The diff is always against *current* intent, so a stale request's diff already reflects the move.

**Render `satisfied[]` too, or your table will lie by omission.** It is the part of the proposal that would do nothing — a field someone else already set to the value this request wants, or a delete whose target is already gone. Those drop out of `changes` by definition, so a request appears to shrink with no indication that it did. Same flat shape, `before == after` on every entry, so one renderer handles both. Orbital's own review page shows them struck through under a *No change* label.

### Approve / reject / close

```bash
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/approve -H "Authorization: Bearer $TOKEN"
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/reject  -H "Authorization: Bearer $TOKEN"
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/close   -H "Authorization: Bearer $TOKEN"
```

- Approver must differ from the author — unless their role is in the policy's `bypassRoles`.
- Approving clears staleness — it re-anchors the request to what the reviewer just saw.
- `approved` is not a stored status. It is derived at read time and reverts to `open` on its own when the base moves — so don't cache it.
- Superseded approvals survive and read as *"approved an earlier version"*.

### Merge an approved request

```bash
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/merge -H "Authorization: Bearer $TOKEN" | jq .
```

Guards run `TARGET_MISSING` → stale → not-approved.

```json
{ "error": "base moved since this request was reviewed",
  "code": "MVCC_CONFLICT", "httpStatus": 409,
  "hint": "re-review the updated diff, then approve again" }
```

Items apply one at a time, so a partial merge is normal — **there is no `merge_failed` status.**

- The request stays `open`; `mergeAttempts` records what each item did.
- What applied stays applied — re-merging it is a no-op.
- Show `mergeAttempts` instead of reporting a hard failure, or people re-merge blindly.

---

## Approval policies

readonly+ can read; only admin can write.

```bash
# who is gated, and how
curl -s $ORBITAL_URL/api/v1/approval-policies -H "Authorization: Bearer $TOKEN" \
  | jq '.[] | {id, namespace, types, allTypes, requiredApprovals, bypassRoles, enabled}'

# is THIS class gated, and may I bypass it?  (labels a Save vs Propose button)
curl -s "$ORBITAL_URL/api/v1/approval-policies/resolve?namespace=colo&type=Server" -H "Authorization: Bearer $TOKEN"
```

```json
{ "required": true, "requiredApprovals": 1, "bypassRoles": ["admin"], "callerMayBypass": false }
```

```bash
# create (admin) — one policy per namespace, holding its own list of types
curl -s -X POST $ORBITAL_URL/api/v1/approval-policies -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{ "namespace":"colo", "types":["Server","ServerMaintenance"],
      "requiredApprovals":1, "bypassRoles":["admin"], "enabled":true }'

# ...or protect the WHOLE namespace, including types added to the schema later
curl -s -X POST $ORBITAL_URL/api/v1/approval-policies -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{ "namespace":"colo", "allTypes":true, "requiredApprovals":1 }'

# stop enforcing WITHOUT losing the config (admin)
curl -s -X PATCH $ORBITAL_URL/api/v1/approval-policies/<id> -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"enabled":false}'

curl -s -X DELETE $ORBITAL_URL/api/v1/approval-policies/<id> -H "Authorization: Bearer $TOKEN"
```

- Send exactly one of `types` or `allTypes` — both, or neither, is refused `400`. Omitting `types` does *not* mean "all".
- `bypassRoles: []` means nobody bypasses, including admins. Omitting the field defaults to `["admin"]`.
- Bypass is a property of the policy, not the user — there are no per-user capability flags.
- Enforcement is per policy; there is no global on/off. `enabled: false` stops one policy without losing its settings.
- No advisory/dry-run mode yet.

---

## When a write is gated

A write to a gated class is refused — whether it arrives via `/graphql` or internally (divergence accept):

```bash
curl -s -X POST $ORBITAL_URL/graphql -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"query":"mutation($orbId:String!,$set:IdracSettingsPatch!){ updateIdracSettings(input:{filter:{orbId:{eq:$orbId}},set:$set}){ numUids } }",
       "variables":{"orbId":"colo:CWJHDX3-idrac","set":{"sshEnabled":true}}}'
```

```json
{ "error": "changes to Server in colo require approval",
  "code": "APPROVAL_REQUIRED", "httpStatus": 403,
  "hint": "open a change request: POST /api/v1/change-requests" }
```

**Nothing was written.** Re-send the same edit as a change request. A caller in `bypassRoles` writes through instead, and that write is recorded as privileged in the audit log.

---

## Related endpoints

### Audit log — privileged writes and policy changes

A privileged write lands in the audit log, not just a log line:

```bash
# every write that skipped review
curl -s "$ORBITAL_URL/api/v1/audit-log?limit=100" -H "Authorization: Bearer $TOKEN" \
  | jq '.events[] | select(.details.privileged == true) | {actor, operationName, resourceIds, bypassedPolicy: .details.bypassedPolicy, at: .createdAt}'

# policy administration (create/update/delete each write a management event)
curl -s "$ORBITAL_URL/api/v1/audit-log?operation_name=createApprovalPolicy" -H "Authorization: Bearer $TOKEN"
curl -s "$ORBITAL_URL/api/v1/audit-log?resource_id=colo" -H "Authorization: Bearer $TOKEN"
```

Policy events carry `before`/`after` of every field, plus `enforcementStopped` when a policy is turned off. A refused write records nothing.

### Divergence accept — gated like any write

Accepting a divergence dispatches a real intent write, so it hits the same gate. On refusal it opens a change request pre-filled from the entry:

```bash
curl -s -X PUT $ORBITAL_URL/api/v1/divergences/<id>/resolution -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"action":"accept"}'
```

```json
{ "status": 202, "changeRequestId": "colo-43" }
```

`202 Accepted`, and no resolution is recorded — intent has not moved yet. Reject and Ignore are never gated: neither touches intent.

### Proposed changes — field-level overlay

`GET /api/v1/proposed-changes?orbId=…` — which fields have a proposal pending, for overlaying on an entity page. Covered in `docs/api-cheatsheet.md` § "Show what is already proposed".

---

## Error codes

| Code | Status | Meaning |
|---|---|---|
| `APPROVAL_REQUIRED` | 403 | Gated class, no bypass. Nothing written — open a change request |
| `MVCC_CONFLICT` | 409 | Base moved since review. Re-review the diff, approve again, re-merge |
| `TARGET_MISSING` | 409 | A target entity was deleted. Re-review won't fix it — close the request, `PATCH` out that item, or recreate the entity |
| `BAD_USER_INPUT` | 400 | Unknown `status` value, >128 `orbId`s, nested entity in a changeset, or a field the deployed schema does not have |
| `FORBIDDEN` | 403 | Role too low (e.g. `readonly` approving, non-admin writing a policy) |
