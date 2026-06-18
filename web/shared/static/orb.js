// orb.js — orb-specific page logic

import {
  BASE,
  INVENTORY_CACHE_KEY,
  initInventoryTable,
  initDatacenterTable,
  initServerListTable,
  loadDataCenterTab,
  loadServerListTab,
  saveTab,
  saveServerTab,
  initDatacenterTabRestoration,
  initServerListTabRestoration,
} from './shared.js'

// ─── Stale-state cleanup ──────────────────────────────────────────────────────
//
// Orb's open DC/Server tabs are keyed by DGraph UIDs. Two events invalidate
// those UIDs: (1) orb restart with a re-seed, (2) `orb import` (drop_all +
// live load assigns new UIDs). Both call this helper so the restoration code
// on window.load sees empty storage and skips.
function clearStaleTabState() {
  localStorage.removeItem('datacenterTabs')
  localStorage.removeItem('serverTabs')
  localStorage.removeItem('dcTabCurrent')
  localStorage.removeItem('srvTabCurrent')
}

// Server-restart trigger — `window.ORBITAL_CONFIG.serverVersion` is a per-restart
// timestamp set by the shared base layout. Differs → orb restarted. Orb has no
// login/session, so there's no natural moment to wipe state; this is the
// orb-only analog to orbital's login-based ?fresh=1 path.
document.addEventListener('DOMContentLoaded', () => {
  const v = window.ORBITAL_CONFIG?.serverVersion
  if (!v) return
  const stored = localStorage.getItem('serverVersion')
  if (stored === v) return
  clearStaleTabState()
  localStorage.setItem('serverVersion', v)
})

// ─── Orb import ───────────────────────────────────────────────────────────────

let orbImportPollTimer = null

function handleOrbImport(tag) {
  orbShowImportStatus('is-info', 'fa-spinner fa-spin', `Importing ${tag}…`)
  fetch(BASE + '/api/v1/import', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tag }),
  })
    .then(r => r.json())
    .then(data => {
      if (data.error) { orbShowImportStatus('is-warning', 'fa-triangle-exclamation', data.error); return }
      pollOrbImport()
    })
    .catch(() => orbShowImportStatus('is-danger', 'fa-circle-xmark', 'Failed to start import.'))
}

function handleOrbImportLatest() {
  const firstRow = document.querySelector('#orb-tags-tbody tr[data-tag]')
  if (!firstRow) return
  handleOrbImport(firstRow.dataset.tag)
}

function pollOrbImport() {
  clearTimeout(orbImportPollTimer)
  fetch(BASE + '/api/v1/import/status')
    .then(r => r.json())
    .then(data => {
      if (data.status === 'done' || data.status === 'partial') {
        // Imported data replaces the local DGraph store. Cached inventory is
        // now stale (sessionStorage doesn't auto-refresh on page load); open
        // DC/Server tab IDs point at UIDs that no longer exist after drop_all.
        sessionStorage.removeItem(INVENTORY_CACHE_KEY)
        clearStaleTabState()
        if (data.status === 'partial') {
          orbShowImportStatus('is-warning', 'fa-circle-exclamation', `Imported ${data.currentVersion} with dispatch errors — see Import History.`)
        } else {
          orbShowImportStatus('is-success', 'fa-circle-check', `Imported ${data.currentVersion} successfully.`)
        }
      } else if (data.status === 'failed') {
        orbShowImportStatus('is-danger', 'fa-circle-xmark', `Import failed: ${data.lastError || 'unknown error'}`)
      } else {
        orbShowImportStatus('is-info', 'fa-spinner fa-spin', data.status === 'running' ? 'Importing…' : 'Pending…')
        orbImportPollTimer = setTimeout(pollOrbImport, 2000)
      }
    })
    .catch(() => { orbImportPollTimer = setTimeout(pollOrbImport, 3000) })
}

function orbShowImportStatus(colorClass, iconClass, text) {
  const box = document.getElementById('orb-import-status-box')
  const article = document.getElementById('orb-import-status-article')
  const icon = document.getElementById('orb-import-status-icon')
  const textEl = document.getElementById('orb-import-status-text')
  if (!box) return
  article.className = `message ${colorClass}`
  icon.innerHTML = `<i class="fa-solid ${iconClass}"></i>`
  textEl.textContent = text
  box.style.display = ''
}

function loadOrbTags() {
  const tbody = document.getElementById('orb-tags-tbody')
  const btn = document.getElementById('btn-refresh-tags')
  if (!tbody) return
  if (btn) btn.classList.add('is-loading')
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  tbody.innerHTML = [1, 2, 3].map(() =>
    `<tr><td style="width:8%">${s()}</td><td style="width:12%">${s()}</td><td style="width:60%">${s()}</td><td style="width:8%">${s()}</td><td style="width:10%">${s()}</td></tr>`
  ).join('')
  Promise.all([
    fetch(BASE + '/api/v1/import/tags', { headers: { 'HX-Request': 'true' } }).then(r => r.text()),
    new Promise(resolve => setTimeout(resolve, 500)),
  ])
    .then(([html]) => { tbody.innerHTML = html })
    .catch(() => {
      if (tbody) tbody.innerHTML = '<tr><td colspan="5" class="has-text-danger">Failed to load tags.</td></tr>'
    })
    .finally(() => { if (btn) btn.classList.remove('is-loading') })
}

