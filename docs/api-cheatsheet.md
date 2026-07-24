# Orbital API Cheatsheet

Common GraphQL queries and REST calls for services consuming Orbital's API (e.g. AEP). Every query below was validated against a live deployment.

## Basics

- **GraphQL endpoint:** `POST {BASE}/graphql` — one endpoint for all queries and mutations.
- **REST endpoints:** `POST {BASE}/api/v1/...` (operational triggers).
- **`{BASE}`** is the ingress host. Through the AKS internal ingress that's `http://ilb.devnew.armada.internal/orbital` (the `/orbital` prefix is required — Istio does not strip it). Through AEP's proxy, use the ingress hostname; the prefix is handled for you.
- **Auth:** `Authorization: Bearer <token>`. Both AEP's Keycloak token and an `orbctl login` (AAD) token are accepted.
- **`orbId`** is the stable public key for every ConfigItem (format `namespace:name`, e.g. `houston:houston-galleon`). Address entities by `orbId`, never by DGraph UID.

**Curl template** (all GraphQL examples use this shape):

```bash
curl -sS -X POST {BASE}/graphql \
  -H "Authorization: Bearer $T" \
  -H "Content-Type: application/json" \
  -d '{"query":"<query on one line, inner quotes \\\"escaped\\\">"}' | jq .
```

---

## 1. Find data centers by `assetDataV2`

`assetDataV2` is a JSON blob (regexp-searchable). Match a substring with `/pattern/`:

```graphql
query {
  queryDataCenter(filter: { assetDataV2: { regexp: "/kubevirt/" } }) {
    orbId
    name
    assetDataV2
  }
}
```

---

## 2. Kubernetes clusters for a data center

`KubernetesCluster` is a GraphQL **interface** — you must use inline fragments (`... on ConfigItem` for shared fields, `... on EksaKubernetesCluster` for provider-specific ones):

```graphql
query {
  getDataCenter(orbId: "houston:houston-galleon") {
    kubernetesClusters {
      __typename
      ... on ConfigItem { orbId name }
      ... on EksaKubernetesCluster { clusterType }   # management | workload | standalone
    }
  }
}
```

To address a single cluster directly by orbId, use the concrete type (no fragment needed):

```graphql
query {
  getEksaKubernetesCluster(orbId: "houston:g2-w1") { orbId name clusterType }
}
```

> Note: `kubernetesClusters` has **no `orbId` filter** (interface-edge limitation). Filter a specific cluster with `getEksaKubernetesCluster(orbId:)` instead of a filter arg.

---

## 3. Backup (etcd / velero) for a cluster

Traverse the forward `backup` edge from the cluster:

```graphql
query {
  getEksaKubernetesCluster(orbId: "colo:dev-main") {
    backup {
      orbId
      etcd   { orbId schedule enabled location }
      velero { orbId schedule enabled location }
    }
  }
}
```

A cluster with no backup configured returns `backup: null`. You can also address a `ClusterBackup` directly by its orbId (convention `<cluster-orbId>-backup`) with `getClusterBackup(orbId: "...")`.

---

## 4. Export (and publish) a data center's subgraph — REST

```bash
curl -sS -X POST {BASE}/api/v1/export \
  -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
  -d '{"orbId":"houston:houston-galleon"}'
# → 202 {"id":"<jobId>","status":"pending"}
```

- Body: `{"orbId":"...","download":false}`. `download` defaults to `false` = **export + publish to OCI**. `download:true` = export only, retains a downloadable zip.
- Poll to completion:

```bash
curl -sS {BASE}/api/v1/export/jobs/<jobId> -H "Authorization: Bearer $T" | jq '{status, phase, published}'
```

`status` walks `pending → running → completed` (or `failed`). On `completed`, `published:true` confirms the ConfigBundle reached the registry. `409` on trigger = an export/restore is already running (only one at a time — shared scratch DGraph).

---

## 5. Servers for a data center

`Server` is a concrete type — no fragment needed:

```graphql
query {
  getDataCenter(orbId: "houston:houston-galleon") {
    servers {
      orbId
      name
      hostname
      model
      serviceTag
      rackPosition
    }
  }
}
```

---

## 6. iDRAC settings for a server

```graphql
query {
  getServer(orbId: "houston:BB52FZ3") {
    orbId
    name
    idracSettings {
      orbId
      firmwareVersion
      sshEnabled
      ipmiEnabled
      lockdownModeEnabled
      dhcpEnabled
      racadmEnabled
      usbManagementPortEnabled
      osToIdracPassThroughEnabled
    }
  }
}
```

---

## Editing a field (mutation pattern)

Resolve the target's `orbId` (queries above), then scope an `update` to it by orbId. Example — change an etcd backup's cron:

```graphql
mutation {
  updateEtcdBackup(input: {
    filter: { orbId: { eq: "colo:dev-main-etcd-backup" } }
    set:    { schedule: "0 */6 * * *" }
  }) { numUids }
}
```

- Edit by `orbId` on the leaf type (`updateEtcdBackup`, `updateServer`, `updateIdracSettings`, …). Do **not** upsert or mutate through the parent for edits.
- Never set `version` — the server auto-increments it. Pass `ifVersion` for optimistic-concurrency protection on read-modify-write.
- Mutations are audited and attributed to the caller's token identity.
