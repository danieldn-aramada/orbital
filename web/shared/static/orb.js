// orb.js — orb-specific page logic

import {
  BASE,
  INVENTORY_CACHE_KEY,
  initInventoryTable,
  initDatacenterTable,
  initServerListTable,
  initClusterTable,
  loadDataCenterTab,
  loadServerListTab,
  loadClusterTab,
  saveTab,
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

// handleStatusPageImport wires the "Import v<N>" button on orb's Status page
// (banner shown when a new version is available). Triggers the same
// POST /api/v1/import + polling flow as the Import page, but updates the
// banner IN PLACE — spinner while running, then a terminal success/fail
// message with a link to /import-history for details. See
// web/templates/orb/pages/status.gohtml for the banner markup.
function handleStatusPageImport(tag) {
  const banner = document.getElementById('orb-status-import-banner')
  if (!banner) return
  renderStatusBanner(banner, 'is-info', 'fa-spinner fa-spin', `Importing ${tag}…`, null)
  fetch(BASE + '/api/v1/import', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tag }),
  })
    .then(r => r.json())
    .then(data => {
      if (data.error) {
        renderStatusBanner(banner, 'is-warning', 'fa-triangle-exclamation', data.error, '/import-history')
        return
      }
      pollStatusPageImport(banner, tag)
    })
    .catch(() => renderStatusBanner(banner, 'is-danger', 'fa-circle-xmark', 'Failed to start import.', '/import-history'))
}

let statusPagePollTimer = null

function pollStatusPageImport(banner, tag) {
  clearTimeout(statusPagePollTimer)
  fetch(BASE + '/api/v1/import/status')
    .then(r => r.json())
    .then(data => {
      if (data.status === 'done') {
        // Import replaced local DGraph; cached inventory is stale on tabs.
        sessionStorage.removeItem(INVENTORY_CACHE_KEY)
        clearStaleTabState()
        renderStatusBanner(banner, 'is-success', 'fa-circle-check', `Imported ${data.currentVersion} successfully.`, '/import-history')
      } else if (data.status === 'partial') {
        sessionStorage.removeItem(INVENTORY_CACHE_KEY)
        clearStaleTabState()
        renderStatusBanner(banner, 'is-warning', 'fa-circle-exclamation', `Imported ${data.currentVersion} with dispatch errors.`, '/import-history')
      } else if (data.status === 'failed') {
        renderStatusBanner(banner, 'is-danger', 'fa-circle-xmark', `Import failed: ${data.lastError || 'unknown error'}`, '/import-history')
      } else {
        renderStatusBanner(banner, 'is-info', 'fa-spinner fa-spin', `Importing ${tag}…`, null)
        statusPagePollTimer = setTimeout(() => pollStatusPageImport(banner, tag), 2000)
      }
    })
    .catch(() => { statusPagePollTimer = setTimeout(() => pollStatusPageImport(banner, tag), 3000) })
}

function renderStatusBanner(banner, colorClass, iconClass, text, historyLink) {
  banner.className = `notification ${colorClass} mb-4`
  banner.style.maxWidth = '620px'
  const link = historyLink
    ? `<div class="mt-3"><a class="button is-small" href="${BASE}${historyLink}"><span>View in Import History</span></a></div>`
    : ''
  banner.innerHTML = `
    <span class="icon-text">
      <span class="icon"><i class="fa-solid ${iconClass}"></i></span>
      <span>${text}</span>
    </span>
    ${link}
  `
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

// loadOrbTags fetches the tags-content fragment and swaps it into
// #orb-tags-content. Pagination nav inside the fragment is HTMX-driven —
// clicks fire fetches with new offsets and re-swap the same container, so
// this function is only called for initial load and Refresh button clicks.
// refresh=true appends ?refresh=1 which busts the server-side verify cache;
// operator affordance for "trust nothing, re-verify everything." Pagination
// nav clicks do NOT bust cache.
function loadOrbTags(refresh = false) {
  const container = document.getElementById('orb-tags-content')
  const btn = document.getElementById('btn-refresh-tags')
  if (!container) return
  if (btn) btn.classList.add('is-loading')
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  container.innerHTML = `
    <div style="overflow-x: auto">
      <table class="table is-striped is-hoverable is-fullwidth is-size-7">
        <thead><tr><th>Tag</th><th>Signature</th><th>Digest</th><th>Size</th><th></th></tr></thead>
        <tbody>${[1, 2, 3].map(() =>
    `<tr><td style="width:8%">${s()}</td><td style="width:12%">${s()}</td><td style="width:60%">${s()}</td><td style="width:8%">${s()}</td><td style="width:10%">${s()}</td></tr>`
  ).join('')}</tbody>
      </table>
    </div>`
  const url = BASE + '/api/v1/import/tags' + (refresh ? '?refresh=1' : '')
  fetch(url, { headers: { 'HX-Request': 'true' } })
    .then(r => r.text())
    .then(html => {
      container.innerHTML = html
      if (window.htmx) htmx.process(container)  // activate hx-get on pagination nav
    })
    .catch(() => {
      container.innerHTML = '<p class="has-text-danger">Failed to load tags.</p>'
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
  // Guard against non-import pages. #orb-tags-content is the outer wrapper
  // and is present in the initial-render HTML; #orb-tags-tbody only appears
  // AFTER the fragment swap, so it's not a reliable init signal.
  if (!document.getElementById('orb-tags-content')) return
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

window.addEventListener('load', initDatacenterTabRestoration)
window.addEventListener('load', initServerListTabRestoration)
window.addEventListener('load', initClusterTabRestoration)

// ─── Cross-app navigation and reload buttons ──────────────────────────────────

initRowNavigation()
initLinkNavigation()
initReloadButtons()

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
    .catch(() => {
      if (container) container.innerHTML = '<div class="notification is-danger is-light is-size-7 m-4"><strong>Reload failed.</strong> Check your connection and try again.</div>'
    })
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

// ─── Delegated click handlers ────────────────────────────────────────────────
//
// Default pattern across the codebase: document-level listener + .closest()
// instead of inline onclick + window bridge. Survives HTMX swaps automatically.
// See docs/reference/UI.md for the canonical rule and exceptions.

document.addEventListener('click', (e) => {
  const imp = e.target.closest('.js-orb-import')
  if (imp) { handleOrbImport(imp.dataset.tag); return }
  const statusImp = e.target.closest('.js-orb-status-import')
  if (statusImp) { handleStatusPageImport(statusImp.dataset.tag); return }
  if (e.target.closest('.js-orb-import-latest')) { handleOrbImportLatest(); return }
  if (e.target.closest('.js-orb-tags-refresh')) { loadOrbTags(true); return }
  if (e.target.closest('.js-orb-courier-upload')) { handleOrbCourierUpload(); return }
  if (e.target.closest('.js-orb-divergence-publish')) { publishDivergence(); return }
  if (e.target.closest('.js-orb-divergence-refresh')) { refreshOrbDivergence(); return }

  // Publish-history row expand — toggles the sibling detail row and swaps
  // the chevron direction. Detail row markup is emitted server-side; this
  // handler only manages visibility, so it survives HTMX pagination swaps.
  const exp = e.target.closest('.js-orb-publish-expand')
  if (exp) {
    const row = exp.closest('tr')
    const next = row?.nextElementSibling
    if (next?.classList.contains('js-orb-publish-detail')) {
      next.classList.toggle('is-hidden')
      const ico = exp.querySelector('.icon i')
      ico?.classList.toggle('fa-chevron-right')
      ico?.classList.toggle('fa-chevron-down')
    }
    return
  }
})