async function handleOrbCourierUpload() {
  const fileInput = document.getElementById('orb-courier-file')
  if (!fileInput?.files[0]) return
  const fd = new FormData()
  fd.append('bundle', fileInput.files[0])
  orbShowImportStatus('is-info', 'fa-spinner fa-spin', 'Uploading bundle…')
  fetch(BASE + '/api/v1/import/artifact', { method: 'POST', body: fd })
    .then(r => r.json())
    .then(data => {
      if (data.error) { orbShowImportStatus('is-warning', 'fa-triangle-exclamation', data.error); return }
      orbShowImportStatus('is-info', 'fa-spinner fa-spin', `Importing ${data.tag}…`)
      pollOrbImport()
    })
    .catch(() => orbShowImportStatus('is-danger', 'fa-circle-xmark', 'Upload failed.'))
}

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('orb-tags-tbody')) return
  loadOrbTags()

  const fileInput = document.getElementById('orb-courier-file')
  if (fileInput) {
    fileInput.addEventListener('change', () => {
      const name = fileInput.files[0]?.name || 'No file selected'
      document.getElementById('orb-courier-filename').textContent = name
      document.getElementById('orb-courier-upload-btn').disabled = !fileInput.files[0]
    })
  }
})

// ─── Inventory / Data Centers / Servers list tables ──────────────────────────
//
// DataTable construction + tab loading are shared with orbital (see shared.js).
// Per docs/reference/ORB.md, orb's UI mirrors orbital's tab-swap interaction model.

document.addEventListener('DOMContentLoaded', () => { initInventoryTable() })

document.addEventListener('DOMContentLoaded', () => {
  initDatacenterTable({
    onRowOpen: (data) => {
      const displayName = data.name
      const id = data.id
      const tab = document.getElementById(`tab-${id}`)
      if (tab) {
        tab.click()
      } else {
        loadDataCenterTab(displayName, id)
        saveTab(displayName, id)
        document.getElementById(`tab-${id}`).click()
      }
    },
  })
})

document.addEventListener('DOMContentLoaded', () => {
  initServerListTable({
    onRowOpen: (data) => {
      const id = data.id
      const displayName = data.hostname !== '—' ? data.hostname : data.serviceTag
      const tab = document.getElementById(`tab-srv-${id}`)
      if (tab) {
        tab.click()
      } else {
        loadServerListTab(displayName, id)
        saveServerTab(displayName, id)
        document.getElementById(`tab-srv-${id}`).click()
      }
    },
  })
})

window.addEventListener('load', initDatacenterTabRestoration)
window.addEventListener('load', initServerListTabRestoration)

// ─── Orb divergence publish ───────────────────────────────────────────────────

function publishDivergence() {
  const btn = document.getElementById('publish-btn')
  if (btn) btn.classList.add('is-loading')

  fetch(BASE + '/api/v1/divergence/publish', { method: 'POST' })
    .then(r => r.json().then(body => ({ ok: r.ok, body })))
    .then(({ ok, body }) => {
      if (!ok) throw new Error(body.message || 'publish failed')
      showPublishStatus('Divergence report published.', 'is-success')
    })
    .catch(err => showPublishStatus(err.message, 'is-danger'))
    .finally(() => { if (btn) btn.classList.remove('is-loading') })
}

// showPublishStatus populates the inline #publish-status notification slot
// with a result message. Same pattern as orbital's #divergence-error — the
// notification stays visible until the user navigates away or triggers
// another publish, matching the project's "inline status, not toast" UX.
function showPublishStatus(msg, cls) {
  const el = document.getElementById('publish-status')
  if (!el) return
  el.className = 'notification is-size-7 is-light ' + cls
  el.textContent = msg
  el.style.display = ''
}

// refreshOrbDivergence fetches the page's HX fragment and swaps the table in
// place. Mirrors orbital's refreshDivergenceReports: skeleton rows render
// immediately, the swap waits on Promise.all so a fast response doesn't flash.
function refreshOrbDivergence() {
  const btn = document.getElementById('btn-refresh-divergence')
  const container = document.getElementById('divergence-content')
  if (!container) return
  if (btn) btn.classList.add('is-loading')
  container.innerHTML = orbDivergenceSkeletonHTML()
  Promise.all([
    fetch(BASE + '/divergence', { headers: { 'HX-Request': 'true' } }).then(r => r.text()),
    new Promise(resolve => setTimeout(resolve, 500)),
  ])
    .then(([html]) => {
      container.innerHTML = html
      if (typeof htmx !== 'undefined') htmx.process(container)
    })
    .catch(() => {})
    .finally(() => { if (btn) btn.classList.remove('is-loading') })
}

// orbDivergenceSkeletonHTML mirrors the real table's six columns so the
// transition from skeleton → real content doesn't reflow. Same is-skeleton
// span pattern as loadOrbTags (orb.js:107) and orbital's divergenceSkeletonHTML.
function orbDivergenceSkeletonHTML() {
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  const row = () => `<tr>
    <td>${s()}</td><td>${s()}</td><td>${s()}</td><td>${s()}</td><td>${s()}</td><td>${s()}</td>
  </tr>`
  return `<div class="table-container">
    <table class="table is-fullwidth is-hoverable is-size-7">
      <thead>
        <tr>
          <th>Orb ID</th><th>Field</th><th>Intended</th><th>Override</th><th>Who</th><th>When</th>
        </tr>
      </thead>
      <tbody>${[1, 2, 3].map(row).join('')}</tbody>
    </table>
  </div>`
}

// ─── window bridge for onclick handlers ──────────────────────────────────────

window.handleOrbImport = handleOrbImport
window.handleOrbImportLatest = handleOrbImportLatest
window.loadOrbTags = loadOrbTags
window.handleOrbCourierUpload = handleOrbCourierUpload
window.publishDivergence = publishDivergence
window.refreshOrbDivergence = refreshOrbDivergence
