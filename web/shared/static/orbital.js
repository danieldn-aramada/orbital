// orbital.js — orbital-specific page logic.
//
// ─── Sections (Cmd-F any header text below) ──────────────────────────────────
//   Server drill-down in DC tab (dblclick → HTMX load into tab)
//   Inventory page
//   Data Centers page
//   Servers page
//   Clusters page
//   Cross-app navigation and reload buttons
//   Backups
//   Export page
//   Edge Delivery page
//   DC edit modal
//   Cluster edit modal
//   HTMX afterSwap — orbital editor cleanup
//   Server edit modal
//   Audit log page
//   Restore
//   Users page
//   Divergence reports page         ← includes break-glass delete
//   Config-item delete modal (DataCenter / Server)
//   Report Issue modal
//   Module exports to window (e2e + cross-module only)   ← last; not a bridge
//
// Section ordering rule: feature sections come first, then the exports block
// at the bottom. Do NOT add code after that block — it's the file's closing
// boundary between module logic and global registration. (Click handlers
// stay inside their feature section as delegated listeners — see UI.md.)

// configitem-editor.js: generic edit-modal submit handler. Imported for side
// effect (registers window.initConfigItemEditor) so any page's modal shim
// can call it without re-importing.
import './configitem-editor.js'

import {
  BASE,
  TabItem,
  unloadTab,
  deleteTab,
  saveTab,
  replaceCurrentTab,
  getCurrentTab,
  activateTab,
  displayTabContent,
  setCurrentTab,
  showDatacenterSkeleton,
  showServerSkeleton,
  showClusterSkeleton,
  fetchWithMinDelay,
  initDetailTabs,
  dtWrapLengthSelect,
  openServerTab,
  initServerEventsTable,
  renderTimestamps,
  formatTimestamp,
  initInventoryTable,
  initDatacenterTable,
  initServerListTable,
  initClusterTable,
  dtIPv4Render,
  loadDataCenterTab,
  loadServerListTab,
  loadClusterTab,
  saveServerTab,
  saveClusterTab,
  initDatacenterTabRestoration,
  initServerListTabRestoration,
  initClusterTabRestoration,
  initNetworkDeviceTable,
  loadNetworkDeviceTab,
  saveNetworkDeviceTab,
  initNetworkDeviceTabRestoration,
  safeDomId,
  initRowNavigation,
  initLinkNavigation,
  initReloadButtons,
} from './shared.js'

// ─── Server drill-down in DC tab (dblclick → HTMX load into tab) ─────────────

document.addEventListener('dblclick', function (e) {
  const row = e.target.closest('tr[data-server-id]')
  if (!row) return
  const serverOrbId = row.dataset.serverId
  const dcOrbId = row.dataset.dcId
  const tabContent = document.getElementById('tab-content-' + safeDomId(dcOrbId))
  if (!tabContent) return
  tabContent.dataset.loaded = ''
  htmx.ajax('GET', BASE + '/servers/' + encodeURIComponent(serverOrbId) + '?dcCtx=1', { target: tabContent, swap: 'innerHTML' })
})

// ─── Inventory page ───────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => { initInventoryTable() })

// ─── Data Centers page ────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  initDatacenterTable({
    onRowOpen: (data) => {
      const displayName = data.name
      const orbId = data.orbId
      const domId = safeDomId(orbId)
      const tab = document.getElementById(`tab-${domId}`)
      if (tab) {
        tab.click()
      } else {
        loadDataCenterTab(displayName, orbId)
        saveTab(displayName, orbId)
        document.getElementById(`tab-${domId}`).click()
      }
    },
  })
})

window.addEventListener('load', initDatacenterTabRestoration)

// ─── Servers page ─────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  initServerListTable({
    onRowOpen: (data) => {
      const orbId = data.orbId
      const domId = safeDomId(orbId)
      const displayName = data.hostname !== '—' ? data.hostname : data.serviceTag
      const tab = document.getElementById(`tab-srv-${domId}`)
      if (tab) {
        tab.click()
      } else {
        loadServerListTab(displayName, orbId)
        saveServerTab(displayName, orbId)
        document.getElementById(`tab-srv-${domId}`).click()
      }
    },
  })
})

window.addEventListener('load', initServerListTabRestoration)

// ─── Clusters page ────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  initClusterTable({
    onRowOpen: (data) => {
      const orbId = data.orbId
      const domId = safeDomId(orbId)
      const displayName = data.name
      const tab = document.getElementById(`tab-cluster-${domId}`)
      if (tab) {
        tab.click()
      } else {
        loadClusterTab(displayName, orbId)
        saveClusterTab(displayName, orbId)
        document.getElementById(`tab-cluster-${domId}`).click()
      }
    },
  })
})

window.addEventListener('load', initClusterTabRestoration)

// ─── Network devices page ─────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  initNetworkDeviceTable({
    onRowOpen: (data) => {
      const orbId = data.orbId
      const domId = safeDomId(orbId)
      const displayName = data.name
      const tab = document.getElementById(`tab-network-device-${domId}`)
      if (tab) {
        tab.click()
      } else {
        loadNetworkDeviceTab(displayName, orbId)
        saveNetworkDeviceTab(displayName, orbId)
        document.getElementById(`tab-network-device-${domId}`).click()
      }
    },
  })
})

window.addEventListener('load', initNetworkDeviceTabRestoration)

// ─── Network device edit modal ────────────────────────────────────────────────

const networkDeviceEditors = new Map()
window.networkDeviceEditors = networkDeviceEditors

function reloadNetworkDeviceFragment(orbId) {
  const domId = safeDomId(orbId)
  const target = document.getElementById('tab-content-network-device-' + domId)
  if (!target) return Promise.resolve()
  return fetchWithMinDelay('/network/' + encodeURIComponent(orbId))
    .then(html => {
      target.innerHTML = html
      htmx.process(target)
      renderTimestamps(target)
      const detailTabs = target.querySelector('[id^="network-device-detail-tabs-"]')
      if (detailTabs) initDetailTabs(detailTabs)
      networkDeviceEditors.delete(domId)
    })
    .catch(() => {
      target.innerHTML = '<div class="notification is-danger is-light is-size-7 m-4"><strong>Reload failed.</strong> Check your connection and try again.</div>'
    })
}

// Reload button on the device detail page.
document.addEventListener('click', (e) => {
  const btn = e.target.closest('.js-network-device-reload')
  if (!btn) return
  btn.classList.add('is-loading')
  reloadNetworkDeviceFragment(btn.dataset.networkDeviceId).finally(() => btn.classList.remove('is-loading'))
})

document.addEventListener('click', function (e) {
  const editBtn = e.target.closest('[data-network-device-edit-id]')
  if (editBtn) {
    const id = editBtn.dataset.networkDeviceEditId
    const modal = document.getElementById('edit-modal-networkdevice-' + id)
    if (!modal) return

    if (!networkDeviceEditors.has(id)) {
      const dataEl = document.getElementById('network-device-edit-data-' + id)
      const targetsEl = document.getElementById('network-device-edit-targets-' + id)
      const initialState = JSON.parse(dataEl ? dataEl.textContent.trim() : '{}')
      const targets = JSON.parse(targetsEl ? targetsEl.textContent.trim() : '[]')
      const editorTarget = document.getElementById('network-device-json-editor-' + id)
      const editor = new window.JSONEditor({
        target: editorTarget,
        props: { mode: 'text', mainMenuBar: false },
      })
      editor.set({ text: JSON.stringify(initialState, null, 2) })
      networkDeviceEditors.set(id, editor)

      const errorEl = document.getElementById('network-device-edit-error-' + id)
      const showError = (msg) => { errorEl.textContent = msg; errorEl.style.display = '' }
      const clearError = () => { errorEl.textContent = ''; errorEl.style.display = 'none' }

      const onSubmit = window.initConfigItemEditor({
        modal,
        editor,
        initialState,
        targets,
        reloadOrbId: modal.dataset.orbId,
        reloadFn: reloadNetworkDeviceFragment,
        showError,
        clearError,
      })

      document.getElementById('network-device-edit-submit-' + id).addEventListener('click', async () => {
        const btn = document.getElementById('network-device-edit-submit-' + id)
        btn.classList.add('is-loading')
        btn.disabled = true
        try {
          const ok = await onSubmit()
          if (ok) {
            modal.classList.remove('is-active')
            document.documentElement.style.overflow = ''
          }
        } finally {
          btn.classList.remove('is-loading')
          btn.disabled = false
        }
      })
    }

    const errorEl = document.getElementById('network-device-edit-error-' + id)
    if (errorEl) { errorEl.textContent = ''; errorEl.style.display = 'none' }
    modal.classList.add('is-active')
    document.documentElement.style.overflow = 'hidden'
    return
  }

  const closeBtn = e.target.closest('[data-network-device-modal-close]')
  if (closeBtn) {
    const id = closeBtn.dataset.networkDeviceModalClose
    const modal = document.getElementById('edit-modal-networkdevice-' + id)
    if (modal) {
      modal.classList.remove('is-active')
      document.documentElement.style.overflow = ''
    }
  }
})

// ─── Cross-app navigation and reload buttons ──────────────────────────────────
// Shared handlers extracted from this file so orb.js gets them for free.
// Edit-modal handlers (data-{dc,srv,cluster}-edit-id) stay below — they
// only render when Actions.Edit is true, which is orbital-only.

initRowNavigation()
initLinkNavigation()
initReloadButtons({
  onDcReloaded: (domId) => dcEditors.delete(domId),
  onSrvReloaded: (target) => {
    const srvDetailTabs = target.querySelector('[id^="srv-detail-tabs-"]')
    if (srvDetailTabs) srvEditors.delete(srvDetailTabs.id.replace('srv-detail-tabs-', ''))
    const dcDetailTabs = target.querySelector('[id^="dc-detail-tabs-"]')
    if (dcDetailTabs) dcEditors.delete(dcDetailTabs.id.replace('dc-detail-tabs-', ''))
  },
})

// ─── Backups ──────────────────────────────────────────────────────────────────

let pendingDeleteId = null

function loadBackups() {
  const tbody = document.getElementById('backup-tbody')
  if (!tbody) return
  fetch(BASE + '/api/v1/backup/jobs', { headers: { 'HX-Request': 'true' } })
    .then(r => r.text())
    .then(html => { tbody.innerHTML = html })
    .catch(() => {})
}

function triggerBackup() {
  const btn = document.getElementById('btn-backup')
  const msg = document.getElementById('backup-status-msg')
  btn.classList.add('is-loading')
  btn.disabled = true
  msg.style.display = 'none'

  fetch(BASE + '/api/v1/backup', { method: 'POST' })
    .then(r => r.json())
    .then(data => {
      if (data.error) {
        msg.textContent = data.error
        msg.style.display = ''
        btn.classList.remove('is-loading')
        btn.disabled = false
      } else {
        loadBackups()
        pollBackup(data.id)
      }
    })
    .catch(() => {
      msg.textContent = 'Request failed.'
      msg.style.display = ''
      btn.classList.remove('is-loading')
      btn.disabled = false
    })
}

function pollBackup(jobId) {
  const btn = document.getElementById('btn-backup')
  const interval = setInterval(() => {
    fetch(BASE + '/api/v1/backup/jobs/' + jobId)
      .then(r => r.json())
      .then(data => {
        loadBackups()
        if (data.status === 'completed' || data.status === 'failed') {
          clearInterval(interval)
          btn.classList.remove('is-loading')
          btn.disabled = false
        }
      })
      .catch(() => { clearInterval(interval); btn.classList.remove('is-loading'); btn.disabled = false })
  }, 2000)
}

function downloadBackup(id) {
  fetch(BASE + '/api/v1/backup/jobs/' + id + '/download')
    .then(r => r.json())
    .then(data => { if (data.url) window.open(data.url, '_blank') })
    .catch(() => {})
}

function openDeleteModal(id, label) {
  pendingDeleteId = id
  document.getElementById('delete-modal-detail').textContent = 'Backup initiated at: ' + label
  document.getElementById('delete-modal').classList.add('is-active')
}

