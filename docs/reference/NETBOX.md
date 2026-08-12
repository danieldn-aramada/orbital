# NetBox Reference

Read this before: querying NetBox, building/validating a `*-network.graphql` seed, or reconciling orbital's `NetworkDevice` / `NetworkInterface` / `NetworkAdapter` against NetBox. NetBox is orbital's **network-topology source today** — orbital is becoming the system of record and the network team pushes into orbital's API, replacing manual NetBox edits (see `docs/network-model.md §1`).

## Connection

- **Base:** `http://ilb.devnew.armada.internal/netbox/api` (AKS dev, behind the ilb → needs VPN).
- **Auth:** `-H "Authorization: Token <token>"` — read-only token (a credential; not committed here — keep it out of the repo).
- **Version: NetBox 4.5.1** — the version matters (MAC moved to a first-class object in 4.2+; see below).
- **Learn shapes from the browsable API**, not OpenAPI: `GET /api/` (groups) and `GET /api/dcim/` (endpoint list). **`/api/schema/` times out — don't rely on it.**
- **Colo = `site_id=4`** (site name "Colo").
- **⚠ The API is SLOW** — ~15–40s/call, and single calls sometimes time out. **Never loop per item** (that's the timeout we hit building the fabric). Pull in bulk with `?limit=1000` and join client-side.

## The join keys (this bit us repeatedly)

| orbital node | orbId | NetBox source field | ⚠ |
|---|---|---|---|
| `Server` | `colo:server-<serial>` | device **`asset_tag`** (Dell) | serial = **Redfish System SerialNumber**. Dell: that equals the Service Tag, which NetBox stores in **`asset_tag`** (device `serial` is BLANK) — join there. Non-Dell (Supermicro A100): NetBox has neither → **join by interface MAC**. |
| `NetworkDevice` | `colo:network-device-<serial>` | device **`serial`** | switches/firewalls *do* have `serial` populated (`JX3623130496`). |
| `NetworkInterface` | owner + FQDD/port | interface **`mac_address`** | **MAC is the cross-source join** — server serials are blank *and* `connected_endpoints.device` carries no serial. Case-insensitive; NetBox uppercases. |

**Rule of thumb: join interfaces by MAC, servers by `asset_tag` (Dell) or MAC (non-Dell), network devices by `serial`.**

## Field shapes (4.5.1, real samples)

### Device — `/dcim/devices/{id}/`
```
name, serial ("" for servers!), asset_tag ("BFRHDX3" = serviceTag for servers),
device_type.model ("PowerEdge R650"), role.name ("Physical_Server" | "tor" | "firewall"),
site.name ("Colo"), rack.name ("Sabey A.OF.C.09"), position (U, float), face.value ("front"),
platform.name, virtual_chassis, vc_position, primary_ip4,
custom_fields { hostname, ip_address, bmc_ip, core_count, gpu_count, memory_total,
                cluster_identifier, part_number, firmware_version, ... }
```
- ⚠ `rack.name` carries a **site prefix** ("Sabey A.OF.C.09") — strip it → `A.OF.C.09` for the orbital rack orbId.
- 💡 `custom_fields` is a rich, **currently-unused** source (BMC IP, core/GPU counts, hostname, cluster) — candidate future enrichment for orbital, overlaps with Redfish.

### Interface — `/dcim/interfaces/?device_id={id}`
```
name ("iDRAC" | "NIC.Embedded.1-1" | "ge-0/0/2"),
type.value ("1000base-t" | "25gbase-x-sfp28"),
mgmt_only (bool — TRUE for the BMC/iDRAC → orbital NetworkInterface.mgmtOnly),
mac_address ("C8:4B:D6:9D:D7:04")          ← string, back-compat; = the primary MAC. USE THIS to join.
primary_mac_address { id, mac_address }     ← 4.2+ MACAddress object (canonical; /dcim/mac-addresses/).
cable (id | null), lag (ref | null),
connected_endpoints [ { device: { name }, name } ]   ← the far interface.
```
- ⚠ **`connected_endpoints[0].device` has `name` only — no `serial`.** You cannot get the far device's serviceTag from a connection. Join by MAC, or do a second device lookup.

### Cable — `/dcim/cables/?device_id={id}`
```
id, status.value ("connected"),
a_terminations [ { object_type: "dcim.interface", object: { device: { name }, name } } ],
b_terminations [ same ]
```
- A cable = A-side ↔ B-side, each termination an interface (device + port). **Device↔device fabric (OOB↔SRX↔ToR) lives here** — switches don't speak Redfish, so their interconnects are NetBox-only.

## Recipes (verified this session)

```bash
NB=http://ilb.devnew.armada.internal/netbox/api ; TOK="Authorization: Token <token>"

# colo network devices (filter to network roles client-side)
curl -s -H "$TOK" "$NB/dcim/devices/?site_id=4&limit=1000" | jq '.results[] | {name, serial, role: .role.name}'

# a device's interfaces + far ends
curl -s -H "$TOK" "$NB/dcim/interfaces/?device_id=8&limit=1000" \
 | jq '.results[] | {name, mac: .mac_address, mgmt: .mgmt_only, far: ((.connected_endpoints//[])[0] | {dev: .device.name, port: .name})}'

# server iDRAC -> OOB port, keyed by MAC (join mac to orbital Server.oobMAC)
curl -s -H "$TOK" "$NB/dcim/interfaces/?site_id=4&cabled=true&limit=1000" \
 | jq -r '.results[] | select((.connected_endpoints//[])[0].device.name=="Colo_OOB_SW1") | "\(.mac_address)\t\(.connected_endpoints[0].name)"'

# device<->device fabric: query each of the 4 devices; keep far-ends that are also devices (both ends appear)
```

## Filters & pagination
`?device_id=` · `?site_id=` · `?name=` · `?cabled=true` · `?mgmt_only=true` · `?role=` · `?limit=1000`.
Response envelope: `{ count, next, previous, results: [...] }`.

## Gotchas (one-liners)
- Server `serial` is blank → serviceTag is in `asset_tag`; **join interfaces by MAC**.
- `connected_endpoints.device` = name only (no serial).
- API is slow → bulk `?limit=1000`, never per-item loops.
- `/api/schema/` (OpenAPI) times out → use the browsable `/api/dcim/` for shapes.
- `rack.name` has a site prefix ("Sabey …").
- The OOB switch role is mislabeled `tor` in NetBox — orbital corrects it to `mgmt`.
- **Non-Dell servers** (Supermicro A100 "Hyperplane", `colo:server-S447008X3823034`): BMC is the **IPMI** Redfish Manager (not iDRAC), NIC Redfish ids are numeric, NetBox lacks serial+asset_tag → **join by MAC**. Redfish (`https://<oob-ip>/redfish/v1`, creds `ADMIN:ADMIN`) is **ground truth over NetBox/sheets** — on the A100 it caught a wrong serial, model, and RAM.
