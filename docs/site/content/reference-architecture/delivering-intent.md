---
description: >-
  The cloud-authored, edge-enforced pattern in five steps, and a table for deciding where a given change belongs.
sidebar_label: Delivering intent
sidebar_position: 2
---

# Delivering intent

**Cloud-authored, edge-enforced.** Intent is written once, in the cloud. It is applied locally, by the data center, on its own schedule.

## The five steps

1. **Author.** Intent changes in Orbital, and only in Orbital.
2. **Export and publish.** A data center's namespace is exported as a versioned artifact, signed and pushed to a registry.
3. **Transport.** The artifact reaches the data center however it allows — a registry, removable media, a scheduled sync.
4. **Apply.** Local controllers reconcile the data center toward that intent, and keep reconciling whether or not the link is up.
5. **Report.** Local overrides surface as divergence and travel back for a human to resolve.

Steps 3 and 4 are the part Orbital does not specify. Transport is a deployment concern, and the reconcilers are your own controllers.

## Where a change belongs

Most confusion about edge configuration is really this question.

| What you want | Where it goes | Why |
|---|---|---|
| Change the intended configuration | Author it in Orbital; it reaches the data center on the next publish | Orbital is the only place intent is authored. This is the normal path. |
| Override something locally, now | Claim the field on the data center's own Kubernetes resources | Takes effect immediately, survives reconciliation, and surfaces as divergence for review |
| Record newly discovered hardware | Propose it to Orbital as intent, for review | Observed data *proposes* intent. It is reviewed and lands in Orbital — never written at the edge. |
| See what has drifted | Read it at the edge | Looking is fine. Writing to Orb's mirror is not — it is read-only, and the next import discards it. |

The second row is the one worth internalising. An urgent local change is **not** a workaround to be apologised for. It is a supported path that produces a reviewable record.