function closeDeleteModal() {
  pendingDeleteId = null
  document.getElementById('delete-modal').classList.remove('is-active')
  const btn = document.getElementById('delete-confirm-btn')
  btn.classList.remove('is-loading')
  btn.disabled = false
}

function confirmDelete() {
  if (!pendingDeleteId) return
  const btn = document.getElementById('delete-confirm-btn')
  btn.classList.add('is-loading')
  btn.disabled = true

  fetch(BASE + '/api/v1/backup/jobs/' + pendingDeleteId, { method: 'DELETE' })
    .then(r => {
      if (r.status === 204 || r.ok) {
        closeDeleteModal()
        loadBackups()
      } else {
        return r.json().then(d => { throw new Error(d.error || 'Delete failed') })
      }
    })
    .catch(err => {
      btn.classList.remove('is-loading')
      btn.disabled = false
      alert(err.message)
    })
}

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('backup-tbody')) return
  loadBackups()
})

// Delegated handlers for Backups page + delete modal. Listeners on document
// survive HTMX swaps of backup-jobs-tbody. See docs/reference/UI.md.
document.addEventListener('click', (e) => {
  if (e.target.closest('.js-backup-trigger')) { triggerBackup(); return }
  const dl = e.target.closest('.js-backup-download')
  if (dl) { downloadBackup(dl.dataset.backupId); return }
  const del = e.target.closest('.js-backup-delete')
  if (del) { openDeleteModal(del.dataset.backupId, del.dataset.backupLabel); return }
  if (e.target.closest('.js-delete-modal-close')) { closeDeleteModal(); return }
  if (e.target.closest('.js-delete-confirm')) { confirmDelete(); return }
})

// Test Connection buttons are declarative — see backups.gohtml and
// divergence-reports.gohtml. The button posts to /api/v1/backup/test-connection
// with HX-Request: true and the server returns an HTML fragment swapped into
// the result slot. No JS handler needed.

// ─── Export page ──────────────────────────────────────────────────────────────

let exportPollTimer = null

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('export-jobs-tbody')) return

  const dcSelect = document.getElementById('export-datacenter-select')
  const destSelect = document.getElementById('export-destination-select')
  const submitBtn = document.getElementById('export-submit-btn')
  const testRow = document.getElementById('test-connection-row')
  const ociOption = destSelect?.querySelector('option[data-oci="true"]')
  const ociPrefix = destSelect?.dataset.ociRegistryPrefix || ''

  // Refresh the OCI destination option's label to include the selected DC's
  // NAME (not orbId — the registry path uses the human name). Preserves
  // "{datacenter-name}" placeholder when no DC selected.
  const refreshOciLabel = () => {
    if (!ociOption) return
    const selected = dcSelect?.selectedOptions?.[0]
    const dcName = selected?.dataset?.name || '{datacenter-name}'
    if (ociPrefix) {
      ociOption.textContent = ociPrefix + dcName
    }
  }

  const refreshTestConnectionVisibility = () => {
    if (!testRow) return
    testRow.style.display = destSelect?.value === 'oci' ? 'flex' : 'none'
  }

  const refreshSubmitEnabled = () => {
    if (!submitBtn) return
    submitBtn.disabled = !dcSelect?.value || !destSelect?.value
  }

  const onAnyChange = () => {
    refreshOciLabel()
    refreshTestConnectionVisibility()
    refreshSubmitEnabled()
  }

  dcSelect?.addEventListener('change', onAnyChange)
  destSelect?.addEventListener('change', onAnyChange)
  onAnyChange()

  document.body.addEventListener('refreshExportJobs', () => loadExportJobsTable())

  loadExportJobsTable()
})

// handleExportSubmit routes the export button. Publishing to OCI opens the
// pre-publish review modal first (the desired-state diff vs the last published
// artifact) so the operator confirms what ships — the "confirm step" of publish.
// Download-only exports (no OCI baseline) fire directly.
function handleExportSubmit(btn) {
  const id = document.getElementById('export-datacenter-select')?.value
  if (!id) return
  const dest = document.getElementById('export-destination-select')?.value
  if (!dest) return
  if (dest === 'download') { doExportRequest(id, true, btn); return }
  openExportPreview(id)
}

// doExportRequest posts the atomic export request. download=false → publish to
// OCI; download=true → export-only zip retained for download.
//
// expectedContentHash (optional) is the guarded-Apply token from the preview:
// the server 409s if another writer changed intent during the review→Apply gap.
// On that 409 we re-open the preview with the FRESH diff rather than shipping
// something the operator never reviewed (terraform's "saved plan is stale" flow,
// made automatic).
function doExportRequest(id, download, btn, expectedContentHash, modal) {
  const buttons = document.querySelectorAll('.js-export-submit')
  buttons.forEach(b => { b.disabled = true })
  if (btn) btn.classList.add('is-loading')

  const payload = { orbId: id, download }
  if (expectedContentHash) payload.expectedContentHash = expectedContentHash

  fetch(BASE + '/api/v1/export', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
    .then(r => r.json().then(json => ({ status: r.status, json })))
    .then(({ status, json }) => {
      buttons.forEach(b => { b.disabled = false })
      if (btn) btn.classList.remove('is-loading')
      // Branch on `code`, never the message: this endpoint also 409s for
      // "export/restore already in progress" (code CONFLICT), which is NOT a
      // stale preview and must fall through to the normal error path.
      if (status === 409 && json.code === 'MVCC_CONFLICT') {
        loadExportPreview(id, 'Someone else changed this data center while you were reviewing. Here are the updated changes.')
        return
      }
      if (modal) modal.classList.remove('is-active')
      if (json.error) {
        showExportBox('is-warning')
        setExportSummary(json.error, 'is-warning')
        return
      }
      startExportPhases(download)
      pollExportStatus(json.id, download)
      loadExportJobsTable()
    })
    .catch(() => {
      buttons.forEach(b => { b.disabled = false })
      if (btn) btn.classList.remove('is-loading')
      if (modal) modal.classList.remove('is-active')
      showExportBox('is-danger')
      setExportSummary('Failed to start export.', 'is-danger')
    })
}

// openExportPreview opens the review modal and loads the desired-state diff from
// the preview API. Confirm proceeds to the actual publish, passing the previewed
// contentHash so the server can refuse if intent moved (guarded Apply). A
// preview failure never blocks publishing — the operator can still ship the
// current state, just without the guard.
// `note` renders a warning banner above the diff (used on a stale-hash retry).
function openExportPreview(id) {
  const modal = document.getElementById('export-preview-modal')
  const body = document.getElementById('export-preview-body')
  const confirmBtn = document.getElementById('export-preview-confirm')
  if (!modal || !body || !confirmBtn) { doExportRequest(id, false, null); return }

  confirmBtn.onclick = () => {
    // The modal deliberately stays OPEN through the request: the guard re-reads
    // and re-hashes the whole subgraph (seconds), and on a stale hash we refresh
    // the diff in place. Closing here would blank the screen mid-check and then
    // flicker back — it reads as a glitch. doExportRequest closes it on any
    // outcome that isn't a stale preview.
    doExportRequest(id, false, confirmBtn, confirmBtn.dataset.contentHash || '', modal)
  }
  modal.classList.add('is-active')
  loadExportPreview(id)
}

// loadExportPreview fills the already-open modal with a fresh diff. Split from
// openExportPreview so a stale-hash 409 can refresh in place without a
// close/reopen flicker. `note` renders a warning banner above the diff.
function loadExportPreview(id, note) {
  const body = document.getElementById('export-preview-body')
  const confirmBtn = document.getElementById('export-preview-confirm')
  if (!body || !confirmBtn) return

  body.innerHTML = '<div class="has-text-centered p-5"><span class="icon is-large has-text-grey"><i class="fa-solid fa-spinner fa-spin fa-2x"></i></span><p class="mt-2 has-text-grey">Computing changes…</p></div>'
  confirmBtn.disabled = true

  fetch(BASE + '/api/v1/export/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ orbId: id }),
  })
    .then(r => r.json())
    .then(json => {
      confirmBtn.dataset.contentHash = (json.current && json.current.contentHash) || ''
      renderExportPreview(body, json, note)
      confirmBtn.disabled = false
    })
    .catch(() => {
      // No hash => publish unguarded rather than sending a stale one.
      confirmBtn.dataset.contentHash = ''
      body.innerHTML = '<div class="notification is-warning is-light">Couldn’t compute the preview. You can still publish the current state.</div>'
      confirmBtn.disabled = false
    })
}

function renderExportPreview(body, json, note) {
  const esc = (x) => String(x == null ? '' : x).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]))
  const fmt = (v) => v == null ? '<span class="has-text-grey-light">∅</span>' : '<span class="is-family-monospace">' + esc(JSON.stringify(v)) + '</span>'
  const fmtDate = (iso) => { if (!iso) return ''; const d = new Date(iso); return isNaN(d.getTime()) ? String(iso).slice(0, 10) : d.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' }) }
  const s = json.summary || {}
  const b = json.lastPublishedVersion || {}
  const changed = (s.added || 0) + (s.removed || 0) + (s.modified || 0)

  // Stale-hash retry banner (someone else edited during the review).
  let head = note ? '<div class="notification is-warning is-light py-2 is-size-7 mb-3">' + esc(note) + '</div>' : ''

  // Plan line — the loudest thing in the modal.
  head += '<p class="mb-1"><strong>' + (changed === 0 ? 'No changes since last publish.' : ('Plan: ' + (s.modified || 0) + ' changed · ' + (s.added || 0) + ' added · ' + (s.removed || 0) + ' removed')) + '</strong> <span class="has-text-grey is-size-7">(' + (s.unchanged || 0) + ' unchanged)</span></p>'

  // Provenance — operator language, no jargon, no digest noise.
  if (b.state === 'first_export') head += '<p class="has-text-grey is-size-7 mb-3">First publish for this data center — every configuration item will be shipped.</p>'
  else if (b.state === 'unavailable') head += '<div class="notification is-warning is-light py-2 is-size-7 mb-3">Couldn’t load the last published artifact' + (b.reason ? ' (' + esc(b.reason) + ')' : '') + '. No diff shown — publishing will ship the current state.</div>'
  else head += '<p class="has-text-grey is-size-7 mb-3">Last published: ' + esc(b.tag || '') + (b.publishedAt ? ' · ' + esc(fmtDate(b.publishedAt)) : '') + '</p>'

  // Change table — grouped by change type, then by owner. Omitted entirely when
  // there's nothing to publish (the plan line already says so).
  const table = changed === 0 ? '' : renderExportPreviewTable(json.changes || [], esc, fmt)

  // Disclaimer as a muted footnote — honest, not loud.
  const footnote = json.disclaimer ? '<p class="has-text-grey-light is-size-7 mt-4">' + esc(json.disclaimer) + '</p>' : ''

  body.innerHTML = head + table + footnote
}

// renderExportPreviewTable renders one section per change type (Modified /
// Added / Removed). Empty sections are never rendered, and each section's row
// count equals its summary count — the API returns ONE entry per changed
// entity, already carrying its owner, so there is no tree to walk and no
// containment logic here. Any client integrating with orbital renders this the
// same way; if the UI ever needs bespoke logic, that belongs in the API.
//
// The `<namespace>:` prefix is stripped from ids (a preview is scoped to one
// data center, so it's constant); the full id stays in the cell's title.
function renderExportPreviewTable(changes, esc, fmt) {
  const rows = changes
  const short = (id) => String(id || '').replace(/^[^:]+:/, '')
  const sections = [
    { key: 'modified', label: 'Modified', cls: 'is-warning' },
    { key: 'added', label: 'Added', cls: 'is-success' },
    { key: 'removed', label: 'Removed', cls: 'is-danger' },
  ]
  let out = ''
  for (const sec of sections) {
    const inSec = rows.filter(r => r.change === sec.key)
    if (!inSec.length) continue

    let trs = ''
    for (const r of inSec) {
      trs += '<tr>'
        + '<td class="is-family-monospace is-size-7" title="' + esc(r.orbId) + '">' + esc(short(r.orbId)) + '</td>'
        + '<td class="has-text-grey is-size-7">' + esc(r.type) + '</td>'
        + '<td class="is-size-7">' + renderExportPreviewFields(r, esc, fmt) + '</td>'
        + '</tr>'
    }

    out += '<p class="mt-4 mb-1"><span class="tag is-small ' + sec.cls + '">' + sec.label + '</span> '
      + '<span class="has-text-grey is-size-7">' + inSec.length + '</span></p>'
      + '<div style="overflow-x:auto"><table class="table is-fullwidth is-narrow is-hoverable mb-0">'
      + '<thead><tr>'
      + '<th class="is-size-7">orbId</th>'
      + '<th class="is-size-7">Type</th>'
      + '<th class="is-size-7">Change</th>'
      + '</tr></thead>'
      + '<tbody>' + trs + '</tbody></table></div>'
  }
  return out
}

