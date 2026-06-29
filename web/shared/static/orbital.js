// orbital.js — orbital-specific page logic

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
  initDcDetailTabs,
  initServerDetailTabs,
  initClusterDetailTabs,
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

// Test Connection buttons are declarative — see backups.gohtml and
// divergence-reports.gohtml. The button posts to /api/v1/backup/test-connection
// with HX-Request: true and the server returns an HTML fragment swapped into
// the result slot. No JS handler needed.

// ─── Export page ──────────────────────────────────────────────────────────────

let exportPollTimer = null

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('export-jobs-tbody')) return

  const select = document.getElementById('export-datacenter-select')
  const submitBtn = document.getElementById('export-submit-btn')
  if (select && submitBtn) {
    select.addEventListener('change', () => { submitBtn.disabled = !select.value })
  }

  document.body.addEventListener('refreshExportJobs', () => loadExportJobsTable())

  loadExportJobsTable()
})

function handleExportSubmit() {
  const select = document.getElementById('export-datacenter-select')
  const id = select.value
  if (!id) return

  const submitBtn = document.getElementById('export-submit-btn')
  submitBtn.classList.add('is-loading')
  submitBtn.disabled = true

  fetch(BASE + '/api/v1/export', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ orbId: id }),
  })
    .then(r => r.json())
    .then(json => {
      submitBtn.classList.remove('is-loading')
      submitBtn.disabled = false
      if (json.error) {
        showExportStatus('is-warning', 'fa-triangle-exclamation', json.error)
        return
      }
      showExportStatus('is-info', 'fa-spinner fa-spin', 'Export started…')
      pollExportStatus(json.id)
      loadExportJobsTable()
    })
    .catch(() => {
      submitBtn.classList.remove('is-loading')
      submitBtn.disabled = false
      showExportStatus('is-danger', 'fa-circle-xmark', 'Failed to start export.')
    })
}

function pollExportStatus(jobId) {
  clearTimeout(exportPollTimer)
  fetch(BASE + `/api/v1/export/jobs/${jobId}`)
    .then(r => r.json())
    .then(job => {
      loadExportJobsTable()
      if (job.status === 'completed') {
        showExportStatus('is-success', 'fa-circle-check', 'Export complete.')
      } else if (job.status === 'failed') {
        showExportStatus('is-danger', 'fa-circle-xmark', `Export failed: ${job.error ?? 'unknown error'}`)
      } else {
        exportPollTimer = setTimeout(() => pollExportStatus(jobId), 2000)
        showExportStatus('is-info', 'fa-spinner fa-spin', job.status === 'running' ? 'Exporting…' : 'Pending…')
      }
    })
    .catch(() => { exportPollTimer = setTimeout(() => pollExportStatus(jobId), 3000) })
}

