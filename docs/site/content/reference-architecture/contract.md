---
description: >-
  The two API surfaces Orbital guarantees at its boundary — exporting intent for a data center, and collecting divergence reports back.
sidebar_label: The contract
sidebar_position: 1
---

# The contract

Everything past Orbital's boundary is yours. These are the two surfaces Orbital guarantees, and the only ones a delivery layer needs to build against.

## Outbound — exporting intent

| Step | Endpoint |
|---|---|
| Start an export for one namespace | `POST /api/v1/export` |
| Check or list jobs | `GET /api/v1/export/jobs`, `GET /api/v1/export/jobs/{id}` |
| Retrieve the artifact | `GET /api/v1/export/jobs/{id}/download` |

An export covers **one namespace** — one data center.

```json title="POST /api/v1/export"
{
  "orbId": "docs-example:datacenter-docs-example",
  "download": false
}
```

`orbId` names the data center, in `namespace:name` form. `download: true` retains a zip for the download endpoint; `false` publishes the artifact and discards the local copy.

The artifact carries the configuration graph as `data.json.gz` and the schema that describes it as `schema.gz`.

:::note
The schema travels with the data. A data center can read an export without asking Orbital what the fields mean, which is what lets an artifact arrive on removable media and still be usable.
:::

## Inbound — divergence reports

| Step | Endpoint | Runs on |
|---|---|---|
| Accept a report from a local agent | `POST /api/v1/divergence` | Orb |
| Publish reports outward | `POST /api/v1/divergence/publish` | Orb |
| List divergences | `GET /api/v1/divergences` | Orbital |
| Resolve one | `PUT /api/v1/divergences/{id}/resolution` | Orbital |

A report is the data center's full current divergence set, not a delta:

```json title="POST /api/v1/divergence"
{
  "overrides": [
    {
      "orbId": "docs-example:DOCS001-idrac",
      "field": "sshEnabled",
      "type": "IdracSettings",
      "intendedValue": false,
      "overrideValue": true,
      "who": "local:admin",
      "when": "2026-10-07T02:14:51Z"
    }
  ]
}
```

Sending the whole set each time is deliberate. A field that stops appearing has stopped diverging, so the loop closes without anyone having to send a retraction — which matters when reports may be delayed, duplicated or delivered out of order across a disconnection window.

An empty `overrides` array is valid and means everything agrees.

Orbital never initiates contact with a data center. Reports are published outward by the edge, and Orbital collects them.

That direction is not an implementation detail. It is what lets a data center sit behind a one-way boundary, or be unreachable for a week, without anything breaking.