// Modified rows show the actual before → after per field (the signal). Added and
// removed rows show only a field count — every field of a new/gone entity is
// trivially new/gone, so dumping them all is noise. The `Type.` prefix is
// stripped from field names because the Type column already carries it.
function renderExportPreviewFields(node, esc, fmt) {
  const fields = node.fields || []
  if (node.change !== 'modified') {
    return '<span class="has-text-grey">' + fields.length + (fields.length === 1 ? ' field' : ' fields') + '</span>'
  }
  return fields.map(f =>
    '<div><span class="has-text-grey">' + esc(String(f.field).replace(/^[^.]+\./, '')) + '</span>: '
    + fmt(f.before) + ' <span class="has-text-grey-light">→</span> ' + fmt(f.after) + '</div>'
  ).join('')
}

function pollExportStatus(jobId, download) {
  clearTimeout(exportPollTimer)
  fetch(BASE + `/api/v1/export/jobs/${jobId}`)
    .then(r => r.json())
    .then(job => {
      loadExportJobsTable()
      renderExportPhases(job, download)
      if (job.status === 'completed') {
        const successMsg = download
          ? 'Export complete. Available in Retained Downloads.'
          : 'Export + publish complete. See Publish History.'
        setExportSummary(successMsg, 'is-success')
        showExportBox('is-success')
      } else if (job.status === 'failed') {
        setExportSummary(`Export failed: ${job.error ?? 'unknown error'}`, 'is-danger')
        showExportBox('is-danger')
      } else {
        exportPollTimer = setTimeout(() => pollExportStatus(jobId, download), 2000)
      }
    })
    .catch(() => { exportPollTimer = setTimeout(() => pollExportStatus(jobId, download), 3000) })
}

// Phase list = ordered steps the atomic export+publish goroutine transitions
// through. In download mode, only the first (Exporting subgraph) applies —
// the `.js-oci-only` items are hidden so the operator doesn't see steps that
// will never run.
const EXPORT_PHASES = ['exporting', 'bundling', 'pushing', 'signing']
const OCI_ONLY_PHASES = new Set(['bundling', 'pushing', 'signing'])

function startExportPhases(download) {
  document.querySelectorAll('.js-oci-only').forEach(el => {
    el.style.display = download ? 'none' : ''
  })
  // Reset every step to pending grey, spinner on the first step so there's
  // an initial signal before the first poll response arrives.
  document.querySelectorAll('#export-phase-list .js-phase').forEach((li, idx) => {
    setPhaseIcon(li, idx === 0 ? 'current' : 'upcoming')
  })
  document.getElementById('export-status-summary').style.display = 'none'
  showExportBox('is-info')
}

function renderExportPhases(job, download) {
  const activePhases = download ? EXPORT_PHASES.filter(p => !OCI_ONLY_PHASES.has(p)) : EXPORT_PHASES
  const phase = job.phase || 'exporting'
  const failed = job.status === 'failed'
  const completed = job.status === 'completed'

  const currentIdx = activePhases.indexOf(phase)
  document.querySelectorAll('#export-phase-list .js-phase').forEach(li => {
    const p = li.dataset.phase
    if (download && OCI_ONLY_PHASES.has(p)) return // stays hidden by startExportPhases
    const idx = activePhases.indexOf(p)
    if (completed) return setPhaseIcon(li, 'done')
    if (idx < 0) return setPhaseIcon(li, 'upcoming')  // unknown phase: leave grey
    if (idx < currentIdx) return setPhaseIcon(li, 'done')
    if (idx === currentIdx) return setPhaseIcon(li, failed ? 'failed' : 'current')
    setPhaseIcon(li, 'upcoming')
  })
}

// setPhaseIcon updates a phase list item's leading icon. State drives both
// the icon shape (check, spinner, xmark, circle) and its color class.
function setPhaseIcon(li, state) {
  const icon = li.querySelector('.icon')
  if (!icon) return
  const map = {
    done:     ['fa-solid fa-check',        'has-text-success'],
    current:  ['fa-solid fa-spinner fa-spin', 'has-text-info'],
    failed:   ['fa-solid fa-xmark',        'has-text-danger'],
    upcoming: ['fa-regular fa-circle',     'has-text-grey-light'],
  }
  const [iconClass, colorClass] = map[state] || map.upcoming
  icon.innerHTML = `<i class="${iconClass} ${colorClass}"></i>`
}

function showExportBox(colorClass) {
  const article = document.getElementById('export-status-article')
  if (article) article.className = `message ${colorClass}`
  document.getElementById('export-status-box').style.display = ''
}

function setExportSummary(text, colorClass) {
  const el = document.getElementById('export-status-summary')
  if (!el) return
  el.textContent = text
  el.className = `mt-2 has-text-weight-semibold ${colorClass ? 'has-text-' + colorClass.replace('is-', '') : ''}`
  el.style.display = ''
}

function loadExportJobsTable() {
  const tbody = document.getElementById('export-jobs-tbody')
  if (!tbody) return
  const table = document.getElementById('export-jobs-table')
  const ociConfigured = table && table.dataset.ociConfigured === 'true'
  fetch(BASE + '/api/v1/export/jobs?ociConfigured=' + ociConfigured, { headers: { 'HX-Request': 'true' } })
    .then(r => r.text())
    .then(html => { tbody.innerHTML = html; htmx.process(tbody) })
    .catch(() => {})
}

function downloadExportJob(btn, jobId) {
  window.location.href = BASE + '/api/v1/export/jobs/' + jobId + '/download'
  const orig = btn.innerHTML
  btn.disabled = true
  btn.innerHTML = '<span class="icon"><i class="fa-solid fa-spinner fa-spin"></i></span><span>Downloading…</span>'
  setTimeout(() => { btn.disabled = false; btn.innerHTML = orig }, 2000)
}

// deleteExportArtifact removes the retained zip for a download-flow job.
// The ExportJob row stays as audit history — only the on-disk artifact and
// the artifact_path pointer are cleared.
function deleteExportArtifact(jobId) {
  if (!confirm('Delete this retained zip?\n\nThe audit record for this export stays intact — only the on-disk file is removed.')) return
  fetch(BASE + `/api/v1/export/jobs/${jobId}/artifact`, { method: 'DELETE' })
    .then(r => {
      if (r.ok) loadExportJobsTable()
      else r.json().then(j => alert(`Delete failed: ${j.error ?? 'unknown'}`))
    })
    .catch(() => alert('Failed to delete artifact.'))
}

// Delegated handlers for Export page (page-level + tbody buttons that re-render via HTMX swap).
document.addEventListener('click', (e) => {
  const submit = e.target.closest('.js-export-submit')
  if (submit) { handleExportSubmit(submit); return }
  const dl = e.target.closest('.js-export-download')
  if (dl) { downloadExportJob(dl, dl.dataset.exportJobId); return }
  const del = e.target.closest('.js-export-artifact-delete')
  if (del) { deleteExportArtifact(del.dataset.exportJobId); return }
  if (e.target.closest('.js-retained-reload')) { loadExportJobsTable(); return }
})

// ─── Edge Delivery page ───────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('artifacts-tbody')) return
  loadArtifactsTable()
})

// artifactsSkeletonHTML returns a 9-column skeleton row block matching the
// publish-history table's column count so the swap doesn't reflow when the
// real content lands.
function artifactsSkeletonHTML(rows = 5) {
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  const cells = Array(9).fill(0).map(() => `<td>${s()}</td>`).join('')
  return Array(rows).fill(0).map(() => `<tr>${cells}</tr>`).join('')
}

function loadArtifactsTable(showSpinner = false) {
  const tbody = document.getElementById('artifacts-tbody')
  if (!tbody) return
  const btn = document.querySelector('.js-artifacts-reload')
  if (showSpinner) {
    if (btn) btn.classList.add('is-loading')
    tbody.innerHTML = artifactsSkeletonHTML()
  }
  // Canonical refresh pattern (docs/reference/UI.md): skeleton in place
  // immediately, then BOTH fetch and min-delay resolve before the swap so a
  // fast response doesn't flash the skeleton. Spinner clears at the same point.
  const fetchP = fetch(BASE + '/api/v1/oci/artifacts', { headers: { 'HX-Request': 'true' } }).then(r => r.text())
  const work = showSpinner
    ? Promise.all([fetchP, new Promise(resolve => setTimeout(resolve, 500))]).then(([html]) => html)
    : fetchP
  work
    .then(html => { tbody.innerHTML = html; htmx.process(tbody) })
    .catch(() => {})
    .finally(() => { if (showSpinner && btn) btn.classList.remove('is-loading') })
}

function testOCIConnection() {
  const btn = document.getElementById('btn-test-connection')
  const result = document.getElementById('test-connection-result')
  btn.classList.add('is-loading')
  result.textContent = ''
  fetch(BASE + '/api/v1/oci/test-connection', { method: 'POST' })
    .then(r => r.json())
    .then(res => {
      btn.classList.remove('is-loading')
      if (res.ok) {
        result.innerHTML = '<span class="has-text-success"><i class="fa-solid fa-circle-check"></i> Connected</span>'
      } else {
        result.innerHTML = `<span class="has-text-danger"><i class="fa-solid fa-circle-xmark"></i> ${res.error ?? 'Failed'}</span>`
      }
    })
    .catch(() => {
      btn.classList.remove('is-loading')
      result.innerHTML = '<span class="has-text-danger">Request failed</span>'
    })
}

let _cachedPublicKey = null

function _showPublicKey(key) {
  const display = document.getElementById('pubkey-display')
  const copyBtn = document.getElementById('btn-copy-pubkey')
  const dlBtn = document.getElementById('btn-download-pubkey')
  const verifyBlock = document.getElementById('pubkey-verify-cmd')
  const verifyText = document.getElementById('pubkey-verify-cmd-text')
  const showBtn = document.getElementById('btn-show-pubkey')

  display.textContent = key
  display.style.display = ''
  copyBtn.style.display = ''
  dlBtn.style.display = ''
  showBtn.querySelector('span:last-child').textContent = 'Hide'

  if (verifyText) {
    verifyText.textContent = `cosign verify --key cosign.pub <repository>:<tag>`
    verifyBlock.style.display = ''
  }
}

function togglePublicKey() {
  const btn = document.getElementById('btn-show-pubkey')
  const display = document.getElementById('pubkey-display')
  const copyBtn = document.getElementById('btn-copy-pubkey')
  const dlBtn = document.getElementById('btn-download-pubkey')
  const verifyBlock = document.getElementById('pubkey-verify-cmd')

  if (display.style.display !== 'none') {
    display.style.display = 'none'
    copyBtn.style.display = 'none'
    dlBtn.style.display = 'none'
    verifyBlock.style.display = 'none'
    btn.querySelector('span:last-child').textContent = 'Show'
    return
  }

  if (_cachedPublicKey) { _showPublicKey(_cachedPublicKey); return }

  btn.classList.add('is-loading')
  fetch(BASE + '/api/v1/oci/public-key')
    .then(r => { if (!r.ok) throw new Error('Failed'); return r.text() })
    .then(key => { _cachedPublicKey = key; _showPublicKey(key) })
    .catch(() => {
      const display = document.getElementById('pubkey-display')
      display.textContent = 'Could not load public key.'
      display.style.display = ''
    })
    .finally(() => btn.classList.remove('is-loading'))
}

