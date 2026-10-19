---
description: >-
  Every node carries a stable, derivable orbId rather than a random identifier. How the convention works and why.
sidebar_label: Identity
sidebar_position: 4
---

# Identity

Every node in Orbital has an `orbId` — a stable, readable identifier derived from the thing it names, never randomly generated.

## The shape

The convention has three parts:

```text
<namespace>:<type>-<natural-key>
```

| Part | Meaning | Example |
|---|---|---|
| `namespace` | The data center the node belongs to | `docs-example` |
| `type` | The ConfigItem type's name, lower-cased with no separators | `networkdevice` |
| `natural-key` | A stable real-world identifier | the device serial |

A server in the `docs-example` namespace with service tag `DOCS001` has the orbId `docs-example:server-DOCS001`. A switch with serial `DOCSSW01` is `docs-example:networkdevice-DOCSSW01`.

## Why it is derived

Deriving the id from data you already hold buys two things.

**Imports are idempotent.** Running the same import twice produces the same ids, so the second run updates nodes instead of duplicating them.

**Clients can construct an id without a lookup.** A tool that knows a server's service tag can address that server directly, with no round trip to Orbital.

A random identifier would cost you both.

## Declaring it

Each type declares its shape in the schema, with an `orbIdPattern` annotation:

```graphql
"""orbIdPattern: {type}-{serviceTag}"""
type Server implements ConfigItem {
  serviceTag: String
  # ...
}
```

The pattern is the source of truth. Orbital prepends the namespace, and interpolates:

| Placeholder | Becomes |
|---|---|
| `{type}` | The type's lower-cased name, or the value of an `orbIdSuffix` annotation where that reads better (`IdracSettings` uses `idrac`) |
| `{field}` | An attribute of the node itself, such as `{serviceTag}` |
| `{edge.field}` | An attribute reached across a relationship, such as `{server.serviceTag}` |

A node that belongs to another one takes its key from its owner. A server's iDRAC settings are `docs-example:DOCS001-idrac`, from `{server.serviceTag}-{type}`.

Two more forms cover the edge cases:

- **Repeat the line** to declare alternatives, for a type whose owner can be one of several. A network interface belongs to a server *or* to a switch, so it declares a pattern for each. The first whose placeholders all resolve wins.
- **`orbIdPattern: external`** opts a type out, for ids assigned by another system.

Orbital's schema tests reject a type that declares no pattern, and at runtime a pattern that cannot be resolved is logged as a warning when Orbital loads the schema. There is deliberately no fallback to a generated id: a natural key cannot be guessed.

## Choosing a natural key

A natural key must stay fixed for the life of the hardware. Serial numbers and service tags qualify. Hostnames and asset tags do not, because both get reassigned.

`Server` has a `name` attribute, and it is not the server's identity.

Because an orbId is immutable, the attributes it is built from are too. The UI lets you set a service tag when you create a server and not afterwards.

## Uniqueness

An orbId is unique across every type. Two nodes cannot share one, and Orbital refuses the second write.

:::warning[Not retroactive]
The constraint applies to new writes only. Applying it to a graph that already holds duplicates succeeds without reporting them. Audit an existing graph before relying on it.
:::
