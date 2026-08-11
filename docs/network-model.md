# Design Proposal: Network topology (NICs, ports, switches) in orbital

**Status:** Design record. Reflects decisions through 2026-08-10. Network types (`NetworkAdapter` / `NetworkPort` / `NetworkDevice`) are in `schema/schema.graphql` and seeded for the Colo reference (`CFRHDX3`).

**Scope:** Server network hardware (NIC cards, ports, MACs) and server↔switch adjacency, modeled as **design intent** in orbital's DGraph. Reverses the settled decision *"Network infrastructure config items are out of v1 scope"* for this server-side + first-hop-switch slice only. Still **excluded**: IPAM (pools/subnets/allocation/overlap), switch internal config, VLAN databases, routing, fabric interior.

---

## 1. Guiding principle — schema vs. source (the decision that shapes everything)

Two layers, held strictly apart:

- **Schema design = generic, vendor-agnostic, follows upstream standards.** Orbital is a general config-management product; an adopter may not run NetBox — or Redfish, or anything we use. The node types/names must stand on their own.
  - Server hardware follows **Redfish / DMTF** (vendor-neutral: `NetworkAdapter`, `Port`) — the same reason Redfish underpins orbital's storage types (`StorageController`, etc.).
  - The switch follows the generic **DCIM "device + role"** abstraction — Redfish does not model switches at all (it is a *server* management standard).
- **Data sourcing = whatever an adopter actually has.** For us today: **NetBox supplies the values; Redfish cross-checks / enriches.** Orbital is the system of record. NetBox is an *input to reconcile from*, not the authority — it is inconsistent (see §4).

**The rule:** the schema carries the full generic shape; each field is populated from whichever source is authoritative for it; **structure a source lacks stays empty — never fabricated.** Example: NetBox is flat (`Device → Interface`, no NIC-card entity). When sourcing from NetBox only, `NetworkAdapter` is simply **absent** — you do *not* synthesize it by parsing interface names. Redfish, which has the card authoritatively, fills it.

---

## 2. Node types

| Node | Follows | Notes |
|---|---|---|
| `NetworkAdapter` | Redfish/DMTF `NetworkAdapter` | Physical NIC card (a FRU). **Optional** — filled from Redfish, absent for flat-only sources. |
| `NetworkPort` | Redfish `Port` ⇄ `EthernetInterface` (1:1, collapsed to one node) | The port/interface. Carries the **MAC** (the cross-source join key). Always linked to `Server`. |
| `NetworkDevice` | Generic DCIM device + role | Minimal identity anchor for **network gear** (switch/router/firewall/sd-wan), role-differentiated. Values from NetBox. `macAddress` is the device MAC. |

**Link aggregation (bonds / LAGs) — modeled as design intent.** Bonding *can be* a design decision ("these two NICs shall be LACP-bonded to the ToR pair"), so it belongs in the intent store. Following NetBox's convention (and OpenConfig / IEEE 802.1AX): a LAG **is an interface with member interfaces**, not a separate entity. So a bond is a `NetworkPort` (`portType: "LAG"`, no adapter, logical) whose physical members reference it via a self-referential `lag` edge (mirrors NetBox `interface.lag`; same pattern as `EksaKubernetesCluster.managementCluster`/`workloadClusters`). Sourced from NetBox where authored; whether the OS *realizes* the bond stays observed/runtime, outside our sourcing.

**Deliberately NOT modeled:**

- **`NetworkSwitchMember` (VC/FPC)** — no vendor-agnostic convention exists (Juniper VC / Cisco StackWise / Arista MLAG are all vendor-specific), Redfish doesn't model switches, and VC-vs-modular-chassis failure domains are **unverifiable** from server-side data. The FPC signal survives losslessly inside `connectedNetworkDevicePort` (`"xe-1/0/28"`); parse it at query time if a redundancy check ever needs it.
- **Switch-port nodes** — the remote port is a string label on `NetworkPort`, not a node. Blast radius traverses `NetworkDevice → NetworkPort → Server` without them.
- **IPAM, cable inventory, NPAR** — deferred. NPAR is a documented future extension (insert a Redfish `NetworkDeviceFunction` level between port and its functions if partitioning is ever enabled; capable but not enabled on examined hardware).

**Graph shape (rooted at `DataCenter`):**

```mermaid
graph TD
    DC["DataCenter"]
    SRV["Server"]
    NA["NetworkAdapter<br/>NIC card · optional"]
    NP["NetworkPort<br/>port / interface / LAG"]
    SW["NetworkDevice<br/>switch / router / firewall"]

    DC -->|servers| SRV
    DC -->|networkDevices| SW
    SRV -->|networkAdapters| NA
    SRV -->|networkPorts| NP
    NA -->|networkPorts| NP
    NP -->|connectedNetworkDevice| SW
    NP -->|"lag / lagMembers (self)"| NP

    classDef existing fill:#f1f5f9,stroke:#94a3b8,color:#0f172a;
    classDef added fill:#dbeafe,stroke:#2563eb,color:#1e3a8a;
    class DC,SRV existing;
    class NA,NP,SW added;
```

