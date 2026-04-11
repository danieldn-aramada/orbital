# Orbital

Orbital is a configuration management framework for modular data centers with
focus on multi-site and air-gapped deployments.

## Concepts

`orbital` - Control plane running in cloud. Holds design intent (i.e.
configuration items) for all modular data centers and serves APIs for digital
twin building. Pushes configuration down to `orbs`.

`orb` - Standalone service running in modular data center. Serves configuration
and can detect drift. Suitable for air-gapped deployments.

`orbital import [orb]` - Workflow that merges existing configuration from existing modular
data center with `orbital`.

`orbital export [orb]` - Workflow that exports configuration of modular data
center and pushes down to `orb`.

## Example


## Stack 

`go` - Implementation language for `orbital` / `orb`.

`dgraph` - Graph database with GraphQL API on top of RDF-like storage engine.
Stores all configuration items for `orbital` / `orb`.

`postgres` - Relational database. Stores metadata and general backend service for `orbital`.

`valkey` - Caching for `Orbital`.
