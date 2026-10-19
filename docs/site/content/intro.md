---
description: >-
  Orbital is the source of truth for edge, air-gapped modular data centers. It holds design intent as a graph and publishes versioned configuration for data centers to pull and apply locally.
slug: /
title: Orbital
sidebar_label: Welcome
sidebar_position: 0
---

import Link from '@docusaurus/Link';

# Orbital

**Orbital is the source of truth for edge, air-gapped modular data centers.**

It holds design intent for every data center as a graph, serves that graph as a traversable API, and publishes versioned configuration for data centers to pull and apply locally. Orbital is never in the reconciliation path. It states what should be true, and records where reality has diverged.

## Why it exists

Orbital was built after an evaluation of DCIM, CMDB and ITSM tools found none that fit, for two reasons.

**Air-gap was the filter.** A modular data center may be disconnected for hours, or permanently. Most tools treat disconnection as an edge case and assume the cloud can reach the data center to reconcile it. That assumption eliminated nearly every commercial option.

**The configuration domains were missing.** The strong open-source options — NetBox, Nautobot, Ralph — model racks, devices, addresses and IPAM very well, and commercial DCIM tools cover facilities and asset lifecycle. None of them natively model BMC profiles, storage devices, Kubernetes cluster configuration or application configuration, which is most of what an operator actually needs to change.

That is not a criticism of those tools. It is a different problem, and the honest conclusion was to keep the network source of truth where it already worked and build for the part that had no home.

So Orbital does not replace what you have. It models what the inventory tools leave out, and it keeps working when the link is down.

## What makes it different

**Air-gapped by design.** Data centers run fully offline. Nothing in the cloud executes against one. Reconciliation is always local.

**Graph-native.** Relationships are first-class, so impact, dependency and connectivity are traversals rather than joins written in advance.

**Divergence is resolved, not suppressed.** When the edge disagrees with intent, Orbital records it and asks. A local override can be promoted *into* intent — the edge can correct the source of truth, not only the other way round.

## Using the documentation

<ul className="cards">
<li><Link to="/getting-started/run-locally"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="2.5" y="4" width="19" height="16" rx="2"/><path d="M6.5 9.5 9.5 12l-3 2.5M12.5 14.5h5"/></svg><strong>Getting Started</strong></span><p>Run Orbital and Orb locally, with example data, in a few minutes.</p></Link></li>
<li><Link to="/concepts/design-intent"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4.5"/><circle cx="12" cy="12" r="1"/></svg><strong>Concepts</strong></span><p>Design intent, the graph, identity, the cloud/edge split, and divergence.</p></Link></li>
<li><Link to="/using-orbital-ui"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="2.5" y="3.5" width="19" height="17" rx="2"/><path d="M2.5 8h19M8 8v12.5"/></svg><strong>Using the Orbital UI</strong></span><p>List views, detail views, relationships, the editor and the audit log.</p></Link></li>
<li><Link to="/defining-pages"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 2.5H6.5a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2V8z"/><path d="M14 2.5V8h5.5"/><path d="m9.5 12.5-2 2 2 2M14.5 12.5l2 2-2 2"/></svg><strong>Defining Pages</strong></span><p>Declare what each page shows, edits and deletes in one file, <code>views.yaml</code>.</p></Link></li>
<li><Link to="/reference-architecture"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/><path d="M10 6.5h4a1.5 1.5 0 0 1 1.5 1.5v6"/><path d="M13.2 11.2 15.5 14l2.3-2.8"/></svg><strong>Reference Architecture</strong></span><p>The contract at Orbital's boundary, and one proven way to deliver intent to a data center.</p></Link></li>
</ul>

:::note Preview
This site is an early preview. Sections are still being written.
:::
