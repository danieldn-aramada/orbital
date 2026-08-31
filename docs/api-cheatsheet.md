# Orbital API Cheatsheet

Copy-paste GraphQL + REST for backend services talking to Orbital on **AKS dev** (external-jwt mode). Deeper context lives in `docs/reference/`.

## Setup

- Base URL (AKS dev): `http://ilb.devnew.armada.internal/orbital`
  - GraphQL: `/graphql`
  - REST: `/api/v1`
  - Swagger — authoritative reference for every REST endpoint's params (types, required/optional): `$ORBITAL_URL/swagger/index.html`
- `orbId` = `namespace:name` — the stable key (don't cache DGraph UIDs)
- Auth: every request needs `Authorization: Bearer <token>`
  - AEP / Keycloak: forward the user's bearer token

```bash
export ORBITAL_URL=http://ilb.devnew.armada.internal/orbital
export TOKEN=<your bearer token>

# smoke test — lists data centers; confirms your token + connectivity
curl -s -X POST $ORBITAL_URL/graphql -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{"query":"query { queryDataCenter { orbId name } }"}' | jq .
```

**Reads** send only `query`. **Mutations** send two fields — `query` **and** `variables`: the `query` holds the operation with `$orbId`/`$set` placeholders; `variables` holds the values. Always put `orbId` (and the `set` payload) in **`variables`**, never inline in the query. A single-entity `update{Kind}` with an inline `orbId` or `set` is **rejected with `400 VARIABLE_FORM_REQUIRED`** — orbital stamps `version`/`updatedAt`/`updatedBy` into a variable `set` resolved via a variable `orbId`, and can't do that against inline literals. Reads with inline filters are fine.

The `-d` body is JSON and may span multiple lines for readability — only the `query` string stays on one line. A mutation's returned entity confirms the write: the server stamps `updatedAt`/`updatedBy` (**no milliseconds**) and bumps `version`.

### Clearing a field — use `remove` (with a `set`)

Orbital's `/graphql` is DGraph GraphQL. To **clear** a field, use the update input's **`remove`** — `set: { field: null }` is a DGraph **no-op** (nulls in `set` are silently ignored, the value stays), and `set: { field: "" }` is **rejected** on typed scalars like `DateTime`. Two rules:

- **`remove` matches on the value** — pass the field's **current** value, so read it first, then remove it.
- **Keep a variable `set`** (even `{}`) alongside `remove`. A `remove`-only mutation is rejected `400 VARIABLE_FORM_REQUIRED`, and the variable `set` is what orbital stamps `version`/`updatedAt`/`updatedBy` into.

```graphql
# 1. read the current value(s)
query { getServerMaintenance(orbId: "ns:server-maintenance-<serial>") { windowStart windowEnd } }

# 2. clear them — remove takes the current values; set can be empty
mutation Clear($orbId: String!, $set: ServerMaintenancePatch!, $remove: ServerMaintenancePatch) {
  updateServerMaintenance(input: { filter: { orbId: { eq: $orbId } }, set: $set, remove: $remove }) { numUids }
}
# variables:
#   { "orbId": "ns:server-maintenance-<serial>",
#     "set": {},
#     "remove": { "windowStart": "<current windowStart>", "windowEnd": "<current windowEnd>" } }
```

The full request body on the wire (what the UI editor sends — kept fields go in `set`, cleared fields in `remove`):

```json
{
  "query": "mutation UpdateServerMaintenance($orbId: String!, $set: ServerMaintenancePatch!, $remove: ServerMaintenancePatch) { updateServerMaintenance(input: { filter: { orbId: { eq: $orbId } }, set: $set, remove: $remove }) { serverMaintenance { orbId } } }",
  "variables": {
    "orbId": "colo:server-maintenance-CWJHDX3",
    "set":    { "enabled": true, "reason": "test" },
    "remove": { "windowStart": "2026-08-14T18:00:00Z", "windowEnd": "2026-08-14T22:00:00Z" }
  }
}
```

Orbital's UI editor does this read-then-remove automatically; API callers do it explicitly.

---

## Example schema 

Focused on iDRAC settings and backup config, not the full schema.

<p align="center"><img src="example-schema.png"/></p>

## Data centers

### Look up a data center by asset ID
```graphql
query { queryDataCenter(filter: { assetDataV2: { regexp: "/<asset_id>/" } }) { orbId name assetDataV2 } }
```

## Servers

### List servers in a data center
```graphql
query {
  getDataCenter(orbId: "houston:houston-galleon") {
    servers { orbId name hostname model serviceTag rackPosition }
  }
}
```

### Fetch a server's iDRAC settings — by server orbId
```graphql
query {
  getServer(orbId: "houston:BB52FZ3") {
    orbId name
    idracSettings {
      orbId sshEnabled ipmiEnabled dhcpEnabled racadmEnabled
    }
  }
}
```

### Update iDRAC settings — by the `idracSettings.orbId` from the fetch above
query
```graphql
mutation UpdateIdracSettings($orbId: String!, $set: IdracSettingsPatch!) {
  updateIdracSettings(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids }
}
```
variables
```json
{ "orbId": "houston:BB52FZ3-idrac", "set": { "sshEnabled": false } }
```

## Clusters

### List EKSA clusters in a data center
```graphql
query {
  getDataCenter(orbId: "houston:houston-galleon") {
    kubernetesClusters {
      ... on EksaKubernetesCluster { orbId name clusterType provider }
    }
  }
}
```

### Fetch a cluster's backup config — by cluster orbId
```graphql
query {
  getEksaKubernetesCluster(orbId: "colo:dev-main") {
    backup {
      orbId
      etcd   { orbId schedule enabled location retentionDays }
      velero { orbId schedule enabled location retentionDays }
    }
  }
}
```

### Update a backup config — by the `etcd`/`velero` orbId from the fetch above
query
```graphql
mutation UpdateEtcdBackup($orbId: String!, $set: EtcdBackupPatch!) {
  updateEtcdBackup(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids }
}
```
variables
```json
{ "orbId": "colo:dev-main-etcd-backup", "set": { "schedule": "0 */6 * * *", "retentionDays": 7 } }
```

Example using curl
```bash
curl -s -X POST $ORBITAL_URL/graphql -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
-d '{
  "query": "mutation UpdateVeleroBackup($orbId: String!, $set: VeleroBackupPatch!) { updateVeleroBackup(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids veleroBackup { orbId retentionDays version updatedAt updatedBy } } }",
  "variables": { "orbId": "colo:dev-main-velero-backup", "set": { "retentionDays": 14 } }
}' | jq .
```
Response
```json
{
  "data": {
    "updateVeleroBackup": {
      "numUids": 1,
      "veleroBackup": [
        {
          "orbId": "colo:dev-main-velero-backup",
          "retentionDays": 14,
          "version": 30,
          "updatedAt": "2026-07-28T19:13:50Z",
          "updatedBy": "daniel.nguyen@armada.ai"
        }
      ]
    }
  }
}
```
`updatedAt` has no milliseconds and `updatedBy` is the authenticated caller — both server-stamped (the client never sent them).

## Network devices

NICs are modeled as `NetworkAdapter` (the card) → `NetworkInterface` (the interface, carries the MAC + `linkSpeedMbps`), and switches/firewalls/routers as `NetworkDevice`. The **BMC/iDRAC is a `NetworkInterface` too** (`mgmtOnly: true`, **no** `networkAdapter` — it's a Redfish *Manager*, not a NIC card), owned by the server directly. Adapters and interfaces are **read-only** (Redfish-sourced, owned by the server); only `NetworkDevice` is directly mutable.

- **Redfish convention (server side).** `NetworkAdapter` = Redfish/DMTF `NetworkAdapter` (the physical NIC card / FRU); `NetworkInterface` collapses Redfish `Port` ⇄ `EthernetInterface` (1:1, joined on MAC). A port links to its `Server` **directly** — mirroring Redfish's `EthernetInterface`-under-`ComputerSystem` — *and* to its card via `networkAdapter`, mirroring `Port`-under-`NetworkAdapter`. The card layer is optional (absent for LAGs, NetBox-only sources, and the BMC/iDRAC). `mgmtOnly: true` flags the management-plane interface (the BMC), mirroring NetBox `interface.mgmt_only`; `macAddress` is an indexed field (not a node) — the cross-source join key.
- **NetBox convention (network side).** `NetworkDevice` = the DCIM *device + `role`* (`tor` / `firewall` / `router` / …), enumerated from a site's NetBox device inventory. `connectedNetworkDevice` / `connectedNetworkDevicePort` mirror NetBox `connected_endpoints` — a *cabled-to* reference (singular per physical port), **not** ownership. `macAddress` is the cross-source join key between Redfish (server side) and NetBox (switch side).

### List a server's NICs (data + management) — by server orbId
```graphql
query {
  getServer(orbId: "colo:CFRHDX3") {
    orbId name
    # data NICs — grouped under their NetworkAdapter (the card)
    networkAdapters {
      orbId name manufacturer model serialNumber
      networkInterfaces {
        orbId name macAddress portType linkSpeedMbps
        connectedNetworkDevicePort
        connectedNetworkDevice { orbId name role }
      }
    }
    # BMC / iDRAC — a mgmtOnly interface with no adapter; filter to the mgmt plane
    networkInterfaces(filter: { mgmtOnly: true }) {
      orbId name macAddress portType linkSpeedMbps mgmtOnly
    }
  }
}
```

### List network devices in a data center — by DC orbId
```graphql
query {
  getDataCenter(orbId: "colo:colo-galleon") {
    networkDevices { orbId name manufacturer model serial role macAddress }
  }
}
```

### Fetch one device + its connected servers (blast radius) — by device orbId
```graphql
query {
  getNetworkDevice(orbId: "colo:network-device-XH3123090344") {
    orbId name manufacturer model serial role macAddress
    dataCenter { orbId name }
    networkInterfaceConnectedNetworkDevice {
      connectedNetworkDevicePort
      networkAdapter { server { orbId name } }
    }
  }
}
```

### Edit a network device — by its orbId (variable form, required)
query
```graphql
mutation UpdateNetworkDevice($orbId: String!, $set: NetworkDevicePatch!) {
  updateNetworkDevice(input: { filter: { orbId: { eq: $orbId } }, set: $set }) {
    numUids networkDevice { orbId role version updatedBy }
  }
}
```
variables
```json
{ "orbId": "colo:network-device-XH3123090344", "set": { "role": "core" } }
```

### Add a new network device — orbital stamps `version`/`createdBy`/`createdAt`
Pass the input as a **variable** (array-of-maps) so the proxy injects `version: 1` and stamps the create-metadata; `dataCenter` links to the existing DC by `orbId`.
query
```graphql
mutation AddNetworkDevice($input: [AddNetworkDeviceInput!]!) {
  addNetworkDevice(input: $input) {
    numUids networkDevice { orbId version createdBy createdAt }
  }
}
```
variables
```json
{ "input": [ {
  "orbId": "colo:network-device-JX3623130496",
  "namespace": "colo",
  "name": "Colo_OOB_SW1",
  "manufacturer": "Juniper",
  "model": "EX2300-48T",
  "serial": "JX3623130496",
  "role": "tor",
  "macAddress": "C8:13:37:AA:F0:27",
  "dataCenter": { "orbId": "colo:colo-galleon" }
} ] }
```

## Audit log (REST)

Every intent mutation is recorded as an immutable audit event. Read them at `GET /api/v1/audit-log` (JSON). Events are written by orbital as a side effect of the mutation.

Filter by the resource's `orbId`. For example, to see the trail for the velero backup edits:
```bash
curl -s "$ORBITAL_URL/api/v1/audit-log?orbId=colo:dev-main-velero-backup&operation_name=updateVeleroBackup&limit=50" \
  -H "Authorization: Bearer $TOKEN" | jq .
```
Example response
```json
{
  "events": [
    {
      "id": "e671bfb1-88b8-4af1-881d-9a268b39150c",
      "operations": [
        "updateVeleroBackup"
      ],
      "resourceTypes": [
        "VeleroBackup"
      ],
      "resourceIds": [
        "colo:dev-main-velero-backup"
      ],
      "actor": "admin@armada.ai",
      "timestamp": "2026-07-29T19:40:50Z",
      ... 
      "changes": [
        {
          "field": "retentionDays",
          "before": 14,
          "after": 13
        }
      ]
    }
  ],
  "total": 37
}
```

All filters are **optional** and **combinable** — omit them all for the full log (newest first)
- `orbId=colo:dev-main-velero-backup` — events touching a resource (repeatable, max 32)
- `operation_name=updateVeleroBackup` — one operation (must match a value in the event's `operations` array, e.g. `updateVeleroBackup`)
- `namespace=colo` — everything under a data center (`colo:*`)
- `since` / `until` — RFC3339 window; `limit` / `offset` — pagination (`limit` ≤ 500)

### Response shape + rendering a diff
```json
{
  "events": [
    {
      "id": "3d6bb15f-8c4c-45f0-8a6c-939b6f9cc512",
      "timestamp": "2026-07-28T19:13:50Z",
      "actor": "asharma@armada.ai",
      "operations": ["updateVeleroBackup"],
      "resourceTypes": ["VeleroBackup"],
      "resourceIds": ["colo:dev-main-velero-backup"],
      "eventCategory": "data",
      "changes": [ { "field": "retentionDays", "before": 7, "after": 15 } ],
      "details": { "operationName": "UpdateVeleroBackup", "before": { … }, "variables": { "set": { … } } }
    }
  ],
  "total": 42
}
```
Diff can be rendered via `changes`. Each entry is `{ field, before, after }` with raw typed values (numbers, bools, strings). 

`changes` is present only for a clean single-entity update. It is omitted for bulk adds, creates, and multi-operation events (which have no single before/after). Its presence is the signal — no need to inspect `operations` or `before`:

```js
if (event.changes) event.changes.forEach(c => renderRow(c.field, c.before, c.after))
else               renderSummary(event.operations, event.resourceIds)   // no field diff available
```

The raw `details` (`before` + `variables.set`) is always included if you'd rather compute the diff yourself — but `changes` already does it, matching exactly what orbital's own UI renders. 


## Export + publish a data center (REST)
```bash
# trigger (download:false = export + publish to OCI)
curl -s -X POST $ORBITAL_URL/api/v1/export -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"orbId":"houston:houston-galleon"}'

# poll
curl -s $ORBITAL_URL/api/v1/export/jobs/<jobId> -H "Authorization: Bearer $TOKEN" | jq '{status, phase, published}'
```


## Show what is already proposed (REST + GraphQL, in parallel)

Two calls, joined on `orbId`. `orbId` is `@id` on the ConfigItem interface — globally unique
across every type — so the overlay is a map lookup, not a correlation. Issue them in parallel;
they have no dependency on each other.

```bash
# 1. the entities you are rendering, and their owned children
curl -s $ORBITAL_URL/graphql -H "Content-Type: application/json" -d '{"query":"
  { getServer(orbId:\"colo:server-CWJHDX3\") {
      orbId hostname
      serverMaintenance { orbId enabled reason }
    } }"}'

# 2. what is proposed for any of those orbIds (repeatable, max 128)
curl -s "$ORBITAL_URL/api/v1/proposed-changes\
?orbId=colo:server-CWJHDX3&orbId=colo:server-maintenance-CWJHDX3" -H "Authorization: Bearer $TOKEN"
```

```json
{ "colo:server-maintenance-CWJHDX3": {
    "type": "ServerMaintenance",
    "fields": { "enabled": {
      "conflicting": false,
      "proposals": [{ "changeRequestId": "colo-42", "status": "open", "op": "update",
                      "value": true, "approvals": 0, "requiredApprovals": 1,
                      "author": "dev@armada.ai", "createdAt": "2026-08-30T22:14:03Z" }] } } } }
```

Rendering it is one lookup — no walk, no grouping, no conflict comparison; orbital did those:

```js
const p = proposals[node.orbId]?.fields['enabled']
```

Two rules worth copying from orbital's own UI, because the API cannot do either for you:

- **Suppress no-ops.** This endpoint reads PostgreSQL only, so it does not know current values.
  A proposal whose `value` already equals what you rendered changes nothing — drop the mark
  (keep the request in your banner; it still needs closing or merging).
- **Recompute `conflicting` over what survives.** The flag counts every proposal. Two proposals
  of which one is already true leave one live claim and no conflict.

`status` is derived from the approval count against the request's stored base — a display hint,
not a merge verdict. Merge re-checks against live intent and can still refuse with
`409 MVCC_CONFLICT`.

Which children to ask about is **your** decision, not orbital's: ask about exactly the orbIds you
rendered. Over 128 the request is refused (`400`), never silently truncated.
