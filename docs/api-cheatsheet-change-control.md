# Orbital API Cheatsheet — Change Control

## 1. Create

Fetch current `version` of each node you intend to change:

```bash
curl -s -X POST $ORBITAL_URL/graphql -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{"query":"{ getIdracSettings(orbId: \"colo:CWJHDX3-idrac\") { version sshEnabled } }"}' | jq .data
# { "getIdracSettings": { "version": 7, "sshEnabled": false } }
```
Then create change request

```bash
curl -s -X POST $ORBITAL_URL/api/v1/change-requests -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{
  "title": "Enable SSH + maintenance on CWJHDX3",
  "namespace": "colo",
  "changes": [
    { "op": "update", "orbId": "colo:CWJHDX3-idrac",              "set": { "sshEnabled": true }, "version": 7 },
    { "op": "update", "orbId": "colo:server-maintenance-CWJHDX3", "set": { "enabled": true },    "version": 3 }
  ]
}' | jq '{id, status, requiredApprovals}'
# { "id": "colo-42", "status": "open", "requiredApprovals": 1 }
```

| | |
|---|---|
| `op` | `upsert` · `update` · `delete` |
| `version` | the entity's version as you read it. Omit ⇒ unconditional. Moved ⇒ `409`. On a create ⇒ `400` |
| `set` / `clear` | fields to write / field names to unset |
| `type` | only when creating an entity that does not exist yet |

Edges by reference, never nested: `"dataCenter": { "orbId": "colo:dc-01" }`.

## 2. Update

`PATCH` replaces the changeset. Requires re-approval if approved.

```bash
curl -s -X PATCH $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{
  "namespace": "colo",
  "changes": [
    { "op": "update", "orbId": "colo:CWJHDX3-idrac", "set": { "sshEnabled": true }, "version": 7 }
  ]
}' | jq '{id, status, approvals}'
```

```bash
# rename only — leaves the changeset, base and approvals untouched
curl -s -X PATCH $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"title":"Enable SSH on CWJHDX3"}'
```

Author only (or a `bypassRoles` role).

## 3. Approve / reject / merge

```bash
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/approve -H "Authorization: Bearer $TOKEN"
```

- Approver ≠ author, unless their role is in `bypassRoles`.
- Approving does **not** clear staleness — only the author rebasing does. A request can be `approved` and `stale` at once, and cannot merge.
- `approved` is derived, not stored — it reverts to `open` when the changeset is amended.
- Merge applies items one at a time; a partial merge leaves the request `open` with `mergeAttempts` filled in. Re-merging what applied is a no-op.

Render buttons from `availableActions` — it is caller-relative and already accounts for role, authorship and prior approvals.

## 4. Check staleness

A change request is stale if it contains any nodes whose `version` no longer matches current. It must be resolved by the author. A stale change request cannot be merged.
```bash
curl -s $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" \
  | jq '{stale, staleEntities, approvals, requiredApprovals, availableActions, missingTargets}'
```
Example response
```json
{ "stale": true,
  "staleEntities": [ { "orbId": "colo:server-maintenance-CWJHDX3", "reviewedVersion": 1, "currentVersion": 2 } ],
  "approvals": 0, "requiredApprovals": 1,
  "availableActions": ["approve","reject"], "missingTargets": [] }
```
Stale change request must be rebased by author.

### Rebase a stale request

Author (or a `bypassRoles` role) re-reads what moved and `PATCH`es the changeset with the new versions.

```bash
# 1. fetch orbIds of nodes that are stale
curl -s $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" | jq '.staleEntities'
# [ { "orbId": "colo:CWJHDX3-idrac", "reviewedVersion": 3, "currentVersion": 4 } ]

# 2. re-read it — take their value, or keep yours
curl -s -X POST $ORBITAL_URL/graphql -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"query":"{ getIdracSettings(orbId: \"colo:CWJHDX3-idrac\") { version sshEnabled } }"}' | jq .data

# 3. PATCH with the new version (and a new proposed value if you want one)
curl -s -X PATCH $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{ "namespace":"colo", "changes":[ { "op":"update", "orbId":"colo:CWJHDX3-idrac", "set":{"sshEnabled":true}, "version":4 } ] }'

# 4. confirm
curl -s $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" | jq '{stale, approvals}'
# { "stale": false, "approvals": 0 }
```

To drop a change object instead, `PATCH` with it omitted. Dropping all of them is `400` — close the request.

Either way approvals are dismissed and the request needs re-approval.

Only the author (or a `bypassRoles` role) can do this. `availableActions` carries `edit` for whoever may.

`missingTargets` is different: the entity was deleted, and no rebase helps.

---

### Find requests

```bash
curl -s "$ORBITAL_URL/api/v1/change-requests?status=active&orbId=colo:CWJHDX3-idrac" -H "Authorization: Bearer $TOKEN" \
  | jq '{total, items: [.items[] | {id, effect, author, status}]}'

curl -s "$ORBITAL_URL/api/v1/change-requests?awaiting_review=true" -H "Authorization: Bearer $TOKEN" | jq .total
```

| Filter | |
|---|---|
| `status` | `open` `approved` `active` `rejected` `merged` `closed` — repeatable, OR-ed |
| `orbId` | repeatable, OR-ed, max 128 |
| `namespace` · `author` · `mine` · `awaiting_review` | |

Use `status=active`, not `open` — `approved` is derived, so `open` alone misses approved-but-unmerged. An edit to an owned child files under the *child's* orbId.


## Error codes

| Code | Status | Meaning |
|---|---|---|
| `APPROVAL_REQUIRED` | 403 | Gated class, no bypass. Nothing written — open a change request |
| `MVCC_CONFLICT` | 409 | The `version` you sent no longer matches, or a request's base moved since review |
| `WRITE_CONTENTION` | 503 | Repeated transaction aborts. Not stale — retry unchanged |
| `TARGET_MISSING` | 409 | A target entity was deleted. Close the request, `PATCH` out that item, or recreate the entity |
| `BAD_USER_INPUT` | 400 | Malformed changeset, unknown `status`, >128 `orbId`s, or a `version` orbital cannot apply |
| `FORBIDDEN` | 403 | Role too low |