Blue nodes are new; grey are existing. A `NetworkPort` attaches to its `Server` directly (always) and to a `NetworkAdapter` when a source provides the card (optional); the `lag` self-edge groups member ports into a bond.

---

## 3. Adjacency naming

How the conventional sources name "the switch a server port connects to":

- NetBox: `cable` / `link_peer` / **`connected_endpoints`** (a *list* — see note)
- LLDP (IEEE 802.1AB — the discovery mechanism): **`neighbor`**
- Redfish: LLDP `neighbor` / remote

The field is **design-intent about a cabled connection** (not observed link state), so the DCIM cabling convention fits best: **`connectedNetworkDevice` / `connectedNetworkDevicePort`** (mirrors NetBox `connected_endpoints`). Vendor-agnostic and unambiguous. *Alternative:* `neighborSwitch` (LLDP's term) if a discovery-flavored name is preferred.

**Singular, despite NetBox's plural.** NetBox renamed `connected_endpoint` → `connected_endpoints` (a list) to support cable-path *tracing* through patch panels / breakout cables. A single physical NIC port is point-to-point — it cables to exactly one switch port — so `connectedNetworkDevice` stays singular. Breakouts (one QSFP → N SFP), if they ever appear, are modeled as separate `NetworkPort`s, each with its own singular `connectedNetworkDevice`.

---

## 4. Sourcing reality (from the live investigation, 2026-08-10)

Investigated the Colo R650 (`eksa-control-04`, iDRAC `10.20.21.44`, serviceTag `CFRHDX3`) against NetBox (`ilb.devnew.armada.internal`). **Every join between Redfish observation and NetBox intent resolved exactly:**

| Join | Redfish / LLDP (observed) | NetBox (design intent) |
|---|---|---|
| NIC MAC | `14:23:F2:30:8F:B0` (+3 more) | interface `IntN1P1` on dev 24 "Sabey Compute Server 3" |
| Cabling (port name) | LLDP `xe-1/0/28` | cable #22 → `xe-1/0/28` |
| Switch identity | LLDP chassis MAC `48:5A:0D:3E:16:20` | `jsrv` on dev 7 `Colo_TOR_Switch (Backup)` — EX4650-48Y, serial `XH3123021901` |

**But NetBox is inconsistent.** Colo (site 4) is fully populated; Demo Galleon 2 (site 5) had empty interface MACs and switches with no interfaces. There are also **multiple NetBox instances (dev / prod).** Consequences:

- Reconciliation is **per-DC and manual** (maintainer + assistant, data center by data center).
- **Join caveat:** orbital servers are keyed by `serviceTag`, but NetBox devices have **blank serials** → there is no `serviceTag ↔ device` join. Use per-port **MAC** (or the iDRAC/oob MAC) as the reconciliation key.
- Where NetBox is thin, **Redfish fills the gap** (NetBox's custom-field schema — `mac_address`, `serial`, `sku`, `bmc_ip`… — is built to receive Redfish data but is currently null).
- **Source `NetworkDevice` nodes from NetBox's site device-inventory, NOT from server-cable far-ends** (2026-08-11 burn). Enumerate `/dcim/devices?site_id=<n>` filtered to network roles (`tor`, `frw`→"firewall", router/core/…). Cable-derivation only ever finds server-adjacent ToRs — it **misses** OOB switches (BMC-cabled) and firewalls (ToR-cabled), and **imports phantom cross-site devices** from one bad server cable. Colo (site 4) = **4 Junipers**: 2× EX4650 ToR, EX2300 OOB switch, SRX1500 firewall. Adjacency edges still derive from cabling/LLDP but only link to a device that's in the inventory.

---

## 5. Schema

Lives in **`schema/schema.graphql`** 

- **`NetworkAdapter`** — the physical NIC card (a FRU: model / manufacturer / serial / part). Optional, Redfish-authoritative.
- **`NetworkPort`** — a port/interface, physical *or* a logical LAG. Carries the `macAddress` (the cross-source join key), `linkSpeedMbps` (the **negotiated** link speed from Redfish, null when down), the `connectedNetworkDevice` / `connectedNetworkDevicePort` adjacency, and the `lag` / `lagMembers` self-edge for bonds.
- **`NetworkDevice`** — a minimal network-gear identity anchor (`model` / `serial` / `role` / `macAddress`), role-differentiated (switch/router/firewall/sd-wan). Values from NetBox; not an editable config item.
- **New edges on existing types** — `Server.networkAdapters`, `Server.networkPorts`, `DataCenter.networkDevices`.

**orbId** extends the existing `namespace:serviceTag-<id>` pattern — e.g. adapter `colo:CFRHDX3-NIC.Integrated.1`, port `colo:CFRHDX3-NIC.Integrated.1-1`, switch (no serviceTag → key on serial) `colo:netdev-XH3123021901`. `macAddress` is the cross-source reconciliation key regardless of orbId.

### Example: a bonded pair (LAG + physical members)

A LAG is just a `NetworkPort` with `portType: "LAG"`, no `networkAdapter`, and a `lagMembers` list that fills itself from whichever ports set `lag` to it — **you only ever set `lag` on the members, never `lagMembers` directly.** Below: the Colo R650 `CFRHDX3` with its two 25G ports bonded.

**LAG `NetworkPort`** (`bond0`):

```
orbId:               "colo:CFRHDX3-bond0"
name:                "bond0"
portType:            "LAG"
linkSpeedMbps:       20000          # aggregate of member negotiated speeds (2 × 10G here) — earns its keep on a LAG
macAddress:          null           # a bond's MAC is inherited at runtime, not design intent
networkAdapter:      null           # logical — no physical card
connectedNetworkDevice: null   # optional; members carry the physical device-port links
connectedNetworkDevicePort: null
lag:                 null           # it is the PARENT, not a member
lagMembers:          [ NIC.Integrated.1-1, NIC.Slot.1-1 ]   # auto (@hasInverse) — never set directly
server:              → "colo:CFRHDX3"
```

**Physical `NetworkPort`** (a member — real media, points up via `lag`):

```
orbId:               "colo:CFRHDX3-NIC.Integrated.1-1"
name:                "NIC.Integrated.1-1"
portType:            "Ethernet"
macAddress:          "14:23:F2:30:8F:B0"
linkSpeedMbps:       10000          # negotiated — a 25G-rated port linked at 10G
networkAdapter:      → "colo:CFRHDX3-NIC.Integrated.1"
connectedNetworkDevice: → "colo:netdev-XH3123021901"
connectedNetworkDevicePort: "xe-1/0/28"
lag:                 → "colo:CFRHDX3-bond0"
server:              → "colo:CFRHDX3"
```

(The second member `NIC.Slot.1-1` is the same shape: `macAddress 14:23:F2:AC:50:F0`, `connectedNetworkDevicePort "xe-0/0/4"`, `lag → colo:CFRHDX3-bond0`.)

As seed GraphQL (edges are nested `orbId` refs — create the LAG before its members):

```graphql
{ orbId: "colo:CFRHDX3-bond0", name: "bond0", namespace: "colo", version: 1,
  portType: "LAG", linkSpeedMbps: 20000, server: { orbId: "colo:CFRHDX3" } }

{ orbId: "colo:CFRHDX3-NIC.Integrated.1-1", name: "NIC.Integrated.1-1", namespace: "colo", version: 1,
  macAddress: "14:23:F2:30:8F:B0", portType: "Ethernet", linkSpeedMbps: 10000,
  server: { orbId: "colo:CFRHDX3" },
  networkAdapter: { orbId: "colo:CFRHDX3-NIC.Integrated.1" },
  connectedNetworkDevice: { orbId: "colo:netdev-XH3123021901" }, connectedNetworkDevicePort: "xe-1/0/28",
  lag: { orbId: "colo:CFRHDX3-bond0" } }
```

---

## 6. Anchor queries (validated against the final schema)

1. **Which servers share a device / blast radius** — start at the network device: `NetworkDevice → networkPortConnectedNetworkDevice → server (→ kubernetesNode → cluster)`. Clean traversal; complete at the leaf/ToR layer (no fabric interior modeled, by design).
2. **Cabling / redundancy validation** — compare intended `connectedNetworkDevice` / `connectedNetworkDevicePort` against the Redfish LLDP observation. Bond membership is now **declared intent** (`lagMembers`), so the check asserts *"the intended LAG's member ports land on ≥2 distinct switches"* — no need to guess which ports are bonded. Two caveats remain: (a) VC (real redundancy) vs. modular chassis (fake) is indistinguishable without switch-plane data, so it reports *likely* not proven redundancy; (b) whether the OS **realizes** the intended bond is runtime/observed, outside our sourcing. State both wherever the check is built.
3. **Provisioning lookup** — `macAddress` is `@search(by:[hash])`: `queryNetworkPort(filter:{macAddress:{eq:...}}) → server`. No MAC node required.

---

## 7. Open items / risks

- **Redundancy heuristic caveat** (§6) — must be stated wherever the check is implemented.
- **Seed reconciliation** — per-DC, MAC-keyed (serviceTag ↔ NetBox device has no join). Start with `CFRHDX3` (NetBox dev 24 / Colo) as the reference pattern, expand across Colo, then other DCs. Account for multiple NetBox instances (dev / prod).
- **`NetworkDevice` generalization — DONE.** Switches, routers, firewalls, and SD-WAN are modeled as one `NetworkDevice` + `role` (superseding the switch-only `NetworkSwitch`). Only `role: "tor"` is seeded so far (the Colo ToRs); other roles get added as they're sourced from NetBox.
- **XE9680 / GPU spot-check** — only an R650 was examined; RoCE/IB ports may differ (`portType` future-proofs it). Re-validate before GA.
- **NPAR** — not modeled (capable, not enabled on examined hardware); future `NetworkDeviceFunction` level if enabled.
- **Documentation on acceptance** — record the scoped reversal of "network out of scope" + these conventions in `docs/reference/DGRAPH.md`; register `NetworkAdapter` / `NetworkPort` as editable config items per `docs/playbooks/add-configitem.md`, but **not** `NetworkDevice` (identity anchor, like `IPAddress` — no editor).