function copyPublicKey() {
  if (!_cachedPublicKey) return
  const btn = document.getElementById('btn-copy-pubkey')
  navigator.clipboard.writeText(_cachedPublicKey).then(() => {
    btn.querySelector('span:last-child').textContent = 'Copied!'
    setTimeout(() => { btn.querySelector('span:last-child').textContent = 'Copy' }, 1500)
  })
}

function downloadPublicKey() {
  if (!_cachedPublicKey) return
  const blob = new Blob([_cachedPublicKey], { type: 'application/x-pem-file' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'cosign.pub'
  a.click()
  URL.revokeObjectURL(url)
}

function copyVerifyCmd() {
  const text = document.getElementById('pubkey-verify-cmd-text')?.textContent
  if (!text) return
  const btn = text.parentElement?.querySelector('button')
  navigator.clipboard.writeText(text).then(() => {
    if (btn) {
      btn.innerHTML = '<span class="icon"><i class="fas fa-check"></i></span>'
      setTimeout(() => { btn.innerHTML = '<span class="icon"><i class="fas fa-copy"></i></span>' }, 1500)
    }
  })
}

// Delegated handlers for Edge Delivery / Publish History buttons.
document.addEventListener('click', (e) => {
  if (e.target.closest('.js-oci-test-connection')) { testOCIConnection(); return }
  if (e.target.closest('.js-pubkey-toggle')) { togglePublicKey(); return }
  if (e.target.closest('.js-pubkey-copy')) { copyPublicKey(); return }
  if (e.target.closest('.js-pubkey-download')) { downloadPublicKey(); return }
  if (e.target.closest('.js-pubkey-verify-cmd-copy')) { copyVerifyCmd(); return }
  if (e.target.closest('.js-artifacts-reload')) { loadArtifactsTable(true); return }
})

// ─── DC edit modal ────────────────────────────────────────────────────────────

const dcEditors = new Map()
window.dcEditors = dcEditors // exposed for e2e smoke tests to assert editor registration

document.addEventListener('click', function (e) {
  const editBtn = e.target.closest('[data-dc-edit-id]')
  if (editBtn) {
    const id = editBtn.dataset.dcEditId
    const modal = document.getElementById('edit-modal-dc-' + id)
    if (!modal) return

    if (!dcEditors.has(id)) {
      const dataEl = document.getElementById('dc-edit-data-' + id)
      const initialJSON = dataEl ? dataEl.textContent.trim() : '{}'
      const editorTarget = document.getElementById('dc-json-editor-' + id)
      const editor = new window.JSONEditor({
        target: editorTarget,
        props: { mode: 'text', mainMenuBar: false },
      })
      editor.set({ text: JSON.stringify(JSON.parse(initialJSON), null, 2) })
      dcEditors.set(id, editor)

      const errorEl = document.getElementById('dc-edit-error-' + id)
      const showError = (msg) => { errorEl.textContent = msg; errorEl.style.display = '' }
      const clearError = () => { errorEl.textContent = ''; errorEl.style.display = 'none' }

      // configitem-editor module handles snapshot/diff/dispatch. DC is the
      // simplest case (no owned children today) but routing through the
      // module keeps the pattern uniform across pages.
      const targetsEl = document.getElementById('dc-edit-targets-' + id)
      const targets = JSON.parse(targetsEl ? targetsEl.textContent.trim() : '[]')
      const onSubmit = window.initConfigItemEditor({
        modal,
        editor,
        initialState: JSON.parse(initialJSON),
        targets,
        reloadOrbId: modal.dataset.orbId,
        reloadFn: async (orbId) => {
          const target = document.getElementById('tab-content-' + safeDomId(orbId))
          if (target && window.htmx) {
            // htmx.ajax returns a Promise that resolves after the swap
            // completes. Awaiting it prevents a race where the next
            // interaction sees stale DOM elements before the fragment
            // has been swapped in (caught by e2e datacenter-edit spec).
            await window.htmx.ajax('GET', BASE + '/datacenters/' + encodeURIComponent(orbId), { target, swap: 'innerHTML' })
            dcEditors.delete(id)
          }
        },
        showError,
        clearError,
      })

      document.getElementById('dc-edit-submit-' + id).addEventListener('click', async () => {
        const btn = document.getElementById('dc-edit-submit-' + id)
        btn.classList.add('is-loading')
        btn.disabled = true
        try {
          const ok = await onSubmit()
          if (ok) {
            modal.classList.remove('is-active')
            document.documentElement.style.overflow = ''
          }
        } finally {
          btn.classList.remove('is-loading')
          btn.disabled = false
        }
      })
    }

    const errorEl = document.getElementById('dc-edit-error-' + id)
    if (errorEl) { errorEl.textContent = ''; errorEl.style.display = 'none' }
    modal.classList.add('is-active')
    document.documentElement.style.overflow = 'hidden'
    return
  }

  const closeBtn = e.target.closest('[data-dc-modal-close]')
  if (closeBtn) {
    const id = closeBtn.dataset.dcModalClose
    const modal = document.getElementById('edit-modal-dc-' + id)
    if (modal) {
      modal.classList.remove('is-active')
      document.documentElement.style.overflow = ''
    }
  }
})

// ─── Cluster edit modal ───────────────────────────────────────────────────────

const clusterEditors = new Map()
window.clusterEditors = clusterEditors

// reloadClusterFragment is the single entry point for re-rendering a cluster
// detail tab's content. Every caller that wants the cluster fragment refreshed
// (Reload button, modal-save success, backup save, backup delete) routes
// through here so the post-swap setup stays consistent:
//   1. fetch HX fragment for the cluster orbId
//   2. swap into tab-content-cluster-<domId>
//   3. htmx.process so any hx-* attrs on new DOM rebind
//   4. renderTimestamps for any timestamp spans
//   5. initDetailTabs to rebind sub-tab click handlers
//   6. clusterEditors.delete(domId) — the JSONEditor instance was attached to
//      the now-removed DOM node; the next Edit click must rebuild fresh.
// Forgetting any of (3-6) silently breaks an interaction (audit tab clicks dead,
// timestamps frozen, Edit modal opens empty). Centralizing makes the contract
// uniform across callers.
export function reloadClusterFragment(orbId) {
  const domId = safeDomId(orbId)
  const target = document.getElementById('tab-content-cluster-' + domId)
  if (!target) return Promise.resolve()
  // Mirror the server-tab reload contract: paint a skeleton, then hold the
  // spinner for a minimum render time so the reload doesn't feel like a flash.
  showClusterSkeleton(orbId)
  return fetchWithMinDelay('/clusters/' + encodeURIComponent(orbId))
    .then(html => {
      target.innerHTML = html
      htmx.process(target)
      renderTimestamps(target)
      const clusterDetailTabs = target.querySelector('[id^="cluster-detail-tabs-"]')
      if (clusterDetailTabs) initDetailTabs(clusterDetailTabs)
      clusterEditors.delete(domId)
    })
    .catch(() => {
      target.innerHTML = '<div class="notification is-danger is-light is-size-7 m-4"><strong>Reload failed.</strong> Check your connection and try again.</div>'
    })
}
window.reloadClusterFragment = reloadClusterFragment

document.addEventListener('click', function (e) {
  const editBtn = e.target.closest('[data-cluster-edit-id]')
  if (editBtn) {
    const id = editBtn.dataset.clusterEditId
    const modal = document.getElementById('edit-modal-cluster-' + id)
    if (!modal) return

    if (!clusterEditors.has(id)) {
      // Initialize the JSON editor once per modal open. The configitem-editor.js
      // module handles snapshot/diff/dispatch — this shim just builds the
      // editor and wires the Save button to the module's submit handler.
      const dataEl = document.getElementById('cluster-edit-data-' + id)
      const targetsEl = document.getElementById('cluster-edit-targets-' + id)
      const initialState = JSON.parse(dataEl ? dataEl.textContent.trim() : '{}')
      const targets = JSON.parse(targetsEl ? targetsEl.textContent.trim() : '[]')
      const editorTarget = document.getElementById('cluster-json-editor-' + id)
      const editor = new window.JSONEditor({
        target: editorTarget,
        props: { mode: 'text', mainMenuBar: false },
      })
      editor.set({ text: JSON.stringify(initialState, null, 2) })
      clusterEditors.set(id, editor)

      const errorEl = document.getElementById('cluster-edit-error-' + id)
      const showError = (msg) => { errorEl.textContent = msg; errorEl.style.display = '' }
      const clearError = () => { errorEl.textContent = ''; errorEl.style.display = 'none' }

      // The configitem-editor module owns snapshot/diff/dispatch. It returns
      // a submit handler closed over (modal, editor, targets) — the page
      // wires it to the Save button. Server/DC edit will migrate next.
      const onSubmit = window.initConfigItemEditor({
        modal,
        editor,
        initialState,
        targets,
        reloadOrbId: modal.dataset.orbId,
        reloadFn: reloadClusterFragment,
        showError,
        clearError,
      })

      document.getElementById('cluster-edit-submit-' + id).addEventListener('click', async () => {
        const btn = document.getElementById('cluster-edit-submit-' + id)
        btn.classList.add('is-loading')
        btn.disabled = true
        try {
          const ok = await onSubmit()
          if (ok) {
            modal.classList.remove('is-active')
            document.documentElement.style.overflow = ''
            clusterEditors.delete(id)
          }
        } finally {
          btn.classList.remove('is-loading')
          btn.disabled = false
        }
      })
    }

    const errorEl = document.getElementById('cluster-edit-error-' + id)
    if (errorEl) { errorEl.textContent = ''; errorEl.style.display = 'none' }
    modal.classList.add('is-active')
    document.documentElement.style.overflow = 'hidden'
    return
  }

  const closeBtn = e.target.closest('[data-cluster-modal-close]')
  if (closeBtn) {
    const id = closeBtn.dataset.clusterModalClose
    const modal = document.getElementById('edit-modal-cluster-' + id)
    if (modal) {
      modal.classList.remove('is-active')
      document.documentElement.style.overflow = ''
    }
  }
})


// ─── HTMX afterSwap — orbital editor cleanup ──────────────────────────────────
// shared.js handles tab init and timestamps; this listener cleans up editor state.

document.addEventListener('htmx:afterSwap', (evt) => {
  const target = evt.detail && evt.detail.target
  if (!target) return
  const dcDetailTabs = target.querySelector('[id^="dc-detail-tabs-"]')
  if (dcDetailTabs) dcEditors.delete(dcDetailTabs.id.replace('dc-detail-tabs-', ''))
  const srvDetailTabs = target.querySelector('[id^="srv-detail-tabs-"]')
  if (srvDetailTabs) srvEditors.delete(srvDetailTabs.id.replace('srv-detail-tabs-', ''))
})

// ─── Server edit modal ────────────────────────────────────────────────────────

const srvEditors = new Map()
window.srvEditors = srvEditors // exposed for e2e smoke tests

document.addEventListener('click', function (e) {
  const editBtn = e.target.closest('[data-srv-edit-id]')
  if (editBtn) {
    const id = editBtn.dataset.srvEditId
    const modal = document.getElementById('edit-modal-srv-' + id)
    if (!modal) return

    if (!srvEditors.has(id)) {
      const dataEl = document.getElementById('srv-edit-data-' + id)
      const initialJSON = dataEl ? dataEl.textContent.trim() : '{}'
      const editor = new window.JSONEditor({
        target: document.getElementById('srv-json-editor-' + id),
        props: { mode: 'text', mainMenuBar: false },
      })
      const parsedInitial = JSON.parse(initialJSON)
      editor.set({ text: JSON.stringify(parsedInitial, null, 2) })
      const { idracSettings: _initialIdrac, ...initialServer } = parsedInitial
      modal.dataset.idracSnapshot = JSON.stringify(_initialIdrac ?? {})
      modal.dataset.serverSnapshot = JSON.stringify(initialServer)
      srvEditors.set(id, editor)

      const errorEl = document.getElementById('srv-edit-error-' + id)
      const showError = (msg) => { errorEl.textContent = msg; errorEl.style.display = '' }
      const clearError = () => { errorEl.textContent = ''; errorEl.style.display = 'none' }

      // Build the targets list once, hand off to configitem-editor module.
      // initConfigItemEditor returns a submit handler closure that does
      // snapshot/diff/parallel-dispatch — same path as cluster edit.
      const targetsEl = document.getElementById('srv-edit-targets-' + id)
      const targets = JSON.parse(targetsEl ? targetsEl.textContent.trim() : '[]')
      const onSubmit = window.initConfigItemEditor({
        modal,
        editor,
        initialState: parsedInitial,
        targets,
        reloadOrbId: modal.dataset.orbId,
        reloadFn: (orbId) => {
          // Server reload-target is page-specific (dc context vs standalone).
          // Reuse modal.dataset.reloadUrl + .reloadTarget set by the template.
          const target = document.getElementById(modal.dataset.reloadTarget)
          if (target && window.htmx) {
            return new Promise((resolve) => {
              window.htmx.ajax('GET', BASE + modal.dataset.reloadUrl, { target, swap: 'innerHTML' })
              srvEditors.delete(id)
              // htmx.ajax returns a promise in some versions; resolve immediately for the rest.
              setTimeout(resolve, 0)
            })
          }
        },
        showError,
        clearError,
      })

      document.getElementById('srv-edit-submit-' + id).addEventListener('click', async () => {
        const btn = document.getElementById('srv-edit-submit-' + id)
        btn.classList.add('is-loading')
        btn.disabled = true
        try {
          const ok = await onSubmit()
          if (ok) {
            modal.classList.remove('is-active')
            document.documentElement.style.overflow = ''
          }
        } finally {
          btn.classList.remove('is-loading')
          btn.disabled = false
        }
      })
    }

    const errorEl = document.getElementById('srv-edit-error-' + id)
    if (errorEl) { errorEl.textContent = ''; errorEl.style.display = 'none' }
    modal.classList.add('is-active')
    document.documentElement.style.overflow = 'hidden'
    return
  }

  const closeBtn = e.target.closest('[data-srv-modal-close]')
  if (closeBtn) {
    const id = closeBtn.dataset.srvModalClose
    const modal = document.getElementById('edit-modal-srv-' + id)
    if (modal) {
      modal.classList.remove('is-active')
      document.documentElement.style.overflow = ''
    }
  }
})

// ─── Audit log page ───────────────────────────────────────────────────────────

const skipVars = new Set(['updatedBy', 'updatedAt', 'id'])

function formatGQL(query) {
  const flat = query.replace(/\s+/g, ' ').trim()
  let indent = 0
  let out = ''
  let i = 0
  while (i < flat.length) {
    const ch = flat[i]
    if (ch === '{') {
      out += ' {\n' + '  '.repeat(++indent)
    } else if (ch === '}') {
      out = out.trimEnd()
      out += '\n' + '  '.repeat(--indent) + '}'
    } else if (ch === ',' && flat[i + 1] === ' ') {
      out += ',\n' + '  '.repeat(indent)
      i++
    } else {
      out += ch
    }
    i++
  }
  return out.trim()
}

function renderPayload(details) {
  if (!details) return null
  let d
  try { d = typeof details === 'string' ? JSON.parse(details) : details } catch (_) { return null }

  if (d.query) {
    const vars = Object.fromEntries(
      Object.entries(d.variables || {}).filter(([k]) => !skipVars.has(k))
    )
    const opName = `<p style="font-size:0.7rem;margin:0 0 0.4rem"><span style="font-weight:600">Operation:</span> ${d.operationName || '—'}</p>`
    const varsBlock = `<p style="font-size:0.7rem;font-weight:600;margin:0 0 0.25rem">Input</p>
      <pre style="font-size:0.72rem;background:var(--bulma-background);padding:0.75rem;white-space:pre-wrap;margin:0 0 0.75rem">${JSON.stringify(vars, null, 2)}</pre>`
    const queryBlock = `<p style="font-size:0.7rem;font-weight:600;margin:0 0 0.25rem">Query</p>
      <pre style="font-size:0.72rem;background:var(--bulma-background);padding:0.75rem;white-space:pre-wrap;word-break:break-word;margin:0;max-height:400px;overflow-y:auto">${formatGQL(d.query)}</pre>`
    return `<div style="padding:0.5rem 1rem 0.75rem">${opName}${varsBlock}${queryBlock}</div>`
  }

  if (Object.keys(d).length === 0) return null
  return `<div style="padding:0.5rem 1rem 0.75rem">
    <pre style="font-size:0.72rem;background:var(--bulma-background);padding:0.75rem;white-space:pre-wrap;word-break:break-word;margin:0;max-height:400px;overflow-y:auto">${JSON.stringify(d, null, 2)}</pre>
  </div>`
}

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('audit-log-table')) return

  const auditTable = new DataTable('#audit-log-table', {
    layout: {
      topStart: [
        { pageLength: { menu: [25, 50, 100, 200] } },
        { buttons: [
          { extend: 'excel', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-excel"></i><span>Excel</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Excel' },
          { extend: 'csv', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-text"></i><span>CSV</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'CSV' },
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-audit' } },
        ] },
      ],
      topEnd: { search: { placeholder: 'Search events…' } },
    },
    order: [[1, 'desc']],
    pageLength: 25,
    autoWidth: true,
    scrollX: true,
    stateSave: true,
    language: {
      infoEmpty: 'No events recorded yet',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'events', 1: 'event' },
    },
    initComplete: function () { dtWrapLengthSelect(this.api()) },
    columns: [
      { data: null, orderable: false, className: 'dt-control', defaultContent: '', width: '1%' },
      { data: 'timestamp' },
      { data: 'actor' },
      { data: 'operations', render: (v) => (v && v.length) ? v.map(op => `<span class="tag is-info is-light is-small">${op}</span>`).join(' ') : '<span class="tag is-light is-small">unknown</span>' },
      { data: 'resourceTypes', render: (v) => (v && v.length) ? v.join(', ') : '—' },
      { data: 'resourceIds', render: (v) => (v && v.length) ? v.join(', ') : '—' },
      { data: 'details', visible: false },
    ],
    ajax: {
      url: BASE + '/api/v1/audit-log?limit=200',
      dataSrc: (json) => json.events ?? [],
    },
    createdRow: function (row, data) {
      row.dataset.details = typeof data.details === 'string' ? data.details : JSON.stringify(data.details)
      if (!renderPayload(data.details)) {
        row.querySelector('td.dt-control')?.classList.remove('dt-control')
      }
    },
  })

  $('#audit-log-table tbody').on('click', 'td.dt-control', function () {
    const tr = this.closest('tr')
    const row = auditTable.row(tr)
    const payload = renderPayload(row.data()?.details)
    if (!payload) return
    if (row.child.isShown()) {
      row.child.hide()
      tr.classList.remove('shown')
    } else {
      row.child(payload).show()
      tr.classList.add('shown')
    }
  })

  const reloadBtn = auditTable.button('reload:name').node()
  reloadBtn.on('click', function () {
    reloadBtn.addClass('is-loading')
    const onError = () => reloadBtn.removeClass('is-loading')
    auditTable.one('error.dt', onError)
    auditTable.ajax.reload(() => {
      auditTable.off('error.dt', onError)
      reloadBtn.removeClass('is-loading')
    }, false)
  })
})