function showExportStatus(colorClass, iconClass, text) {
  const article = document.getElementById('export-status-article')
  const icon = document.getElementById('export-status-icon')
  const textEl = document.getElementById('export-status-text')

  article.className = `message ${colorClass}`
  icon.innerHTML = `<i class="fa-solid ${iconClass}"></i>`
  textEl.textContent = text
  document.getElementById('export-status-box').style.display = ''
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

function deleteExportJob(jobId) {
  if (!confirm('Delete this export job and its local artifact file?\n\nThis does not remove any published OCI artifacts from the registry.')) return
  fetch(BASE + `/api/v1/export/jobs/${jobId}`, { method: 'DELETE' })
    .then(r => {
      if (r.ok) loadExportJobsTable()
      else r.json().then(j => alert(`Delete failed: ${j.error ?? 'unknown'}`))
    })
    .catch(() => alert('Failed to delete job.'))
}

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
  const btn = document.querySelector('button[onclick="loadArtifactsTable(true)"]')
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
        reloadFn: (orbId) => {
          const target = document.getElementById('tab-content-' + safeDomId(orbId))
          if (target && window.htmx) {
            window.htmx.ajax('GET', BASE + '/datacenters/' + encodeURIComponent(orbId), { target, swap: 'innerHTML' })
            dcEditors.delete(id)
          }
          return Promise.resolve()
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
//   5. initClusterDetailTabs to rebind sub-tab click handlers
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
      initClusterDetailTabs(domId)
      clusterEditors.delete(domId)
    })
    .catch(() => {})
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
      <pre style="font-size:0.72rem;background:#f5f5f5;padding:0.75rem;white-space:pre-wrap;margin:0 0 0.75rem">${JSON.stringify(vars, null, 2)}</pre>`
    const queryBlock = `<p style="font-size:0.7rem;font-weight:600;margin:0 0 0.25rem">Query</p>
      <pre style="font-size:0.72rem;background:#f5f5f5;padding:0.75rem;white-space:pre-wrap;word-break:break-word;margin:0;max-height:400px;overflow-y:auto">${formatGQL(d.query)}</pre>`
    return `<div style="padding:0.5rem 1rem 0.75rem">${opName}${varsBlock}${queryBlock}</div>`
  }

  if (Object.keys(d).length === 0) return null
  return `<div style="padding:0.5rem 1rem 0.75rem">
    <pre style="font-size:0.72rem;background:#f5f5f5;padding:0.75rem;white-space:pre-wrap;word-break:break-word;margin:0;max-height:400px;overflow-y:auto">${JSON.stringify(d, null, 2)}</pre>
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
    // Floor the spinner duration at 500ms — same contract as fetchWithMinDelay
    // for the server/DC/cluster reload buttons.
    const minDelay = new Promise(resolve => setTimeout(resolve, 500))
    const reload = new Promise(resolve => auditTable.ajax.reload(() => resolve(), false))
    Promise.all([minDelay, reload]).then(() => reloadBtn.removeClass('is-loading'))
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
  if (btn) { btn.textContent = 'Restore Now'; btn.setAttribute('onclick', 'triggerRestore()') }
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
          btn.setAttribute('onclick', 'triggerRestore(true)')
          btn.disabled = false
        } else {
          btn.textContent = 'Restore Now'
          btn.setAttribute('onclick', 'triggerRestore()')
          btn.disabled = false
        }
      } else {
        btn.textContent = 'Restore Now'
        btn.setAttribute('onclick', 'triggerRestore()')
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

// One-click export-and-publish from a Divergence Reports row. Triggers an
// export for the DC, polls until completed, then opens the existing
// publish-confirm modal (same fragment as /export). Operator confirms in
// the modal — the existing HTMX form does the actual publish. On success,
// the refreshExportJobs HX-Trigger fires and the row's button switches to
// the "Published" disabled state for the session. To republish, the
// operator goes to /export (which reflects current published state).
const divergencePublishedDCs = new Set()

function divergencePublishForDC(button) {
  const dcOrbId = button.dataset.dcOrbid
  if (!dcOrbId || divergencePublishedDCs.has(dcOrbId)) return

  const origHTML = button.innerHTML
  const spinner = (text) => `<span class="icon"><i class="fa-solid fa-spinner fa-spin"></i></span><span>${text}</span>`
  button.disabled = true
  button.innerHTML = spinner('Exporting…')
  hideDivergenceErr()

  // 1) Export.
  fetch(BASE + '/api/v1/export', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ orbId: dcOrbId }),
  })
    .then(r => r.json())
    .then(json => {
      if (json.error) throw new Error(json.error)
      // 2) Poll export job until terminal.
      const pollExport = () => {
        fetch(BASE + `/api/v1/export/jobs/${json.id}`)
          .then(r => r.json())
          .then(job => {
            if (job.status === 'completed') {
              button.innerHTML = spinner('Publishing…')
              // 3) Open publish-confirm modal with summary fragment.
              return openDivergencePublishModal(json.id, button, dcOrbId, origHTML)
            } else if (job.status === 'failed') {
              button.disabled = false
              button.innerHTML = origHTML
              showDivergenceErr('Export failed: ' + (job.error || 'unknown error'))
            } else {
              setTimeout(pollExport, 2000)
            }
          })
          .catch(() => setTimeout(pollExport, 3000))
      }
      pollExport()
    })
    .catch(err => {
      button.disabled = false
      button.innerHTML = origHTML
      showDivergenceErr('Export failed: ' + (err.message || 'unknown error'))
    })
}

