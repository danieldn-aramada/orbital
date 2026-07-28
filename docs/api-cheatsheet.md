# Orbital API Cheatsheet

Copy-paste GraphQL + REST for backend services talking to Orbital on **AKS dev** (external-jwt mode). Deeper context lives in `docs/reference/`.

## Setup

- Base URL (AKS dev): `http://ilb.devnew.armada.internal/orbital`
  - GraphQL: `/graphql`
  - REST: `/api/v1`
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

## Export + publish a data center (REST)
```bash
# trigger (download:false = export + publish to OCI)
curl -s -X POST $ORBITAL_URL/api/v1/export -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"orbId":"houston:houston-galleon"}'

# poll
curl -s $ORBITAL_URL/api/v1/export/jobs/<jobId> -H "Authorization: Bearer $TOKEN" | jq '{status, phase, published}'
```
