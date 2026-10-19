---
description: >-
  ConfigBundle is Armada's implementation of the delivery layer. What it does, and what to build if you write your own.
sidebar_label: ConfigBundle
sidebar_position: 3
---

# ConfigBundle

ConfigBundle is Armada's implementation of steps 3 and 4 — transport and apply.

It packages an Orbital export as an OCI artifact, signs it, distributes it to a data center's local registry, and runs Kubernetes controllers that reconcile the data center toward the bundled intent.

You do not need it. It is useful as a worked example of decisions any delivery layer has to make.

## Three decisions it works through

**Signing and verification.** A data center must be able to trust an artifact that arrived on a disk, with no connection back to whoever produced it. Signatures make provenance checkable offline.

**Layered artifacts.** Configuration produced by different systems travels together, and each layer records which system produced it. Without that attribution, the edge cannot tell what it is applying or who to ask about it.

**Field ownership at the edge.** A local override is recorded as a *claim* on a field rather than silently overwriting it. This is the one that matters most — it is what makes a divergence report possible at all. Without ownership tracking there is no way to distinguish "a human decided this" from "something drifted".

## Building your own

You need something that:

- Pulls an export and verifies it came from your Orbital
- Applies it locally, repeatedly, without needing the cloud
- Notices when a field has been overridden locally
- Publishes those observations somewhere Orbital can collect them

If you already run GitOps at the edge, the first three are mostly solved. The fourth is usually the only genuinely new work.
