---
hide_table_of_contents: true
description: >-
  How Orbital models a fleet of data centers as one graph of design intent, and the rules that follow from it.
slug: /concepts
sidebar_label: Overview
sidebar_position: 0
title: Concepts
---

import Link from '@docusaurus/Link';

# Concepts

Orbital models a fleet of data centers as one graph of design intent. These pages cover how that works and the rules that follow from it.

Read them in order on a first pass. Each is short.

<ul className="cards">
<li><Link to="/concepts/design-intent"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="4.5"/><circle cx="12" cy="12" r="1"/></svg><strong>Design intent</strong></span><p>What Orbital records, what it refuses to do, and why it never reconciles.</p></Link></li>
<li><Link to="/concepts/graph"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="5" cy="6" r="2.4"/><circle cx="19" cy="6" r="2.4"/><circle cx="12" cy="18" r="2.4"/><path d="M7.3 7.2 10.5 15.9M16.7 7.2 13.5 15.9M7.4 6h9.2"/></svg><strong>The graph</strong></span><p>Nodes, relationships and namespaces — and why a graph rather than tables.</p></Link></li>
<li><Link to="/concepts/identity"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20.5 13.3 13 20.8a2 2 0 0 1-2.8 0l-7-7A2 2 0 0 1 2.6 12V4.6A2 2 0 0 1 4.6 2.6H12a2 2 0 0 1 1.4.6l7.1 7.1a2 2 0 0 1 0 3z"/><circle cx="7.5" cy="7.5" r="1.3"/></svg><strong>Identity</strong></span><p>How every node gets a stable, derivable <code>orbId</code> instead of a random one.</p></Link></li>
<li><Link to="/concepts/orbital-and-orb"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M17.5 10.5a4.5 4.5 0 0 0-8.6-1.4A3.5 3.5 0 0 0 5 12.5"/><path d="M6 18.5h11a3.5 3.5 0 0 0 .5-7"/><path d="M4.5 12.5A3 3 0 0 0 6 18.5"/></svg><strong>Orbital and Orb</strong></span><p>The cloud service, the edge service, and the three stores that are easy to confuse.</p></Link></li>
<li><Link to="/concepts/divergence"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 12h5"/><path d="M8 12l5-6h8"/><path d="M8 12l5 6h8"/><path d="M18 3l3 3-3 3"/><path d="M18 15l3 3-3 3"/></svg><strong>Divergence</strong></span><p>What happens when the edge disagrees with intent, and the three ways to resolve it.</p></Link></li>
<li><Link to="/concepts/glossary"><span className="card-head"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4.5 4.5A2 2 0 0 1 6.5 2.5h13v16h-13a2 2 0 0 0-2 2z"/><path d="M4.5 20.5a2 2 0 0 0 2 2h13v-4"/></svg><strong>Glossary</strong></span><p>Every term this documentation uses, each with one meaning.</p></Link></li>
</ul>
