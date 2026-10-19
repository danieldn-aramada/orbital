---
hide_table_of_contents: true
description: >-
  The contract at Orbital's boundary, how intent reaches a data center, and ConfigBundle as one proven implementation of the delivery layer.
slug: /reference-architecture
sidebar_label: Overview
sidebar_position: 0
title: Reference Architecture
---

import Link from '@docusaurus/Link';

# Reference Architecture

Orbital publishes intent. Something has to carry that intent to a data center and apply it.

Orbital does not do that part, deliberately — nothing in the cloud executes against a data center. These pages describe the contract Orbital guarantees at its boundary, the pattern beyond it, and one proven implementation.

:::note
Most teams adopting Orbital will write their own delivery layer, or adapt one they already run. That is the intended outcome. This is a reference, not a requirement.
:::

<ul className="cards">
<li><Link to="/reference-architecture/contract"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 2.5H6.5a2 2 0 0 0-2 2v15a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2V8z"/><path d="M14 2.5V8h5.5"/><path d="M8.5 13h7M8.5 16.5h4.5"/></svg><strong>The contract</strong></span><p>The two API surfaces Orbital guarantees: exporting intent, and collecting divergence.</p></Link></li>
<li><Link to="/reference-architecture/delivering-intent"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2.5 12h6"/><path d="M15.5 12h6"/><circle cx="12" cy="12" r="3"/><path d="M18.5 9l3 3-3 3"/><path d="M5.5 15l-3-3 3-3"/></svg><strong>Delivering intent</strong></span><p>Cloud-authored, edge-enforced — the five steps, and where a given change belongs.</p></Link></li>
<li><Link to="/reference-architecture/configbundle"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2.8 20.5 7v10L12 21.2 3.5 17V7z"/><path d="M3.8 7.2 12 11.6l8.2-4.4M12 11.6V21"/></svg><strong>ConfigBundle</strong></span><p>Armada's implementation, and what to build if you write your own.</p></Link></li>
</ul>
