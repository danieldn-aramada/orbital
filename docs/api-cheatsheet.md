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
```graphql
mutation {
  updateIdracSettings(input: {
    filter: { orbId: { eq: "houston:BB52FZ3-idrac" } }
    set:    { sshEnabled: false }
  }) { numUids }
}
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
```graphql
mutation {
  updateEtcdBackup(input: {
    filter: { orbId: { eq: "colo:dev-main-etcd-backup" } }
    set:    { schedule: "0 */6 * * *", retentionDays: 7 }
  }) { numUids }
}
```
Same shape for `updateVeleroBackup` (orbId `…-velero-backup`).

## Export + publish a data center (REST)
```bash
# trigger (download:false = export + publish to OCI)
curl -s -X POST $ORBITAL_URL/api/v1/export -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"orbId":"houston:houston-galleon"}'

# poll
curl -s $ORBITAL_URL/api/v1/export/jobs/<jobId> -H "Authorization: Bearer $TOKEN" | jq '{status, phase, published}'
```
