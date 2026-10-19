---
description: >-
  When the edge disagrees with intent, Orbital records it rather than correcting it. The three resolutions and why they are manual.
sidebar_label: Divergence
sidebar_position: 5
---

# Divergence

**Divergence is a field where the edge disagrees with intent.**

Orbital expects it. A data center that has been unreachable for a week, or an engineer who changed something at 3am to stop an outage, both produce divergence. It is a normal condition, not a fault.

## How a divergence appears

A local administrator overrides a field directly on the data center's Kubernetes resources, claiming ownership of it.

The edge notices that claim, records the observed value alongside the intended one, and publishes a report. Orbital imports that report and shows the field as diverged.

Nothing is corrected automatically at any point in that sequence.

## What a divergence looks like

An engineer turns SSH on at a server's BMC to debug something at 2am. Intent says it should be off.

The edge reports that as:

```json
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

Every field matters. `orbId` and `field` identify exactly what disagrees. `intendedValue` and `overrideValue` are both kept, so nobody has to guess which was which. `who` records that a human claimed the field rather than something drifting. `when` is when it was first observed, not when it was reported.

A cloud administrator then picks one of three outcomes:

- **Accept** — SSH should be on for this server. Intent becomes `true`, and the edge keeps what it has.
- **Reject** — it was a debugging change that should not persist. Intent stays `false`, and the edge is reset to match.
- **Ignore** — this server is special and both sides stay as they are, recorded as a standing decision.

## Three ways to resolve it

A cloud administrator decides each diverged field's disposition.

| Action | Intent | Edge | Meaning |
|---|---|---|---|
| **Accept** | Updated to the observed value | Unchanged | The local decision was right. Adopt it. |
| **Reject** | Unchanged | Reverts to intent | The local change was wrong. Undo it. |
| **Ignore** | Unchanged | Unchanged | Both stay as they are, knowingly and indefinitely. |

Accept and Reject close the loop: once the edge stops reporting the field, the divergence and its resolution are deleted together.

Ignore does not close. It persists until someone removes it, and the delivery layer re-reads it (`GET /api/v1/divergences?action=ignore`) every time it packages an export. That is the point — it is a standing decision to leave a field alone.

:::note
Ignore means Orbital's intent stays deliberately stale for that field. The record of that choice is what makes it safe: the next engineer sees a decision rather than an unexplained mismatch.
:::

## Why this is unusual

Comparing desired against observed state is not new. Most configuration tools do it, and most call the difference drift.

What Orbital does differently is what happens next.

**The edge can correct the source of truth.** `Accept` promotes the observed value *into* intent. In most systems the authored state is always right and the deployed state is always wrong, so the only remedy is to overwrite the edge. Orbital treats a local override as potentially correct, and gives it a reviewed path upward.

**A disagreement can be left standing.** `Ignore` is not "dismiss this alert". It is a recorded, persistent decision that re-applies on every build until someone removes it. That is the honest answer to a fix made at 3am that must not be reverted.

**Disconnection is expected.** Divergence is data, not an error condition. A data center that has been unreachable for a week and drifted is behaving normally, not failing.

## Why resolution is manual

An override is evidence that someone with local context made a call. Orbital does not have that context.

Automatically reverting it would undo a deliberate fix. Automatically accepting it would let an emergency workaround quietly become policy. Both are worse than asking.