// ─── Restore ──────────────────────────────────────────────────────────────────

function loadRestoreJobs() {
  const tbody = document.getElementById('restore-tbody')
  if (!tbody) return
  fetch(BASE + '/api/v1/restore/jobs', { headers: { 'HX-Request': 'true' } })
    .then(r => r.text())
    .then(html => { tbody.innerHTML = html })
    .catch(() => {
      if (tbody) tbody.innerHTML = '<tr><td colspan="6" class="has-text-grey has-text-centered">Failed to load.</td></tr>'
    })
}

function onRestoreSelectChange(sel) {
  const warning = document.getElementById('restore-schema-warning')
  const btn = document.getElementById('btn-restore')
  if (!warning) return
  const sv = sel.options[sel.selectedIndex]?.dataset?.schemaVersion
  const currentSV = document.getElementById('restore-backup-select')?.dataset?.currentSchema
  if (sv && currentSV && sv !== currentSV) {
    warning.textContent = `Schema version mismatch: selected backup is ${sv}, current schema is ${currentSV}. You will be prompted to confirm before the restore proceeds.`
    warning.style.display = ''
  } else {
    warning.style.display = 'none'
  }
  if (btn) { btn.textContent = 'Restore Now'; delete btn.dataset.confirmMismatch }
}

function triggerRestore(confirm) {
  const btn = document.getElementById('btn-restore')
  const msg = document.getElementById('restore-status-msg')
  const sel = document.getElementById('restore-backup-select')
  if (!sel || !sel.value) {
    msg.textContent = 'Select a backup first.'
    msg.style.display = ''
    return
  }
  btn.classList.add('is-loading')
  btn.disabled = true
  msg.style.display = 'none'

  const body = { backupId: sel.value }
  if (confirm) body.confirmSchemaMismatch = true

  fetch(BASE + '/api/v1/restore', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then(r => r.json())
    .then(data => {
      btn.classList.remove('is-loading')
      if (data.error) {
        msg.textContent = data.error
        msg.style.display = ''
        if (data.requiresConfirmation) {
          btn.textContent = 'Confirm Restore'
          btn.dataset.confirmMismatch = 'true'
          btn.disabled = false
        } else {
          btn.textContent = 'Restore Now'
          delete btn.dataset.confirmMismatch
          btn.disabled = false
        }
      } else {
        btn.textContent = 'Restore Now'
        delete btn.dataset.confirmMismatch
        loadRestoreJobs()
        pollRestore(data.id)
      }
    })
    .catch(() => {
      msg.textContent = 'Request failed.'
      msg.style.display = ''
      btn.classList.remove('is-loading')
      btn.disabled = false
    })
}

function pollRestore(jobId) {
  const btn = document.getElementById('btn-restore')
  const interval = setInterval(() => {
    fetch(BASE + '/api/v1/restore/jobs/' + jobId)
      .then(r => r.json())
      .then(data => {
        loadRestoreJobs()
        if (data.status === 'completed' || data.status === 'failed') {
          clearInterval(interval)
          if (btn) { btn.classList.remove('is-loading'); btn.disabled = false }
        }
      })
      .catch(() => { clearInterval(interval); if (btn) { btn.classList.remove('is-loading'); btn.disabled = false } })
  }, 3000)
}

function openRestoreLogModal(btn) {
  const parts = []
  if (btn.dataset.log) parts.push(btn.dataset.log)
  if (btn.dataset.error) parts.push('Error: ' + btn.dataset.error)
  document.getElementById('restore-log-content').textContent = parts.join('\n') || '(no output)'
  document.getElementById('restore-log-modal').classList.add('is-active')
}

function closeRestoreLogModal() {
  document.getElementById('restore-log-modal').classList.remove('is-active')
}

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('restore-tbody')) return
  loadRestoreJobs()
})

// Delegated handlers for Restore page. Log buttons re-render via HTMX swap so
// the listener must be on document, not the button.
document.addEventListener('click', (e) => {
  const trig = e.target.closest('.js-restore-trigger')
  if (trig) {
    triggerRestore(trig.dataset.confirmMismatch === 'true')
    return
  }
  const log = e.target.closest('.js-restore-log-open')
  if (log) { openRestoreLogModal(log); return }
  if (e.target.closest('.js-restore-log-close')) { closeRestoreLogModal(); return }
})
document.addEventListener('change', (e) => {
  const sel = e.target.closest('.js-restore-select')
  if (sel) onRestoreSelectChange(sel)
})

