---
description: >-
  Every configuration item carries a stable, derivable orbId rather than a random identifier. How the convention works and why.
sidebar_label: Identity
sidebar_position: 4
---

# Identity

Every configuration item in Orbital has an `orbId` — a stable, readable identifier that is derived from the thing it names, never randomly generated.

## The shape

An orbId has three parts:

```
<namespace>:<kind>-<natural-key>
```

| Part | Meaning | Example |
|---|---|---|
| `namespace` | The data center the item belongs to | `seattle` |
| `kind` | The item's type, in kebab-case | `network-device` |
| `natural-key` | A stable real-world identifier | the device serial |

A server in the `seattle` namespace with service tag `CFRHDX3` has the orbId `seattle:server-CFRHDX3`.

## Why it is derived

Deriving the id from data you already hold buys two things.

**Imports are idempotent.** Running the same import twice produces the same ids, so the second run updates rows instead of duplicating them.

**Clients can construct an id without a lookup.** A tool that knows a server's service tag can address that server directly, with no round trip to Orbital.

A random identifier would cost you both.

## Choosing a natural key

A natural key must stay fixed for the life of the hardware. Serial numbers and service tags qualify. Hostnames and asset tags do not, because both get reassigned.

Each type declares its own shape in the schema:

```graphql
"""orbIdPattern: {kind}-{serviceTag}"""
type Server implements ConfigItem {
```

:::note
Orbital fails the build if a type declares no pattern. There is deliberately no default: a natural key cannot be guessed. `Server` has a `name` field, and it is not the server's identity.
:::

## Uniqueness

An orbId is unique across every type. Two items cannot share one, and Orbital refuses the second write.

:::warning[Not retroactive]
The constraint applies to new writes only. Applying it to a graph that already holds duplicates succeeds without reporting them. Audit an existing graph before relying on it.
:::
