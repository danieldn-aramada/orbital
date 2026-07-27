# Orbital API Cheatsheet

GraphQL queries + REST calls for API clients (e.g. AEP). All validated against a live deployment.

## Setup

- Base URL (dev): http://ilb.devnew.armada.internal/orbital
  - GraphQL: /graphql
  - REST: /api/v1
- `orbId` = `namespace:name`
  - the stable key in Orbital
  - safe to cache if need (don't cache DGraph UID)
- Auth — every request needs `Authorization: Bearer <token>`:
  - **AEP / Keycloak:** forward the user's existing bearer token 
  - **orbctl / AAD:** `orbctl login`; any command run with `-v` prints the access token

```bash
# set these once — every example below uses them
export ORBITAL_URL=http://ilb.devnew.armada.internal/orbital
export TOKEN=<your bearer token>

curl -sS -X POST $ORBITAL_URL/graphql -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"query":"…"}' | jq .
```

---

## Queries

### Data centers by asset ID

```graphql
query {
  queryDataCenter(filter: { assetDataV2: { regexp: "/<asset_id>/" } }) {
    orbId name assetDataV2
  }
}
```
- Wrap the pattern in `/…/` — DGraph errors without the slashes. Substring-matches the JSON blob.

### Clusters in a data center — one provider

```graphql
query {
  getDataCenter(orbId: "houston:houston-galleon") {
    kubernetesClusters(filter: { provider: { eq: "eksa" } }) {
      ... on EksaKubernetesCluster {
        orbId name clusterType
        controlPlaneEndpoint { address }
        tinkerbellIP { address }
      }
    }
  }
}
```
- `provider` → `eq` (or `in: ["eksa","maas"]` for several).
- `orbId`/`name` must sit **inside** the `... on` fragment — the interface edge can't select them directly.

### Clusters — all providers, or one by orbId

```graphql
# all (mixed types) — ... on ConfigItem gives every entry a baseline; others return {}
query { getDataCenter(orbId: "houston:houston-galleon") {
  kubernetesClusters { __typename ... on ConfigItem { orbId name } ... on EksaKubernetesCluster { clusterType } }
} }

# single cluster by orbId (concrete type, no fragment)
query { getEksaKubernetesCluster(orbId: "houston:g2-w1") { orbId name clusterType } }
```

### Backup (etcd / velero) for a cluster

```graphql
query {
  getEksaKubernetesCluster(orbId: "colo:dev-main") {
    backup { orbId
      etcd   { orbId schedule enabled location retentionDays }
      velero { orbId schedule enabled location retentionDays }
    }
  }
}
```
- `backup: null` = none configured.

### Servers in a data center

```graphql
query {
  getDataCenter(orbId: "houston:houston-galleon") {
    servers { orbId name hostname model serviceTag rackPosition }
  }
}
```

### iDRAC settings for a server

```graphql
query {
  getServer(orbId: "houston:BB52FZ3") {
    orbId name
    idracSettings {
      orbId firmwareVersion sshEnabled ipmiEnabled lockdownModeEnabled
      dhcpEnabled racadmEnabled usbManagementPortEnabled osToIdracPassThroughEnabled
    }
  }
}
```

## Edit a field

```graphql
mutation {
  updateEtcdBackup(input: {
    filter: { orbId: { eq: "colo:dev-main-etcd-backup" } }
    set:    { schedule: "0 */6 * * *" }
  }) { numUids }
}
```
- Update the leaf type by `orbId` (`updateEtcdBackup`, `updateServer`, `updateIdracSettings`, …).
- Don't set `version` (auto-incremented); pass `ifVersion` to guard against lost updates.

## Export + publish a data center (REST)

```bash
# trigger — download:false (default) = export + publish to OCI; true = export-only zip
curl -sS -X POST $ORBITAL_URL/api/v1/export -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"orbId":"houston:houston-galleon"}'                 # → 202 {"id","status"}

# poll
curl -sS $ORBITAL_URL/api/v1/export/jobs/<jobId> -H "Authorization: Bearer $TOKEN" | jq '{status, phase, published}'
```
- `completed` + `published:true` = ConfigBundle in the registry. `409` = an export/restore is already running (one at a time).