// ─── Users page ───────────────────────────────────────────────────────────────

function setUserRole(userId, role, btn) {
  btn.classList.add('is-loading')
  btn.disabled = true

  fetch(BASE + `/api/v1/users/${userId}/role`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ role }),
  })
    .then(r => {
      if (!r.ok) return r.json().then(j => Promise.reject(j.message || 'Request failed'))
      window.location.reload()
    })
    .catch(msg => {
      btn.classList.remove('is-loading')
      btn.disabled = false
      const errEl = document.getElementById('users-error')
      if (errEl) {
        errEl.textContent = typeof msg === 'string' ? msg : 'Failed to update role.'
        errEl.style.display = ''
        setTimeout(() => { errEl.style.display = 'none' }, 5000)
      }
    })
}

document.addEventListener('click', (e) => {
  const btn = e.target.closest('.js-user-role-set')
  if (!btn) return
  setUserRole(btn.dataset.userId, btn.dataset.role, btn)
})

// ─── Divergence reports page ──────────────────────────────────────────────────
//
// Expansion is per-session, in-DOM only — no sessionStorage persistence.
// Page loads (and refreshes) always start collapsed; the operator must click
// to expand. Avoids the surprise of a newly-arrived report being auto-expanded
// because the same DC was expanded in a prior session.

function setGroupExpanded(dcId, expanded) {
  const sel = `[data-dc="${CSS.escape(dcId)}"]`
  const parent = document.querySelector('tr.divergence-group-row' + sel)
  const detail = document.querySelector('tr.divergence-group-detail' + sel)
  if (!parent || !detail) return
  detail.style.display = expanded ? '' : 'none'
  const chevron = parent.querySelector('.divergence-chevron')
  if (chevron) {
    chevron.classList.toggle('fa-chevron-down', expanded)
    chevron.classList.toggle('fa-chevron-right', !expanded)
  }
}

function toggleDivergenceGroup(dcId) {
  const sel = `[data-dc="${CSS.escape(dcId)}"]`
  const detail = document.querySelector('tr.divergence-group-detail' + sel)
  if (!detail) return
  setGroupExpanded(dcId, detail.style.display === 'none')
}

// toggleDivergenceAction selects/deselects an action button within a per-row
// .divergence-action-group. Radio-style: clicking a different button switches
// the selection; clicking the already-selected button unselects it (= no
// decision staged on that row).
function toggleDivergenceAction(btn) {
  const group = btn.closest('.divergence-action-group')
  if (!group) return
  const wasSelected = btn.classList.contains('is-selected')
  // Reset every button in this group to the unselected (outlined) state.
  for (const b of group.querySelectorAll('.divergence-action-btn')) {
    b.classList.remove('is-selected')
    b.classList.add('is-outlined')
  }
  // Toggle the clicked one if it wasn't already selected.
  if (!wasSelected) {
    btn.classList.remove('is-outlined')
    btn.classList.add('is-selected')
  }
  updateDivergenceBatchCounter()
}

// collectDivergenceSelections returns [{id, action}, ...] for every row whose
// action group has a button in the .is-selected state. Rows where the operator
// hasn't clicked any action button (or has explicitly unstaged) are excluded
// — those are silently skipped by the batch endpoint server-side too.
function collectDivergenceSelections() {
  const groups = document.querySelectorAll('.divergence-action-group')
  const out = []
  for (const g of groups) {
    const selected = g.querySelector('.divergence-action-btn.is-selected')
    if (selected) {
      out.push({ id: g.dataset.entryId, action: selected.dataset.action })
    }
  }
  return out
}

// Submit is gated on the ALL-OR-NOTHING rule: every visible pending row must
// have a staged decision. Resolved rows don't render the action group, so the
// "pending" count is exactly the number of .divergence-action-group elements
// on the page. This keeps the rule precise (resolved rows don't count against
// the gate) and self-correcting (after a partial submit the page reloads;
// remaining rows reset the gate).
function updateDivergenceBatchCounter() {
  const counter = document.getElementById('divergence-batch-counter')
  const submit = document.getElementById('divergence-batch-submit')
  if (!counter || !submit) return

  const total = document.querySelectorAll('.divergence-action-group').length
  const staged = collectDivergenceSelections().length

  if (total === 0) {
    submit.disabled = true
    counter.textContent = 'No pending decisions.'
    return
  }
  if (staged === total) {
    submit.disabled = false
    counter.textContent = `${staged} of ${total} decisions made — ready to submit.`
    return
  }
  submit.disabled = true
  const remaining = total - staged
  counter.textContent =
    `${staged} of ${total} decisions made — ${remaining} row${remaining === 1 ? '' : 's'} still need${remaining === 1 ? 's' : ''} a decision.`
}

// submitDivergenceBatch is the Submit-button handler. Instead of POSTing
// immediately, it opens the confirmation modal pre-populated with the staged
// decisions. The actual POST happens when the modal's Confirm button fires
// confirmDivergenceBatch().
function submitDivergenceBatch() {
  const resolutions = collectDivergenceSelections()
  if (resolutions.length === 0) return
  populateDivergenceConfirmModal(resolutions)
  const modal = document.getElementById('divergence-confirm-modal')
  if (modal) modal.classList.add('is-active')
}

function closeDivergenceConfirmModal() {
  const modal = document.getElementById('divergence-confirm-modal')
  if (modal) modal.classList.remove('is-active')
  const btn = document.getElementById('divergence-confirm-btn')
  if (btn) { btn.classList.remove('is-loading'); btn.disabled = false }
}

// populateDivergenceConfirmModal fills the modal body with:
//   - summary tags (count per action with appropriate colors)
//   - a Force warning when any forces are staged
//   - a per-row list (collapsed via <details> when >10 rows)
function populateDivergenceConfirmModal(resolutions) {
  const counts = { accept: 0, reject: 0, ignore: 0 }
  for (const r of resolutions) counts[r.action] = (counts[r.action] || 0) + 1

  const tags = []
  if (counts.accept) tags.push(`<span class="tag is-success is-light">${counts.accept} Accept</span>`)
  if (counts.reject) tags.push(`<span class="tag is-danger is-light">${counts.reject} Reject</span>`)
  if (counts.ignore) tags.push(`<span class="tag is-warning is-light">${counts.ignore} Ignore</span>`)

  const warning = counts.reject > 0
    ? `<div class="notification is-danger is-light is-size-7 mt-3 mb-0" style="padding:0.75em 1em;">
         <strong>Reject</strong> keeps orbital's current intent and signals the edge to overwrite the local override on the next publish. The edge admin's change will be lost.
       </div>`
    : ''
  const ignoreInfo = counts.ignore > 0
    ? `<div class="notification is-warning is-light is-size-7 mt-3 mb-0" style="padding:0.75em 1em;">
         <strong>Ignore</strong> leaves orbital intent unchanged and allows the edge admin to retain ownership of this field. Divergence will continue to be reported until resolved differently.
       </div>`
    : ''

  const rowsHTML = resolutions.map(r => {
    const group = document.querySelector(`.divergence-action-group[data-entry-id="${r.id}"]`)
    const orbId = group?.dataset.entryOrbid || r.id
    const field = group?.dataset.entryField || '?'
    const colorClass = r.action === 'accept' ? 'has-text-success'
                      : r.action === 'reject' ? 'has-text-danger'
                      : 'has-text-warning-dark'
    return `<li class="is-size-7" style="margin-bottom:0.15em;">
              <code>${orbId}</code> / <code>${field}</code>
              &nbsp;→&nbsp; <strong class="${colorClass}">${r.action}</strong>
            </li>`
  }).join('')

  const listHTML = resolutions.length <= 10
    ? `<ul style="margin-left:1.25em; margin-top:0.75em;">${rowsHTML}</ul>`
    : `<details style="margin-top:0.75em;">
         <summary class="is-size-7 has-text-grey" style="cursor:pointer;">Show all ${resolutions.length} decisions</summary>
         <ul style="margin-left:1.25em; margin-top:0.5em;">${rowsHTML}</ul>
       </details>`

  const summary = document.getElementById('divergence-confirm-summary')
  summary.innerHTML = `
    <p class="mb-3">Submitting ${resolutions.length} decision${resolutions.length === 1 ? '' : 's'}:</p>
    <div style="display:flex;gap:0.4rem;flex-wrap:wrap;">${tags.join('')}</div>
    ${warning}
    ${ignoreInfo}
    ${listHTML}
  `
}

// confirmDivergenceBatch is the Confirm-button handler.
// Fires N parallel PUT /api/v1/divergences/:id/resolution calls — one per
// staged decision. The server's REST endpoint is single-row; batching is a
// client-side concern. Per-row errors surface in the same #divergence-error
// notification slot; full success reloads the page.
function confirmDivergenceBatch() {
  const confirmBtn = document.getElementById('divergence-confirm-btn')
  const submit = document.getElementById('divergence-batch-submit')
  const errEl = document.getElementById('divergence-error')
  const resolutions = collectDivergenceSelections()
  if (resolutions.length === 0) {
    closeDivergenceConfirmModal()
    return
  }

  confirmBtn.classList.add('is-loading')
  confirmBtn.disabled = true

  Promise.allSettled(resolutions.map(r =>
    fetch(BASE + `/api/v1/divergences/${encodeURIComponent(r.id)}/resolution`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action: r.action }),
    }).then(async resp => {
      if (resp.ok) return { id: r.id, ok: true }
      const body = await resp.json().catch(() => ({}))
      const errMessage = body.message || body.error || `HTTP ${resp.status}`
      throw Object.assign(new Error(errMessage), { id: r.id, status: resp.status })
    })
  )).then(outcomes => {
    const failed = outcomes
      .filter(o => o.status === 'rejected')
      .map(o => ({ id: o.reason.id || '?', error: o.reason.message || 'unknown error' }))
    const okCount = outcomes.length - failed.length
    if (failed.length === 0) {
      window.location.reload()
      return
    }
    // Mark each failed row's Decision cell inline so the row itself shows the
    // failure — the banner is a summary, the row is the source of truth.
    failed.forEach(f => {
      const row = document.querySelector(`tr[data-entry-id="${f.id}"]`)
      if (!row) return
      const decisionCell = row.querySelector('td:last-child')
      if (decisionCell) {
        decisionCell.innerHTML = `<span class="has-text-danger has-text-weight-medium" title="${f.error.replace(/"/g, '&quot;')}">failed</span>`
      }
    })
    const msg = okCount > 0
      ? `${okCount} applied, ${failed.length} failed:\n${failed.map(f => `• ${f.id}: ${f.error}`).join('\n')}`
      : `All ${failed.length} decisions failed:\n${failed.map(f => `• ${f.id}: ${f.error}`).join('\n')}`
    closeDivergenceConfirmModal()
    if (errEl) {
      errEl.style.whiteSpace = 'pre-line'
      errEl.textContent = msg
      errEl.style.display = ''
      // Persistent — no auto-hide. User dismisses via reload/refresh or new submit.
    }
    if (submit) submit.classList.remove('is-loading')
    updateDivergenceBatchCounter()
  })
}

document.addEventListener('DOMContentLoaded', updateDivergenceBatchCounter)

// refreshDivergenceReports fetches the page's HX fragment and swaps the
// table contents in place. Same Promise.all pattern as loadOrbTags: skeleton
// rows show immediately, and the swap waits for BOTH the fetch and the 500ms
// min-delay so a fast response doesn't flash the skeleton.
function refreshDivergenceReports() {
  const btn = document.getElementById('btn-refresh-divergence')
  const container = document.getElementById('divergence-content')
  if (!container) return
  if (btn) btn.classList.add('is-loading')
  container.innerHTML = divergenceSkeletonHTML()
  Promise.all([
    fetch(BASE + '/divergence-reports', { headers: { 'HX-Request': 'true' } }).then(r => r.text()),
    new Promise(resolve => setTimeout(resolve, 500)),
  ])
    .then(([html]) => {
      container.innerHTML = html
      htmx.process(container)
      updateDivergenceBatchCounter()
    })
    .catch(() => {})
    .finally(() => { if (btn) btn.classList.remove('is-loading') })
}

