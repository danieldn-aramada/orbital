# Orbital API Cheatsheet — Change Control

Change requests and their review. Setup and auth: [`api-cheatsheet.md`](api-cheatsheet.md).

## Create

### Read the versions you will change

```bash
# curl
curl -s -X POST $ORBITAL_URL/graphql -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{"query":"{ getIdracSettings(orbId: \"colo:CWJHDX3-idrac\") { version sshEnabled } }"}' | jq .data

# response
{ "getIdracSettings": { "version": 7, "sshEnabled": false } }
```

### Open a change request

```bash
# curl
curl -s -X POST $ORBITAL_URL/api/v1/change-requests -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{
  "title": "Enable SSH + maintenance on CWJHDX3",
  "namespace": "colo",
  "changes": [
    { "op": "update", "orbId": "colo:CWJHDX3-idrac",              "set": { "sshEnabled": true }, "version": 7 },
    { "op": "update", "orbId": "colo:server-maintenance-CWJHDX3", "set": { "enabled": true },    "version": 3 }
  ]
}' | jq '{id, status, requiredApprovals}'

# response
{ "id": "colo-42", "status": "open", "requiredApprovals": 1 }
```

| Item field | |
|---|---|
| `op` | `upsert` · `update` · `delete` |
| `version` | the version you read. Omitted: unconditional. Moved: `409`. Sent on a create: `400` |
| `set` / `clear` | fields to write / field names to unset |
| `type` | only when creating an entity that does not exist yet |

Link edges by reference, never nested: `"dataCenter": { "orbId": "colo:dc-01" }`.

## Amend

Author, or a role in the policy's `bypassRoles`. `availableActions` contains `edit` for whoever may.

### Replace the changeset — dismisses approvals

```bash
# curl
curl -s -X PATCH $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{
  "namespace": "colo",
  "changes": [
    { "op": "update", "orbId": "colo:CWJHDX3-idrac", "set": { "sshEnabled": true }, "version": 7 }
  ]
}' | jq '{id, status, approvals}'
```

To drop an item, `PATCH` without it. Dropping all of them is `400` — close the request instead.

### Rename only — keeps approvals

```bash
# curl
curl -s -X PATCH $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"title":"Enable SSH on CWJHDX3"}'
```

## Review

### Approve, reject, merge, close

```bash
# curl
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/approve -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"comment":"Checked against the maintenance window."}'
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/reject  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"comment":"Wrong host."}'
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/merge   -H "Authorization: Bearer $TOKEN"
curl -s -X POST $ORBITAL_URL/api/v1/change-requests/colo-42/close   -H "Authorization: Bearer $TOKEN"
```

- Render buttons from `availableActions`. It is per caller, and already accounts for role, authorship and prior approvals.
- The approver cannot be the author, unless their role is in `bypassRoles`.
- A rejection closes the request.
- `approved` is derived, not stored: it reverts to `open` when the changeset is amended.
- Approving does not clear staleness. A request can be `approved` and stale at once, and cannot merge.
- Merge applies items one at a time. A partial merge leaves the request `open` with `mergeAttempts` filled in; re-merging skips what already applied.

### Read who decided what

`reviews` is each approver's current decision. `reviewHistory` is every decision, oldest first — including ones the approver later replaced.

```bash
# curl
curl -s $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" | jq '.reviewHistory'

# response
[
  { "approver": "reviewer@armada.ai", "decision": "approved", "comment": "first look",
    "revision": 1, "at": "2026-10-23T10:13:12Z", "superseded": true,  "current": false },
  { "approver": "reviewer@armada.ai", "decision": "approved", "comment": "after the amend",
    "revision": 2, "at": "2026-10-23T10:13:35Z", "superseded": false, "current": true }
]
```

`current` means the approval still counts toward merging. `superseded` means the same approver decided again later.

## Staleness

A request is stale when an entity it changes has a newer `version` than the one reviewed. A stale request cannot merge; the author rebases it.

### Check

```bash
# curl
curl -s $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" \
  | jq '{stale, staleEntities, approvals, requiredApprovals, availableActions, missingTargets}'

# response
{ "stale": true,
  "staleEntities": [ { "orbId": "colo:server-maintenance-CWJHDX3", "reviewedVersion": 1, "currentVersion": 2 } ],
  "approvals": 0, "requiredApprovals": 1,
  "availableActions": ["approve", "reject"], "missingTargets": [] }
```

