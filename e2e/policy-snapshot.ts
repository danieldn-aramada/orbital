// Save and restore the approval policies a developer already had.
//
// The specs need a clean slate — the approval gate is global state, and the
// policies page asserts on row counts — but achieving that by deleting
// everything the API returns wipes policies created on a real dev stack.
//
// An in-memory snapshot only protects a run that FINISHES. Kill the suite (or
// the server) mid-run and afterAll never fires; the next run then snapshots the
// already-empty state and faithfully restores nothing. That is not theoretical:
// it is how a developer's `colo` policy was lost, and every subsequent run made
// the loss permanent.
//
// So the snapshot lives in a FILE, and the rule that makes it durable is:
// **write it only if it does not already exist.** A leftover file means the
// previous run was interrupted, and it — not the current, emptied state — is the
// true baseline. The file is removed only after a successful restore.

import { APIRequestContext } from '@playwright/test'
import * as fs from 'fs'
import * as path from 'path'

const SNAPSHOT = path.join(__dirname, '.policies-snapshot.json')

type Policy = {
  id: string
  // Empty for an all-namespaces policy — the two are mutually exclusive, and
  // the API rejects a body carrying neither.
  namespace: string
  allNamespaces?: boolean
  allTypes?: boolean
  types?: string[]
  requiredApprovals: number
  bypassRoles?: string[]
  enabled: boolean
}

async function list(request: APIRequestContext): Promise<Policy[]> {
  const res = await request.fetch('/api/v1/approval-policies', { method: 'GET' })
  return res.ok() ? await res.json() : []
}

/** savePolicies snapshots the current policies unless a snapshot already exists. */
export async function savePolicies(request: APIRequestContext) {
  if (fs.existsSync(SNAPSHOT)) return   // an interrupted run's baseline wins
  fs.writeFileSync(SNAPSHOT, JSON.stringify(await list(request), null, 2))
}

export async function clearPolicies(request: APIRequestContext) {
  for (const p of await list(request)) {
    await request.fetch(`/api/v1/approval-policies/${p.id}`, { method: 'DELETE' })
  }
}

/**
 * restorePolicies puts the snapshot back and then discards it.
 *
 * The file is removed ONLY after every policy is recreated, so a failure part
 * way through leaves the baseline on disk for the next run to finish.
 */
export async function restorePolicies(request: APIRequestContext) {
  if (!fs.existsSync(SNAPSHOT)) return
  const saved: Policy[] = JSON.parse(fs.readFileSync(SNAPSHOT, 'utf8'))

  await clearPolicies(request)
  for (const p of saved) {
    const res = await request.fetch('/api/v1/approval-policies', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      // allNamespaces and namespace are exclusive: a global policy has no
      // namespace, and sending `namespace: undefined` alongside no
      // allNamespaces is rejected as "namespace is required" — which made a
      // global policy impossible to round-trip, failing every later run.
      data: JSON.stringify({
        ...(p.allNamespaces ? { allNamespaces: true } : { namespace: p.namespace }),
        allTypes: p.allTypes ?? true,
        types: p.types ?? [],
        requiredApprovals: p.requiredApprovals,
        bypassRoles: p.bypassRoles ?? [],
        enabled: p.enabled,
      }),
    })
    if (!res.ok()) {
      // Keep the file: whatever went wrong, the baseline is still recoverable.
      throw new Error(`restore policy for ${p.allNamespaces ? "(all namespaces)" : p.namespace} failed: ${res.status()} ${await res.text()}`)
    }
  }
  fs.unlinkSync(SNAPSHOT)
}