// divergenceSkeletonHTML mirrors the real table's column widths so the
// transition from skeleton → real content doesn't reflow. Same is-skeleton
// span pattern as loadOrbTags and shared.js helpers.
function divergenceSkeletonHTML() {
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  const row = () => `<tr>
    <td>${s()}</td><td>${s()}</td><td>${s()}</td><td>${s()}</td><td>${s()}</td><td>${s()}</td>
  </tr>`
  return `<table class="table is-striped is-fullwidth is-size-7" style="table-layout: fixed;">
    <colgroup>
      <col style="width: 2.5rem">
      <col style="width: 20%">
      <col style="width: 18%">
      <col style="width: 10%">
      <col style="width: 18%">
      <col>
    </colgroup>
    <thead>
      <tr>
        <th></th>
        <th>Data Center</th>
        <th>Last Seen</th>
        <th>Entries</th>
        <th>Status</th>
        <th>Actions</th>
      </tr>
    </thead>
    <tbody>${[1, 2, 3].map(row).join('')}</tbody>
  </table>`
}

// One-click atomic export-and-publish from a Divergence Reports row. The
// unified `POST /api/v1/export` (download=false) does the whole flow in a
// single async job — no separate publish call, no confirmation modal. Poll
// until terminal; on success, flip the button to a "Go to Publish History"
// link so the operator can inspect the published artifact.
const divergencePublishedDCs = new Set()

function divergencePublishForDC(button) {
  const dcOrbId = button.dataset.dcOrbid
  if (!dcOrbId || divergencePublishedDCs.has(dcOrbId)) return

  const origHTML = button.innerHTML
  const spinner = (text) => `<span class="icon"><i class="fa-solid fa-spinner fa-spin"></i></span><span>${text}</span>`
  button.disabled = true
  button.innerHTML = spinner('Publishing…')
  hideDivergenceErr()

  // Atomic call — server exports subgraph then pushes to OCI in one goroutine.
  fetch(BASE + '/api/v1/export', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ orbId: dcOrbId, download: false }),
  })
    .then(r => r.json())
    .then(json => {
      if (json.error) throw new Error(json.error)
      // Row is committed to the atomic flow — subsequent clicks stay silent.
      divergencePublishedDCs.add(dcOrbId)
      pollDivergencePublish(json.id, button)
    })
    .catch(err => {
      button.disabled = false
      button.innerHTML = origHTML
      showDivergenceErr('Publish failed: ' + (err.message || 'unknown error'))
    })
}

function pollDivergencePublish(jobId, button) {
  const setToHistoryLink = () => {
    button.disabled = false
    button.onclick = () => { window.location.href = BASE + '/publish-history' }
    button.innerHTML = '<span class="icon"><i class="fa-solid fa-arrow-up-right-from-square"></i></span><span>Go to Publish History</span>'
  }
  const tick = () => {
    fetch(BASE + `/api/v1/export/jobs/${jobId}`)
      .then(r => r.json())
      .then(job => {
        if (job.status === 'completed') {
          setToHistoryLink()
        } else if (job.status === 'failed') {
          button.disabled = false
          button.innerHTML = '<span class="icon has-text-danger"><i class="fa-solid fa-triangle-exclamation"></i></span><span>Publish failed</span>'
          showDivergenceErr('Publish failed: ' + (job.error || 'unknown error'))
        } else {
          setTimeout(tick, 2000)
        }
      })
      .catch(() => setTimeout(tick, 3000))
  }
  tick()
}

function showDivergenceErr(msg) {
  const el = document.getElementById('divergence-error')
  if (!el) return
  el.textContent = msg
  el.style.display = ''
}
function hideDivergenceErr() {
  const el = document.getElementById('divergence-error')
  if (!el) return
  el.style.display = 'none'
  el.textContent = ''
}

// Break-glass: delete orbital's local divergence-report state for a DC.
// Used when state is stuck (stale resolutions from an earlier session,
// partial supersede edge case, etc.). Backend wipes the DB rows in one
// transaction AND resets the ingester's idempotency tracker so the next
// poll re-processes the latest S3 report fresh.
function divergenceDeleteReportForDC(button) {
  const dcOrbId = button.dataset.dcOrbid
  const entryCount = button.dataset.entryCount || '?'
  if (!dcOrbId) return
  if (!window.confirm(
    `Delete ${entryCount} divergence entries + decisions for ${dcOrbId}?\n\n` +
    `Break-glass only — orbital normally re-ingests automatically. ` +
    `orb's report and edge state are unchanged.`
  )) {
    return
  }
  const origHTML = button.innerHTML
  button.disabled = true
  button.innerHTML = '<span class="icon"><i class="fa-solid fa-spinner fa-spin"></i></span><span>Deleting…</span>'

  fetch(BASE + '/api/v1/divergences?dcOrbId=' + encodeURIComponent(dcOrbId), {
    method: 'DELETE',
  })
    .then(r => r.json().then(body => ({ ok: r.ok, status: r.status, body })))
    .then(({ ok, status, body }) => {
      if (!ok) throw new Error(body.message || `HTTP ${status}`)
      // Hard-reload — page server-renders divergence state, so we need a fresh fetch.
      window.location.reload()
    })
    .catch(err => {
      button.disabled = false
      button.innerHTML = origHTML
      window.alert('Delete failed: ' + err.message)
    })
}

// Delegated handlers for Divergence Reports page. All buttons live inside
// #divergence-content which is innerHTML-swapped on refresh, so document-level
// listeners are the only ones that survive.
document.addEventListener('click', (e) => {
  if (e.target.closest('.js-divergence-refresh')) { refreshDivergenceReports(); return }
  if (e.target.closest('.js-divergence-confirm-close')) { closeDivergenceConfirmModal(); return }
  if (e.target.closest('.js-divergence-confirm-submit')) { confirmDivergenceBatch(); return }
  const group = e.target.closest('.js-divergence-group-toggle')
  // Whole-row toggle, but skip if the click landed on an interactive child
  // so button/link clicks inside the row reach their own delegated handlers.
  if (group && !e.target.closest('button, a, input, select, textarea, label')) {
    toggleDivergenceGroup(group.dataset.dc); return
  }
  const pub = e.target.closest('.divergence-publish-btn')
  if (pub) { divergencePublishForDC(pub); return }
  const del = e.target.closest('.divergence-delete-btn')
  if (del) { divergenceDeleteReportForDC(del); return }
  const act = e.target.closest('.divergence-action-btn')
  if (act) { toggleDivergenceAction(act); return }

  // Publish-history: expand row shows audit events between this publish
  // and the previous one for the same DC. Detail row markup is emitted
  // server-side with hx-trigger="revealed once" — first expand fires the
  // HTMX fetch, subsequent toggles just flip visibility.
  const chg = e.target.closest('.js-publish-changes-expand')
  if (chg) {
    const row = chg.closest('tr')
    const next = row?.nextElementSibling
    if (next?.classList.contains('js-publish-changes-detail')) {
      next.classList.toggle('is-hidden')
      const ico = chg.querySelector('.icon i')
      ico?.classList.toggle('fa-chevron-right')
      ico?.classList.toggle('fa-chevron-down')
    }
    return
  }
})
document.addEventListener('submit', (e) => {
  if (e.target.closest('#divergence-batch-form')) {
    e.preventDefault()
    submitDivergenceBatch()
  }
})

// ─── Config-item delete modal (DataCenter / Server) ───────────────────────────

;(function () {
  let pendingDelete = null

  function openCfgDeleteModal(id, type, name, dcId) {
    pendingDelete = { id, type, dcId }
    const modal     = document.getElementById('cfg-delete-modal')
    const title     = document.getElementById('cfg-delete-modal-title')
    const loading   = document.getElementById('cfg-delete-modal-loading')
    const body      = document.getElementById('cfg-delete-modal-body')
    const errorEl   = document.getElementById('cfg-delete-modal-error')
    const confirmBtn = document.getElementById('cfg-delete-confirm-btn')

    if (!modal) return
    title.textContent = 'Delete ' + name
    loading.style.display = ''
    body.style.display = 'none'
    errorEl.style.display = 'none'
    errorEl.textContent = ''
    confirmBtn.disabled = true
    confirmBtn.classList.remove('is-loading')
    modal.classList.add('is-active')

    fetch(BASE + '/config-items/delete-preview?id=' + encodeURIComponent(id) + '&type=' + type)
      .then(r => r.ok ? r.text() : r.text().then(t => Promise.reject(t)))
      .then(html => {
        loading.style.display = 'none'
        body.innerHTML = html
        body.style.display = ''
        confirmBtn.disabled = false
      })
      .catch(err => {
        loading.style.display = 'none'
        errorEl.textContent = typeof err === 'string' ? err : 'Failed to load impact summary.'
        errorEl.style.display = ''
      })
  }

  function closeCfgDeleteModal() {
    const modal = document.getElementById('cfg-delete-modal')
    if (modal) modal.classList.remove('is-active')
    pendingDelete = null
  }

  document.addEventListener('click', function (e) {
    const btn = e.target.closest('[data-cfg-delete-id]')
    if (!btn) return
    openCfgDeleteModal(
      btn.dataset.cfgDeleteId,
      btn.dataset.cfgDeleteType,
      btn.dataset.cfgDeleteName || btn.dataset.cfgDeleteType,
      btn.dataset.cfgDeleteDcId || ''
    )
  })

  document.addEventListener('click', function (e) {
    if (e.target.closest('.js-cfg-delete-close')) closeCfgDeleteModal()
  })

  document.addEventListener('click', async function (e) {
    if (!e.target.closest('#cfg-delete-confirm-btn')) return
    if (!pendingDelete) return
    const { id, type, dcId } = pendingDelete
    const btn = document.getElementById('cfg-delete-confirm-btn')
    btn.classList.add('is-loading')
    btn.disabled = true

    try {
      const r = await fetch(BASE + '/api/v1/config-items/' + encodeURIComponent(type) + '/' + encodeURIComponent(id), { method: 'DELETE' })
      if (!r.ok) {
        const t = await r.text()
        throw new Error(t || 'Delete failed')
      }
      closeCfgDeleteModal()
      if (type === 'DataCenter') {
        const dcDomId = safeDomId(id)
        const tabClose = document.getElementById('tab-close-' + dcDomId)
        if (tabClose) tabClose.click()
        const reloadBtn = document.getElementById('btn-reload-datacenters')
        if (reloadBtn) reloadBtn.click()
      } else if (type === 'KubernetesCluster') {
        const clusterDomId = safeDomId(id)
        const tabClose = document.getElementById('tab-close-cluster-' + clusterDomId)
        if (tabClose) tabClose.click()
        const reloadBtn = document.getElementById('btn-reload-clusters')
        if (reloadBtn) reloadBtn.click()
      } else if (type === 'Server' && dcId) {
        const dcDomId = safeDomId(dcId)
        const dcTarget = document.getElementById('tab-content-' + dcDomId)
        if (dcTarget) {
          htmx.ajax('GET', BASE + '/datacenters/' + encodeURIComponent(dcId), { target: dcTarget, swap: 'innerHTML' })
        }
      } else {
        const srvDomId = safeDomId(id)
        const tabClose = document.getElementById('tab-close-srv-' + srvDomId)
        if (tabClose) tabClose.click()
        const reloadBtn = document.getElementById('btn-reload-servers')
        if (reloadBtn) reloadBtn.click()
      }
    } catch (err) {
      btn.classList.remove('is-loading')
      btn.disabled = false
      const errorEl = document.getElementById('cfg-delete-modal-error')
      errorEl.textContent = err.message || 'Delete failed.'
      errorEl.style.display = ''
    }
  })
})()

// ─── Report Issue modal ──────────────────────────────────────────────────────