function openDivergencePublishModal(jobId, button, dcOrbId, origHTML) {
  return fetch(BASE + `/api/v1/export/jobs/${jobId}/publish-modal`, { headers: { 'HX-Request': 'true' } })
    .then(r => r.text())
    .then(html => {
      const body = document.getElementById('publish-modal-body')
      body.innerHTML = html
      htmx.process(body)
      const modal = document.getElementById('publish-confirm-modal')
      modal.classList.add('is-active')

      // The export succeeded — this row's publish flow is COMMITTED for this
      // session, regardless of whether the operator confirms the modal or
      // cancels it. To re-export and re-publish, they must use /export.
      // Mark the DC now so the modal-close observer never re-enables.
      divergencePublishedDCs.add(dcOrbId)

      // Once any terminal state is reached (publish completed OK, failed, or
      // operator closed the modal without confirming), collapse to a uniform
      // "Go to Publish History" link. Same color (is-link, blue), same icon,
      // same destination — the per-state distinction lives in the publish
      // history page itself, not in this button.
      const setToHistoryLink = () => {
        button.disabled = false
        button.onclick = () => { window.location.href = BASE + '/publish-history' }
        button.innerHTML = '<span class="icon"><i class="fa-solid fa-arrow-up-right-from-square"></i></span><span>Go to Publish History</span>'
      }
      const onTerminal = () => {
        document.body.removeEventListener('refreshExportJobs', onTerminal)
        setToHistoryLink()
      }
      document.body.addEventListener('refreshExportJobs', onTerminal)

      // Modal closed before any publish attempt (operator hit Cancel after the
      // confirm modal appeared) — same treatment. Row stays locked for the
      // session per divergencePublishedDCs; operator's path forward is still
      // the publish-history page.
      const observer = new MutationObserver(() => {
        if (modal.classList.contains('is-active')) return
        observer.disconnect()
        document.body.removeEventListener('refreshExportJobs', onTerminal)
        setToHistoryLink()
      })
      observer.observe(modal, { attributes: true, attributeFilter: ['class'] })
    })
    .catch(err => {
      // Failure to even open the publish modal — re-enable so operator can
      // retry from the row (no export-job/artifact was committed past here).
      button.disabled = false
      button.innerHTML = origHTML
      showDivergenceErr('Open publish modal failed: ' + err.message)
    })
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

// ─── window bridge for onclick handlers ──────────────────────────────────────
// ES modules don't expose functions to global scope by default.

window.triggerBackup = triggerBackup
window.downloadBackup = downloadBackup
window.openDeleteModal = openDeleteModal
window.closeDeleteModal = closeDeleteModal
window.confirmDelete = confirmDelete
window.handleExportSubmit = handleExportSubmit
window.downloadExportJob = downloadExportJob
window.deleteExportJob = deleteExportJob
window.loadArtifactsTable = loadArtifactsTable
window.testOCIConnection = testOCIConnection
window.togglePublicKey = togglePublicKey
window.copyPublicKey = copyPublicKey
window.downloadPublicKey = downloadPublicKey
window.copyVerifyCmd = copyVerifyCmd
window.triggerRestore = triggerRestore
window.onRestoreSelectChange = onRestoreSelectChange
window.openRestoreLogModal = openRestoreLogModal
window.closeRestoreLogModal = closeRestoreLogModal
window.setUserRole = setUserRole
window.toggleDivergenceGroup = toggleDivergenceGroup
window.toggleDivergenceAction = toggleDivergenceAction
window.updateDivergenceBatchCounter = updateDivergenceBatchCounter
window.submitDivergenceBatch = submitDivergenceBatch
window.confirmDivergenceBatch = confirmDivergenceBatch
window.closeDivergenceConfirmModal = closeDivergenceConfirmModal
window.refreshDivergenceReports = refreshDivergenceReports
window.divergencePublishForDC = divergencePublishForDC

// ─── Divergence break-glass: delete orbital's local report state for a DC ────
//
// Used when the divergence-report state is stuck (stale resolutions from an
// earlier session, partial supersede edge case, etc.). Backend wipes the DB
// rows in one transaction AND resets the ingester's idempotency tracker so
// the next poll re-processes the latest S3 report fresh.
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
window.divergenceDeleteReportForDC = divergenceDeleteReportForDC