`missingTargets` is different: the entity was deleted, and no rebase helps.

### Rebase

Re-read what moved, then `PATCH` the changeset with the new versions. Approvals are dismissed.

```bash
# 1. what moved
curl -s $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" | jq '.staleEntities'
# [ { "orbId": "colo:CWJHDX3-idrac", "reviewedVersion": 3, "currentVersion": 4 } ]

# 2. re-read it — take their value, or keep yours
curl -s -X POST $ORBITAL_URL/graphql -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"query":"{ getIdracSettings(orbId: \"colo:CWJHDX3-idrac\") { version sshEnabled } }"}' | jq .data

# 3. PATCH with the new version
curl -s -X PATCH $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{ "namespace":"colo", "changes":[ { "op":"update", "orbId":"colo:CWJHDX3-idrac", "set":{"sshEnabled":true}, "version":4 } ] }'

# 4. confirm
curl -s $ORBITAL_URL/api/v1/change-requests/colo-42 -H "Authorization: Bearer $TOKEN" | jq '{stale, approvals}'
# { "stale": false, "approvals": 0 }
```

## Diff

### What merging would change

```bash
# curl
curl -s $ORBITAL_URL/api/v1/change-requests/colo-21/diff -H "Authorization: Bearer $TOKEN" \
  | jq '{summary, changes, satisfied}'

# response
{ "summary": { "added": 0, "removed": 0, "modified": 1, "unchanged": 0 },
  "changes": [
    { "orbId": "colo:dev-main-velero-backup", "type": "VeleroBackup", "change": "modified",
      "fields": [ { "field": "VeleroBackup.retentionDays", "before": 17, "after": 18 } ] } ],
  "satisfied": [
    { "orbId": "colo:dev-main-velero-backup", "type": "VeleroBackup", "change": "satisfied",
      "fields": [ { "field": "VeleroBackup.location", "before": "https://…/all-namespaces-v2", "after": "https://…/all-namespaces-v2" } ] } ] }
```

Render `changes` — that is the diff. `satisfied` is the part that would do nothing because the value already matches; hide or grey it. Each field also carries `outcome: applies | satisfied`.

## Find requests

```bash
# curl
curl -s "$ORBITAL_URL/api/v1/change-requests?status=active&orbId=colo:CWJHDX3-idrac" -H "Authorization: Bearer $TOKEN" \
  | jq '{total, items: [.items[] | {id, effect, author, status}]}'

curl -s "$ORBITAL_URL/api/v1/change-requests?awaitingReview=true" -H "Authorization: Bearer $TOKEN" | jq .total
```

| Filter | |
|---|---|
| `status` | `open` `approved` `active` `rejected` `merged` `closed` — repeatable, OR-ed |
| `orbId` | repeatable, OR-ed, max 128 |
| `namespace` | repeatable, OR-ed |
| `type` | ConfigItem type any item targets; repeatable; an interface (`KubernetesCluster`) matches its implementations |
| `createdAt_gte` · `createdAt_lte` | RFC3339 window; also `updatedAt_*`, `executedAt_*` (merge time); `_gt` / `_lt` exclusive |
| `author` · `mine` · `awaitingReview` | |
| `limit` · `offset` | paging after filtering; `total` counts every match |

- Use `status=active`, not `open`. `approved` is derived, so `open` alone misses approved-but-unmerged requests.
- An edit to an owned child is filed under the child's orbId.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| `APPROVAL_REQUIRED` | 403 | Gated class, no bypass. Nothing written — open a change request |
| `MVCC_CONFLICT` | 409 | The `version` you sent no longer matches, or the request's base moved since review |
| `WRITE_CONTENTION` | 503 | Repeated transaction aborts. Not stale — retry unchanged |
| `TARGET_MISSING` | 409 | A target entity was deleted. Close the request, `PATCH` out that item, or recreate the entity |
| `BAD_USER_INPUT` | 400 | Malformed changeset, unknown `status`, more than 128 `orbId`s, or a `version` orbital cannot apply |
| `FORBIDDEN` | 403 | Role too low |
