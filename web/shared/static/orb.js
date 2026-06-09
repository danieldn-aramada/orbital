// orb.js — orb-specific page logic

import {
  BASE,
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

// ─── Server-restart tab cleanup ───────────────────────────────────────────────
//
// Orb has no login/session, so there's no natural moment to wipe stale tab
// state. The shared base layout exposes window.ORBITAL_CONFIG.serverVersion —
// a timestamp set once per orb startup. When it differs from what we last
// stored, orb has restarted and any tab IDs in localStorage may point at
// DGraph UIDs that no longer exist (re-import changes UIDs). Clear them so
// the restoration code on window.load sees empty storage and skips.
//
// Runs on DOMContentLoaded which fires before window.load — restoration runs
// after, sees empty storage. Orbital uses its login-based ?fresh=1 path
// instead; this handler is orb-only.
document.addEventListener('DOMContentLoaded', () => {
  const v = window.ORBITAL_CONFIG?.serverVersion
  if (!v) return
  const stored = localStorage.getItem('serverVersion')
  if (stored === v) return
  localStorage.removeItem('datacenterTabs')
  localStorage.removeItem('serverTabs')
  localStorage.removeItem('dcTabCurrent')
  localStorage.removeItem('srvTabCurrent')
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
      if (data.status === 'done') {
        orbShowImportStatus('is-success', 'fa-circle-check', `Imported ${data.currentVersion} successfully.`)
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
      showPublishToast('Divergence report published: ' + body.key, 'is-success')
      setTimeout(() => location.reload(), 1500)
    })
    .catch(err => showPublishToast(err.message, 'is-danger'))
    .finally(() => { if (btn) btn.classList.remove('is-loading') })
}

function showPublishToast(msg, cls) {
  const toast = document.getElementById('publish-toast')
  if (!toast) return
  toast.className = 'notification ' + cls
  toast.textContent = msg
  toast.classList.remove('is-hidden')
  setTimeout(() => toast.classList.add('is-hidden'), 4000)
}

// ─── window bridge for onclick handlers ──────────────────────────────────────

window.handleOrbImport = handleOrbImport
window.handleOrbImportLatest = handleOrbImportLatest
window.loadOrbTags = loadOrbTags
window.handleOrbCourierUpload = handleOrbCourierUpload
window.publishDivergence = publishDivergence
