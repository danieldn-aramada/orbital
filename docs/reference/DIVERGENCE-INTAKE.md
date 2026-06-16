# Orb Divergence Intake API

> **Audience:** anyone building a producer that reports configuration divergence to orb. Today that's [cb-controller](../plans/divergence-cb-controller-contract.md); tomorrow it could be any edge component that knows what intent it was given and what it observes.

## Principle

Orb is a **producer-agnostic pipe** for divergence reports. It accepts orbital-native entries, stores them, publishes them. It has no built-in knowledge of any specific producer ecosystem (configbundle, K8s, Tinkerbell, …). All translation from producer-native vocabulary into orbital-native is the producer's responsibility.

See [`feedback_orb_orbital_agnostic_of_configbundle`](../../) — this principle reverses an earlier decision that put translation logic inside orb.

## `POST /api/v1/divergence`

The single intake endpoint. Replace-not-merge: every call carries the **full current divergence set** for this orb.

### Request

```http
POST /api/v1/divergence HTTP/1.1
Host: orb:8010
Content-Type: application/json

{
  "overrides": [
    {
      "orbId":         "colo:srv-001-idrac",
      "field":         "sshEnabled",
      "type":          "IdracSettings",
      "intendedValue": false,
      "overrideValue": true,
      "who":           "local:admin",
      "when":          "2026-06-11T14:00:00Z"
    }
  ]
}
```

### Response — 200

```json
{"stored": 1}
```

### Payload fields

| Field | Type | Required | Meaning |
|---|---|:---:|---|
| `overrides` | array | yes | Full current divergence set; may be empty |
| `overrides[].orbId` | string | yes | Orbital identifier of the ConfigItem (e.g. `colo:srv-001-idrac`) |
| `overrides[].field` | string | yes | Leaf field name on that ConfigItem (e.g. `sshEnabled`) |
| `overrides[].type` | string | yes | Orbital GraphQL type name (e.g. `IdracSettings`) — orbital uses this to dispatch `update{Type}` on Accept |
| `overrides[].intendedValue` | any (JSON) | yes | What orbital intent declared this field should be |
| `overrides[].overrideValue` | any (JSON) | yes | What the producer observed locally |
| `overrides[].who` | string | yes | Producer's label for the override source (e.g. `local:admin`) — opaque to orb, surfaced verbatim in orbital UI |
| `overrides[].when` | RFC3339 string | yes | When the producer first observed the override |

### Semantics

- **Replace-not-merge.** Every POST replaces orb's stored set. Entries that don't appear in the latest POST are considered resolved.
- **Empty array is meaningful.** `{"overrides": []}` says "no current divergences" — orb stores an empty set; orbital interprets that as "all clear for this DC on the next publish."
- **Idempotent.** Re-posting the same body produces the same stored state.
- **No bundle/digest concept.** Orb has no notion of the bundle (or other artifact) the report came from. Producers internally key their mapping however they want; only orbital-native entries cross the API boundary.

### What orb validates (structural)

- Body is valid JSON
- `overrides` is an array
- Every entry has all eight fields above present (presence check only)
- `when` parses as RFC3339

### What orb does NOT validate (orbital-domain concerns)

- That `orbId` refers to a real ConfigItem in orbital
- That `type` is a real orbital GraphQL type
- That `field` is real on `type`
- That `intendedValue` / `overrideValue` match the declared type of `field`
- Any business rules

Orb is air-gapped from orbital and cannot do these checks. Orbital validates on its ingestion side. Producers ship orbital-correct data.

### Errors

| Code | When | Body |
|---|---|---|
| 400 | Malformed JSON, missing required field, unparseable timestamp | `{"message": "..."}` |
| 500 | Disk write failure | `{"message": "failed to store report"}` |

There is no 422. Orb has no lookup that could fail at intake time.

## Companion endpoints

### `GET /api/v1/divergence`

Returns the currently stored set. Same shape as the request's `.overrides`.

```http
GET /api/v1/divergence HTTP/1.1

200 OK
[
  {"orbId": "...", "field": "...", "type": "...", "intendedValue": ..., "overrideValue": ..., "who": "...", "when": "..."}
]
```

### `POST /api/v1/divergence/publish`

Snapshots the current stored set into S3 (Azure Blob via the AWS SDK in production). Orbital's S3 poller consumes from there.

Returns 503 if S3 is not configured on this orb. Returns 200 with `{"key": "<s3-key>"}` on success.

## How producers fit in

Any producer that can answer "what fields am I watching, what's their intended value, what's their current value" can call this API. The producer's responsibilities:

1. **Maintain its own intent reference.** Whether by reading a manifest the producer last applied, querying a CRD, parsing a config file, or holding intent in memory — that's the producer's choice.
2. **Observe the current local state.** Walk a K8s `managedFields`, read a file, query a daemon — producer's choice.
3. **Translate to orbital-native.** If the producer's native vocabulary differs from orbital's (e.g. K8s field paths vs orbital orbIds), the producer carries the translation table. cb-bundler ships a mapping inside the OCI bundle that cb-controller uses for this purpose; other producers can use any mechanism.
4. **POST the full current set** on whatever cadence makes sense.

The first producer (cb-controller) lives in the configbundle repo and handles its own configbundle-specific concerns (the bundle's mapping layer, walking `managedFields`, takeover-apply semantics) — none of which are part of this API.

## Recovery semantics — what producers MUST handle

The intake is **replace-not-merge**: each POST overwrites orb's stored set verbatim. A POST with `{Overrides: []}` wipes the store. This is the contract — producers MUST NOT POST an empty set unless they actually mean "no divergences exist."

Two failure modes producers need to defend against, both observed live with cb-controller:

1. **Producer-side state loss** (e.g. controller restart). If the producer's intent baseline lives only in memory, a restart loses it. Subsequent runs of `computeOverrides()` produce empty results — not because there are no divergences, but because the producer can't compute them. Posting that empty set wipes orb.

   **Mitigation:** persist the intent baseline durably (cb-controller stores `last-applied-spec.yaml` in the per-CR ConfigMap and rehydrates on startup). When intent is unknown, **skip the POST entirely** rather than posting empty.

2. **Consumer-side state loss** (orb wipe / fresh edge deploy). orb has no signal back to the producer. The producer's dedup cache ("I already posted this set") prevents re-emission. orb stays empty forever until the next intent change.

   **Mitigation:** producers should re-post on a heartbeat (cb-controller's reporter ticks every 5min by default, clearing its dedup cache and re-running the comparison). Bounds recovery latency to one interval. Trades a small amount of redundant traffic for self-healing.

These are producer concerns — the intake API itself stays stateless and replace-not-merge. A producer that doesn't implement either guard will degrade silently when the failure mode hits.

## Why this contract has no producer-specific concepts

cb-controller's vocabulary (`spec.servers[serviceTag=X].idrac.sshEnabled`, `bundleDigest`, `managedFields`) is K8s-specific and configbundle-specific. If any of those leaked into orb's intake API:

- A second producer (e.g. a non-K8s edge agent for a future spike) would either have to fake the schema or orb would need a parallel intake path
- Orb would have configbundle-aware code, breaking the agnostic-pipe principle
- The mapping translation logic would have to live somewhere — and that somewhere would be orb, which contradicts the principle

By making the intake API speak only orbital-native, both problems disappear: every producer translates on its own side, and orb stays a thin courier.

## Auth

Currently none — the deployment-layer NetworkPolicy is the gate (Spike 15 decision). Producers and orb run inside the same trust boundary (a galleon's mgmt cluster). Adding intake auth would be a future decision driven by a real threat, not a hypothetical one.