function openReportIssueModal() {
  document.getElementById('report-issue-modal').classList.add('is-active')
}
function closeReportIssueModal() {
  document.getElementById('report-issue-modal').classList.remove('is-active')
}
document.addEventListener('click', (e) => {
  if (e.target.closest('.js-report-issue-open')) { openReportIssueModal(); return }
  if (e.target.closest('.js-report-issue-close')) { closeReportIssueModal(); return }
})
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') closeReportIssueModal()
})

// ─── Module exports to window (e2e + cross-module only) ─────────────────────
// All click handlers use class-based delegation (see docs/reference/UI.md);
// the entries below are NOT a bridge for inline onclick. They exist for two
// reasons only:
//   1. e2e tests assert on JSONEditor instance maps (dcEditors / clusterEditors
//      / srvEditors) — exposed so Playwright can read them.
//   2. Cross-module callables (reloadClusterFragment) — invoked from contexts
//      that can't import the module symbol directly.
// Do NOT add new entries here to support a new onclick — use delegation.


// ─── Publish History: Compare tab ─────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('compare-result')) return
  initComparePage()
})

// initComparePage populates the pickers from the artifact list, honours any
// ?from=&to= already in the URL, and runs the diff.
function initComparePage() {
  const dcSel = document.getElementById('compare-dc-select')
  const fromSel = document.getElementById('compare-from-select')
  const toSel = document.getElementById('compare-to-select')
  const runBtn = document.getElementById('btn-compare-run')
  const out = document.getElementById('compare-result')
  if (!dcSel || !fromSel || !toSel || !runBtn) return

  const params = new URLSearchParams(window.location.search)
  const wantFrom = params.get('from')
  const wantTo = params.get('to')

  // Versions are fetched per data center via ?dc=, never by pulling the whole
  // artifact list and grouping here. The unfiltered list is capped, so one
  // busy data center would push the others off it entirely — and grouping
  // client-side is the "UI compensating for the API" pattern orbital rejects.
  const loadVersions = (dcOrbId, selectFrom, selectTo) => {
    fromSel.disabled = toSel.disabled = runBtn.disabled = true
    return fetch(`${BASE}/api/v1/oci/artifacts?dc=${encodeURIComponent(dcOrbId)}&status=completed&limit=500`)
      .then(r => r.json())
      .then(rows => {
        // Only artifacts with a digest can be pulled by digest.
        const usable = (rows || []).filter(a => a.digest)
        // Oldest-first so the From/To dropdowns read chronologically.
        usable.sort((x, y) => String(x.completedAt || '').localeCompare(String(y.completedAt || '')))

        if (usable.length < 2) {
          fromSel.innerHTML = toSel.innerHTML = '<option value="">—</option>'
          out.innerHTML = `<div class="notification is-light">${usable.length === 0
            ? 'Nothing published for this data center yet.'
            : 'Only one published version — nothing to compare against yet.'}</div>`
          return
        }

        const opts = usable.map(a => `<option value="${a.id}">${esc(a.tag)}</option>`).join('')
        fromSel.innerHTML = opts
        toSel.innerHTML = opts
        fromSel.disabled = toSel.disabled = runBtn.disabled = false
        // Default to the two most recent — the common "what changed last?" case.
        fromSel.value = String(selectFrom || usable[usable.length - 2].id)
        toSel.value = String(selectTo || usable[usable.length - 1].id)
      })
  }

  let mostRecentDC = null

  const populateDCs = (activeOrbId) => {
    // Data centers come from the Topology API, the authoritative source —
    // not inferred from whichever artifacts happened to fit in a capped list.
    return fetch(BASE + '/graphql', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query: '{ queryDataCenter { orbId name } }' }),
    })
      .then(r => r.json())
      .then(res => {
        const dcs = (res.data && res.data.queryDataCenter) || []
        if (!dcs.length) {
          dcSel.innerHTML = '<option value="">No data centers</option>'
          return null
        }
        dcs.sort((a, b) => String(a.name || a.orbId).localeCompare(String(b.name || b.orbId)))
        // Falling back to dcs[0] lands on the alphabetically-first data center,
        // which usually has nothing published — so a fresh visit opens on an
        // empty state. Prefer whichever DC published most recently.
        const active = activeOrbId || mostRecentDC || dcs[0].orbId
        dcSel.innerHTML = dcs.map(d =>
          `<option value="${esc(d.orbId)}"${d.orbId === active ? ' selected' : ''}>${esc(d.name || d.orbId)}</option>`).join('')
        return active
      })
  }

  dcSel.addEventListener('change', () => { out.innerHTML = ''; loadVersions(dcSel.value) })

  runBtn.addEventListener('click', () => {
    if (!fromSel.value || !toSel.value) return
    // Keep the URL in sync so the current view is always linkable.
    window.history.replaceState({}, '', `${BASE}/publish-history/compare?from=${fromSel.value}&to=${toSel.value}`)
    rememberCompare(dcSel.value, fromSel.value, toSel.value)
    runCompare(fromSel.value, toSel.value)
  })

  // A deep link names artifacts, not a data center — resolve one to find which
  // DC to select, then load that DC's versions and run the diff.
  if (wantFrom && wantTo) {
    fetch(`${BASE}/api/v1/oci/artifacts/${encodeURIComponent(wantFrom)}`)
      .then(r => r.ok ? r.json() : null)
      .then(a => populateDCs(a && a.datacenterId))
      .then(active => active && loadVersions(dcSel.value, wantFrom, wantTo))
      .then(() => { rememberCompare(dcSel.value, wantFrom, wantTo); runCompare(wantFrom, wantTo) })
      .catch(() => { out.innerHTML = '<div class="notification is-warning is-light">Couldn\u2019t load that comparison.</div>' })
    return
  }

  // No query params: restore the last comparison from this session rather than
  // resetting. The tabs are routes, so Artifacts → Compare is a full page load —
  // without this, every trip back silently discards the user's selection.
  const last = recallCompare()
  if (last) {
    populateDCs(last.dc)
      .then(active => active && loadVersions(dcSel.value, last.from, last.to))
      .then(() => {
        // Only auto-run if the remembered pair still exists — artifacts can be
        // deleted between visits.
        if (fromSel.value === String(last.from) && toSel.value === String(last.to)) {
          window.history.replaceState({}, '', `${BASE}/publish-history/compare?from=${last.from}&to=${last.to}`)
          runCompare(last.from, last.to)
        }
      })
      .catch(() => populateDCs().then(active => active && loadVersions(dcSel.value)))
    return
  }

  // Cold start: ask which data center published most recently, then open on it.
  fetch(BASE + '/api/v1/oci/artifacts?status=completed&limit=1')
    .then(r => r.ok ? r.json() : [])
    .then(rows => { if (rows && rows.length) mostRecentDC = rows[0].datacenterId })
    .catch(() => {})
    .then(() => populateDCs())
    .then(active => active && loadVersions(dcSel.value))
}

// Compare selection is remembered per browser session (not localStorage) — it is
// transient working state, not a preference worth surviving a restart.
const COMPARE_STATE_KEY = 'orbital.compare.last'

function rememberCompare(dc, from, to) {
  try { sessionStorage.setItem(COMPARE_STATE_KEY, JSON.stringify({ dc, from, to })) } catch (e) { /* private mode / quota */ }
}

function recallCompare() {
  try {
    const raw = sessionStorage.getItem(COMPARE_STATE_KEY)
    if (!raw) return null
    const v = JSON.parse(raw)
    return (v && v.dc && v.from && v.to) ? v : null
  } catch (e) { return null }
}


function runCompare(from, to) {
  const out = document.getElementById('compare-result')
  if (!out) return
  out.innerHTML = '<div class="has-text-centered p-5"><span class="icon is-large has-text-grey"><i class="fa-solid fa-spinner fa-spin fa-2x"></i></span><p class="mt-2 has-text-grey">Comparing…</p></div>'

  fetch(`${BASE}/api/v1/export/compare?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`)
    .then(r => r.json().then(json => ({ status: r.status, json })))
    .then(({ status, json }) => {
      if (status !== 200) {
        out.innerHTML = `<div class="notification is-warning is-light">${esc(json.error || 'Comparison failed.')}</div>`
        return
      }
      renderCompare(out, json)
    })
    .catch(() => {
      out.innerHTML = '<div class="notification is-danger is-light">Comparison request failed.</div>'
    })
}

function esc(x) {
  return String(x == null ? '' : x).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]))
}

// renderCompare reuses renderExportPreviewTable verbatim — the compare endpoint
// returns the identical flat `changes[]` shape as the export preview, because
// both are produced by the same graphdiff core. Only the header differs.
function renderCompare(out, json) {
  const fmt = (v) => v == null ? '<span class="has-text-grey-light">∅</span>' : '<span class="is-family-monospace">' + esc(JSON.stringify(v)) + '</span>'
  const s = json.summary || {}
  const changed = (s.added || 0) + (s.removed || 0) + (s.modified || 0)
  // Header carries DATA only — counts, versions, dates — matching how GitHub
  // ("Showing N changed files…") and terraform ("Plan: 2 to add…") head a diff.
  // Neither explains what the view isn't; standing prose in a header reads as
  // documentation and gets skipped after the first visit.
  //
  // Emphasis comes from COLOUR, never from mixing bold and regular weight on one
  // line — and the colours are the ones the Modified/Added/Removed sections below
  // already use, so header and table speak one language.
  let head = '<p class="is-size-5 mb-1">' + esc(json.from.tag)
    + ' <span class="has-text-grey-light">→</span> ' + esc(json.to.tag) + '</p>'
    + '<p class="has-text-grey is-size-7 mb-4">Published '
    + esc(formatPublishRange(json.from.publishedAt, json.to.publishedAt)) + '</p>'

  // Zero counts stay grey — a green "0 added" reads as a result when it is an
  // absence. Only counts that actually happened earn their colour.
  const stat = (n, label, cls) =>
    '<div style="min-width:5.5rem">'
    + '<p class="is-size-4 ' + (n > 0 ? cls : 'has-text-grey-light') + '">' + n.toLocaleString() + '</p>'
    + '<p class="is-size-7 has-text-grey">' + label + '</p></div>'

  head += '<div class="is-flex mb-5" style="gap:2.5rem; flex-wrap:wrap;">'
    + stat(s.modified || 0, 'Changed', 'has-text-warning-dark')
    + stat(s.added || 0, 'Added', 'has-text-success')
    + stat(s.removed || 0, 'Removed', 'has-text-danger')
    + stat(s.unchanged || 0, 'Unchanged', 'has-text-grey')
    + '</div>'

  // The one place the audit-log-vs-content-diff distinction actually misleads:
  // zero differences right after the Audit Log showed edits reads as "broken".
  // Explain it here and nowhere else — contextual help at the moment of doubt.
  if (changed === 0) {
    head += '<p class="has-text-grey-light is-size-7 mb-3">No differences between these versions. '
      + 'Edits that were later undone don\u2019t appear here — see the Audit Log for the full edit history.</p>'
  }

  const table = changed === 0 ? '' : renderExportPreviewTable(json.changes || [], esc, fmt)
  out.innerHTML = head + table
}

// formatPublishRange renders two publish timestamps without repeating what the
// pair already says. Publishes minutes apart previously rendered as the redundant
// "Aug 27, 2026 → Aug 27, 2026"; same-day now collapses to the times, which are
// the only distinguishing part. The year appears only when it differs.
function formatPublishRange(fromISO, toISO) {
  const a = fromISO ? new Date(fromISO) : null
  const b = toISO ? new Date(toISO) : null
  if (!a || !b || isNaN(a.getTime()) || isNaN(b.getTime())) return ''

  const time = (d) => d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: false })
  const dayMonth = (d) => d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
  const full = (d) => d.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' })

  if (a.toDateString() === b.toDateString()) return `${dayMonth(a)}, ${time(a)} → ${time(b)}`
  if (a.getFullYear() !== b.getFullYear()) return `${full(a)} → ${full(b)}`
  return `${dayMonth(a)} → ${dayMonth(b)}`
}
