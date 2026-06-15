# Plan — Layer producer attribution + single dispatch endpoint

**Status:** Implemented (2026-06-14). Producer attribution (orbital side: annotations + extended `ORBITAL_BUNDLER_URLS`; orb side: reads annotations + UI reshape) is live; verified end-to-end via `make e2e-divergence`. Deferred to a later session: `IsOrbitalNative` removal (requires ent migration) and `ConsumerConfig` refactor to consumer-name (functional but cosmetic — current shape works against cb's new `/dispatch`).

**Origin:** Design discussion on 2026-06-14 about orb's import-history layers view. Two intertwined issues surfaced:

1. orb labels non-graph layers as `bundler` (producer-side vocabulary leaking to the edge UI). The operator deploying orb can't tell what each layer is, only that something was dispatched.
2. cb-controller exposes two endpoints (`/consume` for the manifest layer, `/mapping` for the mapping layer). orb has to know both URLs. That couples orb's config to the consumer's internal handler shape.

## Settled decisions

1. **Single dispatch endpoint per consumer.** cb-controller exposes `POST /dispatch`. It reads `Content-Type` from the request header and routes internally to the manifest-apply or mapping-cache handler. The two endpoints (`/consume`, `/mapping`) are removed — no compat aliases. MVP, hard-cut.

2. **orb's `ORB_CONSUMERS` becomes consumer-centric, not media-type-centric.** Today the env maps media type → URL; the URL knows which handler to hit. After: env maps **consumer name → base URL**, and orb sends `Content-Type: <media type>` on each dispatch. Consumer is responsible for its own internal routing.

3. **Producer identity travels with the artifact, not the edge config.** Orbital writes a per-layer OCI annotation `com.armada.orbital.producer` at push time. orb reads it during import and displays it. Edge operators do not configure producer names — they're declared by whoever built the artifact and tamper-evident via cosign.

4. **`ORBITAL_BUNDLER_URLS` extends to carry friendly names.** Format: `name=url[,name=url,...]`. The name is what shows up in the producer annotation and in orb's UI. Example: `configbundle-bundler=http://localhost:8020/bundle`.

5. **orb's layers view is reshaped around operator needs.** Columns become: `Producer | Media Type | Consumer | Status`. The current `Role` column (bundler/dgraph/unknown) disappears — its value is now derived from producer + consumer presence:
   - Producer `orbital` + consumer `(local dgraph)` = graph layer
   - Producer `<name>` + consumer URL = dispatched
   - Producer `<name>` + consumer `(no consumer registered)` = orb saw the layer but couldn't route it (clear misconfig signal)

6. **Fallback when annotation absent.** Older artifacts in the registry won't have `com.armada.orbital.producer`. orb displays `(unannotated)` rather than guessing. Operator's signal to re-publish.

7. **"configbundle-bundler" is the chosen name** for the current bundler. Acknowledged as overloaded with the term "bundler" elsewhere — accepted as a pragmatic MVP choice.

## Open questions (decide at implementation time)

- **Source of the name on the producer side.** Option A: declared in `ORBITAL_BUNDLER_URLS` (cleaner, decided above). Option B: the bundler returns its own identity in the response body. B is more dynamic but adds a new field to the bundler contract — leave for v2 if needed.
- **Annotation key naming.** Going with `com.armada.orbital.producer`. Reverse-DNS namespaced, follows OCI annotation conventions. Don't shorten.
- **Should orb still show full media type in the new UI?** Yes — power users need it for debugging media-type mismatches. Could be collapsible or in a tooltip if it's too noisy in the row.
- **What about the `IsOrbitalNative` field in orbital's artifact response?** It becomes redundant once producer attribution is the source of truth. Either keep it as a derived/cached signal or remove it. Probably remove — fewer fields = clearer contract.

## Implementation phases

### Phase 1 — cb-controller (configbundle repo)

**See [`cb-controller-dispatch-rewrite.md`](./cb-controller-dispatch-rewrite.md) for the full self-contained plan owned by the cb session.** That plan covers:

- `POST /dispatch` with `Content-Type` routing (replacing `/consume` and `/mapping`)
- Mapping persisted to a per-CR `ConfigMap` (not in-memory cache)
- Event-driven debounced reporter (replacing the ticker)
- Content-hash dedup
- `DIVERGENCE_REPORTER_DEBOUNCE` env var (default 5s)

The one orb-side change required by Phase 1: **orb must dispatch the manifest layer before the mapping layer** so the ConfigBundle CR exists when the mapping layer arrives and its OwnerReference can be set. One-line sort by media type in orb's `Dispatcher.Dispatch`. Tracked here, not in the cb plan.

### Phase 2 — orb

- **Config** (`internal/orbconfig/config.go`): change `ORB_CONSUMERS` from `mediaType=url` pairs to `consumerName=baseURL` pairs. Drop media-type from the env entirely.
- **Dispatch** (`internal/orb/dispatch.go`): one URL per consumer; send `Content-Type: <media type of layer>` header; loop over consumers and POST every non-graph layer to every consumer. Consumer ignores unknown content types with 415. (Alternative: each layer goes to one consumer based on a routing table — but that pulls media-type knowledge back into orb. Broadcast is simpler.)
- **Importer** (`internal/orb/importer.go`): read OCI manifest's per-layer annotations during import. Populate a new `Producer string` field on `LayerRecord`.
- **API** (`internal/orbserver/import_handlers.go`): expose `producer` in the layer JSON.
- **UI** (`web/templates/orb/pages/import-history.gohtml` + the layers fragment): swap the `Role` column for `Producer`; keep `Media Type`, `Consumer`, `Status` columns; handle the no-annotation fallback.

### Phase 3 — orbital

- **Config** (`internal/config/config.go`): extend `ORBITAL_BUNDLER_URLS` parser to accept `name=url` pairs. Validate names are non-empty and unique.
- **Bundler client** (`internal/bundler/client.go`): carry a `Name string` on each `Client` so the publisher knows which name to annotate with.
- **Publisher** (`internal/oci/publisher.go`): when building the OCI manifest in `pushArtifact`, attach `com.armada.orbital.producer=<bundler-name>` to each layer the bundler returned. Attach `com.armada.orbital.producer=orbital` to the two graph layers (data + schema) so the convention is uniform.
- **UI alignment**: update the orbital-side artifact-layers display in `web/templates/orbital/pages/signed-artifacts.gohtml` (and adjacent fragments) to show the producer label from the annotation, so orbital + orb display the same string. Drop the `IsOrbitalNative` blue/yellow distinction; replace with producer string.

### Phase 4 — cross-cutting cleanup

- Remove `IsOrbitalNative` field from `ent/schema/registry_artifact.go` (and any places it surfaces in JSON or templates) since producer string subsumes it.
- Update `docs/reference/OCI.md` with the annotation contract.
- Update memory `[[feedback-orb-vs-orbital-vocabulary]]` — the "bundler/unknown" dual-vocabulary rule is superseded by producer-annotation truth. Both apps display the same producer string. The memory becomes a small note pointing here.

### Phase 5 — verification

- Existing `make e2e-divergence` exercises the full publish + import flow. After implementation, the import-history UI should show `Producer: configbundle-bundler` for the two layers from the bundler and `Producer: orbital` for the two graph layers, with consumer URLs and dispatch status visible.
- Add a unit test on the orbital side verifying the annotation is set on each layer at push time.
- Add a unit test on the orb side verifying the annotation is read into `LayerRecord.Producer`.

## Risks + tradeoffs

- **Cross-repo coordination.** orbital + orb + cb-controller all need to ship the new behavior together. Order: cb-controller first (still serves old endpoints during transition or hard-cut depending on MVP appetite); then orbital writes annotations; then orb consumes them. Each step is independently deployable as long as fallbacks (unannotated → "(unannotated)") work.
- **Broadcast vs targeted dispatch in orb.** I'm proposing orb broadcasts every dispatch-eligible layer to every registered consumer, relying on the consumer to 415 on unknown types. Alternative: orb maintains a routing table that maps media type → consumer URL (closer to today's behavior). Broadcast is simpler but inefficient if you ever have many consumers. Probably fine at MVP scale (≤3 consumers). Revisit if it becomes a real problem.
- **OCI annotation size limits.** Annotations are limited in some registries; producer name is tiny (`configbundle-bundler` is 20 bytes). No issue at our scale.
- **Backward compat with deployed orbs.** Existing orbs reading the new orbital output: they ignore unknown annotations safely. Existing orbital writing to old orbs: same — annotations are additive. So a partial rollout is safe.

## What this is NOT

- Not a redesign of the bundler protocol. Bundlers still respond with `Result{Layers, ConsumedResolutionIDs}`. We're just adding metadata at the OCI assembly step.
- Not a change to the dispatch wire protocol's body — just headers and annotations.
- Not a security boundary change. Producer annotations are inside the cosign-signed manifest; tampering invalidates the signature.

## Done definition

- One env var per consumer (orb) and per bundler (orbital), each with a friendly name.
- cb-controller exposes `POST /dispatch` only.
- Every OCI layer in artifacts produced by orbital carries `com.armada.orbital.producer`.
- orb's import-history layers view shows `Producer | Media Type | Consumer | Status` — never `bundler` or `dispatched` as a label.
- `make e2e-divergence` passes unchanged.
- Memory updated; ADR not needed (this is implementation detail of the OCI artifact + edge-dispatch contract, both of which are already-settled).
