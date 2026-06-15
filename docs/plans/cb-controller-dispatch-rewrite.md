# Plan — cb-controller: single dispatch endpoint + event-driven debounced reporter

**Status:** Designed (2026-06-14). Ready for the cb session to implement.

**Audience:** The cb session working in `~/armada/configbundle/`. Mirror this file into that repo's `docs/plans/` before starting so it's self-contained there.

**Related context (read for background, do not implement from):** [`layer-producer-attribution.md`](./layer-producer-attribution.md) covers the orbital-side OCI annotation work and orb-side UI changes. Those are owned by separate sessions and do not block this plan — the two can ship in parallel.

---

## What this plan covers (in scope)

1. Replace `/consume` + `/mapping` endpoints on cb-controller with a single `POST /dispatch` that routes by `Content-Type`.
2. Replace the in-memory `MappingCache` with a per-ConfigBundle `ConfigMap` for mapping storage.
3. Replace the polling `DivergenceReporter` ticker with an event-driven controller-runtime watch + debouncer.
4. Add content-hash dedup so identical override sets don't re-POST.

## Out of scope

- Producer annotations on OCI layers (orbital side — separate plan).
- orb's `ORB_CONSUMERS` env schema change (orb side — separate plan).
- orb's import-history UI changes (orb side — separate plan).
- Changes to the bundler HTTP service (not affected).

---

## Settled decisions

1. **Single endpoint, content-routed.** `POST /dispatch` reads `Content-Type` and routes internally:
   - `application/vnd.armada.configbundle.manifest.v1+yaml` → apply ConfigBundle CR via SSA
   - `application/vnd.armada.configbundle.mapping.v1+json` → store mapping in ConfigMap
   - else → `415 Unsupported Media Type`
   - Drop `/consume` and `/mapping` handlers. Hard-cut, no aliases.

2. **Mapping persists in a per-CR `ConfigMap`, not on CR status.** Rationale: ConfigBundle CRs are already large; status size counts against the K8s ~1MB soft limit per object. ConfigMap gets its own envelope.

3. **ConfigMap naming convention:** `<configbundle-name>-mapping` in the same namespace.

4. **ConfigMap lifecycle:** owned by the ConfigBundle CR via `metadata.ownerReferences` with `controller: true, blockOwnerDeletion: true`. K8s garbage-collects the ConfigMap when the CR is deleted. No manual cleanup needed.

5. **ConfigMap shape:**
   ```yaml
   apiVersion: v1
   kind: ConfigMap
   metadata:
     name: <configbundle-name>-mapping
     namespace: <same as CR>
     labels:
       armada.ai/configbundle: <configbundle-name>
       armada.ai/component: mapping
     ownerReferences:
     - apiVersion: armada.ai/v1
       kind: ConfigBundle
       name: <configbundle-name>
       uid: <CR uid>
       controller: true
       blockOwnerDeletion: true
   data:
     digest: sha256:<digest the mapping was produced for>
     mapping.json: |
       {"items":[ ... ]}
   ```

6. **Layer-arrival ordering:** orb dispatches the **manifest layer before the mapping layer** within a single import. cb-controller's mapping handler can rely on the CR existing when mapping arrives. (orb-side change tracked in the layer-producer-attribution plan; trivial — sort by media type before dispatch.)

7. **Reporter is event-driven, not ticker-driven.** Register a second controller-runtime controller on the existing Manager that watches `ConfigBundle` with a predicate filtering for `managedFields` deltas. Drop the `time.Ticker` + `DIVERGENCE_REPORTER_INTERVAL` env var.

8. **Debounce window: 5 seconds, env-var configurable.** New env var `DIVERGENCE_REPORTER_DEBOUNCE` (default `5s`). Use `time.ParseDuration` so operators can set `2s` / `500ms` / `30s`.

9. **Debounce implementation:** per-key `lastEventAt` map maintained in the predicate; reconciler returns `RequeueAfter(remaining)` until quiet for the full window, then POSTs.

10. **Content-hash dedup:** per-key `lastPostedHash` map maintained by the reporter. If the SHA256 of the override set matches the last successfully-POSTed hash, skip the POST. In-memory only; reset on controller restart (first reconcile after restart re-POSTs current state — orb idempotently stores).

11. **No backstop ticker.** controller-runtime's built-in periodic re-list (default ~10h) catches missed events. Adequate at this scale.

---

## End-to-end flow after this change

**On orb import:**
1. Orb dispatches manifest layer → cb-controller's `/dispatch` → `Content-Type: ...manifest.v1+yaml` → SSA-apply ConfigBundle CR.
2. Orb dispatches mapping layer → cb-controller's `/dispatch` → `Content-Type: ...mapping.v1+json` → `Create-or-Update` the `<cb-name>-mapping` ConfigMap with OwnerReference to the CR.

**On `local:admin` SSA:**
1. K8s API server mutates the CR. Watch fires on cb-controller's reporter controller.
2. Predicate checks: did `managedFields` change? Yes → update `lastEventAt[key]=now`, enqueue reconcile.
3. Reconciler fires: `elapsed = now - lastEventAt[key]`. If `elapsed < debounceWindow`, return `RequeueAfter(window - elapsed)`.
4. Eventually `elapsed >= debounceWindow` → reconciler computes override set from current CR + mapping (loaded from the CR's matching ConfigMap), computes content hash.
5. If hash matches `lastPostedHash[key]`, skip POST.
6. Otherwise POST to orb's `/api/v1/divergence`, on 2xx update `lastPostedHash[key]`.

**On cb-controller restart:**
1. controller-runtime cache rebuilds from etcd. CRs and ConfigMaps still there.
2. First reconcile per CR fires (lastEventAt = zero → elapsed = huge → no requeue).
3. Reporter computes overrides, posts (cache primes naturally).

**On orb temporarily unreachable:**
1. POST returns error → reconcile returns error → work queue requeues with exponential backoff.
2. When orb returns, next retry succeeds. `lastPostedHash` updated.

**Quiet state (no SSAs, no imports):**
- Zero events fire. Zero reconciles. Zero POSTs to orb. The reporter is silent.

---

## Implementation steps

### Step 1: `/dispatch` endpoint

- File: `internal/controller/consume.go` (consider renaming to `dispatch.go` at the end)
- Add `POST /dispatch` handler with `Content-Type` switch.
- Move `applyManifest` and `cacheMapping` logic into routed sub-handlers (no behavior change yet).
- Remove `mux.HandleFunc("POST /consume", ...)` and `mux.HandleFunc("POST /mapping", ...)`.
- `X-Orb-Digest` header still required; surface as 400 if absent.
- Tests: existing tests for `handleConsume` and `handleMapping` get retargeted to `/dispatch` with the appropriate `Content-Type`. Add a 415 case for an unknown content type.

### Step 2: Mapping → ConfigMap

- Replace the `MappingCache` struct in `internal/controller/consume.go` (or wherever it lives) with a thin reader/writer:
  - `Write(ctx, cbName, namespace, digest, mappingBytes) error` — create-or-update the ConfigMap, set OwnerReference from the CR lookup.
  - `Read(ctx, cbName, namespace) (*Mapping, error)` — get the ConfigMap, parse `data["mapping.json"]`.
- Delete `MappingCache.Load`/`Store` callers; they read/write via the new path.
- Mapping handler:
  1. Look up the ConfigBundle CR by name+namespace (returns 404 if absent → respond 409 Conflict so orb retries; manifest should have arrived first per ordering decision).
  2. Build the ConfigMap with OwnerReference.
  3. `CreateOrUpdate` via controller-runtime client.
- Tests: envtest verifies the ConfigMap is created with OwnerReference and survives controller restart, gets GC'd on CR delete.

### Step 3: Event-driven reporter

- New file: `internal/controller/divergence_reporter_controller.go`
- Replace the current `DivergenceReporter` (a `ctrl.Runnable` with `time.Ticker`) with a controller-runtime controller:
  ```go
  func (r *Reporter) SetupWithManager(mgr ctrl.Manager) error {
      return ctrl.NewControllerManagedBy(mgr).
          For(&armadav1.ConfigBundle{}).
          WithEventFilter(r.predicate()).
          Complete(r)
  }
  ```
- `predicate()` returns a `predicate.Funcs` that on `UpdateEvent` compares `oldObj.ManagedFields` vs `newObj.ManagedFields` looking for any `local:*` ownership delta. On match: update `lastEventAt[key]=time.Now()` and return `true`.
- Drop the standalone Runnable registration. Drop `DIVERGENCE_REPORTER_INTERVAL` env handling.

### Step 4: Debouncer in reconciler

- Reconciler:
  ```go
  func (r *Reporter) Reconcile(ctx, req) (reconcile.Result, error) {
      r.mu.Lock()
      last := r.lastEventAt[req.NamespacedName]
      r.mu.Unlock()

      elapsed := time.Since(last)
      if elapsed < r.debounceWindow {
          return reconcile.Result{RequeueAfter: r.debounceWindow - elapsed}, nil
      }
      // ... compute overrides, hash, post ...
  }
  ```
- `debounceWindow` populated from env: `DIVERGENCE_REPORTER_DEBOUNCE` (default `5s`). Parse with `time.ParseDuration`. Refuse to start if parse fails.

### Step 5: Content-hash dedup

- Inside reconciler, after computing the override set:
  ```go
  h := sha256.Sum256(canonicalJSON(overrides))
  r.mu.Lock()
  if r.lastPostedHash[req.NamespacedName] == h {
      r.mu.Unlock()
      return reconcile.Result{}, nil
  }
  r.mu.Unlock()
  // POST...
  // on 2xx:
  r.mu.Lock()
  r.lastPostedHash[req.NamespacedName] = h
  r.mu.Unlock()
  ```
- `canonicalJSON` must sort keys to ensure stable hash regardless of map iteration order.

### Step 6: Wiring + cleanup

- `cmd/main.go`: register the new controller via `SetupWithManager`. Remove the old `Runnable` registration. Remove ticker-related options.
- Update `make up` instructions in cb-controller's CLAUDE.md / README — `DIVERGENCE_REPORTER_INTERVAL` is gone; `DIVERGENCE_REPORTER_DEBOUNCE` is new.

### Step 7: Tests

- **Unit**: predicate fires on `local:*` ownership change, ignores spec-only updates.
- **Unit**: debouncer returns `RequeueAfter` until quiet window passes.
- **Unit**: content-hash dedup skips POST when override set is unchanged.
- **Integration (envtest)**: full flow — dispatch manifest → CR created → dispatch mapping → ConfigMap created with OwnerReference → SSA as `local:admin` → reconcile observed → after 5s, single POST to a stub orb intake.
- **Integration**: CR deletion → ConfigMap garbage-collected.

---

## Open questions to decide during implementation

- **Hash function**: SHA256 is overkill for in-memory dedup but is everywhere already. FNV-64 is faster and adequate. Pick whichever is one less import. Recommendation: SHA256 for consistency with the rest of the codebase.
- **ConfigMap data key**: `mapping.json` reads cleanly. Avoid `mapping` (ambiguous) or `data` (generic).
- **What if `local:admin` SSAs a field that isn't in the mapping?** Currently the reporter can't translate, so it logs and skips that field. After this change, behavior is unchanged. (Future enhancement: surface unmappable fields as a separate signal.)
- **Reporter starts before mapping arrives.** First few reconciles on a fresh CR may find no mapping ConfigMap → skip the POST and requeue. Document this as expected: divergence reports lag the first import by one mapping dispatch.

---

## Risks + mitigations

- **OwnerReference UID race.** Between manifest creating the CR and mapping arriving, the CR's UID is read once. If the CR is deleted and recreated between manifest and mapping (very unlikely), the OwnerReference would reference a stale UID. K8s would reject. Mitigation: respond 409 to orb so the import retries; or look up the CR fresh on each mapping handler call.
- **ConfigMap update conflicts.** Two rapid dispatches for different digests of the same CR race on ConfigMap write. CreateOrUpdate's optimistic concurrency handles this — second write wins; idempotent.
- **Burst SSAs across many CRs.** Work queue serializes by default. To handle 50+ CRs concurrently, bump `MaxConcurrentReconciles` to e.g. `5`. Worth noting but not required for MVP.
- **Debounce + restart.** On restart, `lastEventAt` is empty → first reconcile fires immediately. Acceptable — POSTing the current state right after a restart is correct behavior.

---

## Done definition

- `POST /dispatch` is the only consume endpoint. `/consume` and `/mapping` are gone.
- Mapping for each ConfigBundle lives in `<cb-name>-mapping` ConfigMap with OwnerReference to the CR.
- No `time.Ticker` anywhere in the reporter.
- `DIVERGENCE_REPORTER_INTERVAL` env var is removed; `DIVERGENCE_REPORTER_DEBOUNCE` is documented.
- A `local:admin` SSA produces a POST to orb within `debounce window + reconcile latency` (~5s).
- Rapid back-and-forth SSAs produce a single POST after the activity quiets.
- Unchanged override sets produce zero POSTs.
- Controller restart re-POSTs current state and re-arms the dedup cache.
- envtest covers happy path + restart + GC.

---

## Coordination with the orbital-side work

This plan can ship independently. The orbital-side producer-attribution work and orb's UI changes do not block any of this. The only orb-side change required by this plan is **dispatching the manifest layer before the mapping layer** (one-line sort by media type, tracked separately).

Once shipped:
- Local dev: `make e2e-divergence` should still pass.
- `DIVERGENCE_REPORTER_INTERVAL=15s` in the orbital repo's e2e script needs to be replaced with `DIVERGENCE_REPORTER_DEBOUNCE=2s` (or removed entirely if the 5s default is OK for tests).
