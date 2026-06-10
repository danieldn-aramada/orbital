// orbital.js — orbital-specific page logic

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
  fetchWithMinDelay,
  initDcDetailTabs,
  initServerDetailTabs,
  dtWrapLengthSelect,
  openServerTab,
  initServerEventsTable,
  renderTimestamps,
  formatTimestamp,
  initInventoryTable,
  initDatacenterTable,
  initServerListTable,
  loadDataCenterTab,
  loadServerListTab,
  saveServerTab,
  initDatacenterTabRestoration,
  initServerListTabRestoration,
} from './shared.js'

// ─── Server drill-down in DC tab (dblclick → HTMX load into tab) ─────────────

document.addEventListener('dblclick', function (e) {
  const row = e.target.closest('tr[data-server-id]')
  if (!row) return
  const serverId = row.dataset.serverId
  const dcId = row.dataset.dcId
  const tabContent = document.getElementById('tab-content-' + dcId)
  if (!tabContent) return
  tabContent.dataset.loaded = ''
  htmx.ajax('GET', BASE + '/servers/' + serverId + '?dcCtx=1', { target: '#tab-content-' + dcId, swap: 'innerHTML' })
})

// ─── Inventory page ───────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => { initInventoryTable() })

// ─── Data Centers page ────────────────────────────────────────────────────────

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

window.addEventListener('load', initDatacenterTabRestoration)

// ─── Servers page ─────────────────────────────────────────────────────────────

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

window.addEventListener('load', initServerListTabRestoration)

// Double-click on server row in DC detail panel → navigate to /servers?open=<id>
document.addEventListener('dblclick', (e) => {
  const row = e.target.closest('tr[data-server-id]')
  if (!row) return
  const id = row.dataset.serverId
  const label = row.dataset.displayName || id
  window.location.href = BASE + '/servers?open=' + encodeURIComponent(id) + '&label=' + encodeURIComponent(label)
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
        pollBackup(data.jobId)
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

function testBackupConnection() {
  const btn = document.getElementById('btn-test-backup-connection')
  const result = document.getElementById('backup-connection-result')
  btn.classList.add('is-loading')
  result.textContent = ''
  fetch(BASE + '/api/v1/backup/test-connection', { method: 'POST' })
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
      pollExportStatus(json.jobId)
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
        showExportStatus('is-success', 'fa-circle-check', 'Export complete.', jobId)
      } else if (job.status === 'failed') {
        showExportStatus('is-danger', 'fa-circle-xmark', `Export failed: ${job.error ?? 'unknown error'}`)
      } else {
        exportPollTimer = setTimeout(() => pollExportStatus(jobId), 2000)
        showExportStatus('is-info', 'fa-spinner fa-spin', job.status === 'running' ? 'Exporting…' : 'Pending…')
      }
    })
    .catch(() => { exportPollTimer = setTimeout(() => pollExportStatus(jobId), 3000) })
}

function showExportStatus(colorClass, iconClass, text, downloadJobId) {
  const box = document.getElementById('export-status-box')
  const article = document.getElementById('export-status-article')
  const icon = document.getElementById('export-status-icon')
  const textEl = document.getElementById('export-status-text')
  const dlWrap = document.getElementById('export-download-link')
  const dlAnchor = document.getElementById('export-download-anchor')

  article.className = `message ${colorClass}`
  icon.innerHTML = `<i class="fa-solid ${iconClass}"></i>`
  textEl.textContent = text
  box.style.display = ''

  if (downloadJobId) {
    dlAnchor.href = BASE + `/api/v1/export/jobs/${downloadJobId}/download`
    dlWrap.style.display = ''
  } else {
    dlWrap.style.display = 'none'
  }
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

function loadArtifactsTable(showSpinner = false) {
  const tbody = document.getElementById('artifacts-tbody')
  if (!tbody) return
  const btn = document.querySelector('button[onclick="loadArtifactsTable(true)"]')
  if (showSpinner && btn) btn.classList.add('is-loading')
  const minDelay = new Promise(resolve => setTimeout(resolve, showSpinner ? 200 : 0))
  fetch(BASE + '/api/v1/oci/artifacts', { headers: { 'HX-Request': 'true' } })
    .then(r => r.text())
    .then(html => { tbody.innerHTML = html })
    .catch(() => {})
    .finally(() => { minDelay.then(() => { if (btn) btn.classList.remove('is-loading') }) })
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

      document.getElementById('dc-edit-submit-' + id).addEventListener('click', async () => {
        const btn = document.getElementById('dc-edit-submit-' + id)
        clearError()
        btn.classList.add('is-loading')
        btn.disabled = true
        try {
          let vars
          try { vars = JSON.parse(editor.get().text) } catch (_) {
            showError('Invalid JSON — fix the syntax and try again.')
            return
          }
          if (vars.assetDataV2 !== undefined && vars.assetDataV2 !== null && typeof vars.assetDataV2 !== 'string') {
            vars.assetDataV2 = JSON.stringify(vars.assetDataV2)
          }
          const currentVersion = parseInt(modal.dataset.version, 10) || 0
          const resp = await fetch(BASE + '/api/v1/graphql', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              query: `mutation UpdateDataCenter(
                $id: ID!, $name: String!, $assetDataV2: String,
                $version: Int, $updatedBy: String!, $updatedAt: DateTime!
              ) {
                updateDataCenter(input: {
                  filter: { id: [$id] }
                  set: { name: $name, assetDataV2: $assetDataV2, version: $version, updatedBy: $updatedBy, updatedAt: $updatedAt }
                }) {
                  dataCenter { id name }
                }
              }`,
              variables: {
                ...vars,
                id,
                orbId: modal.dataset.orbId || '',
                ifVersion: currentVersion,
                version: currentVersion + 1,
                updatedBy: modal.dataset.currentUser || '',
                updatedAt: new Date().toISOString(),
              },
            }),
          })
          if (!resp.ok) {
            if (resp.status === 409) {
              const body = await resp.json().catch(() => ({}))
              showError(body.error || 'Conflict — please reload and try again.')
            } else {
              showError(`Server error (${resp.status}) — try again.`)
            }
            return
          }
          const result = await resp.json()
          if (result.errors && result.errors.length > 0) { showError(result.errors[0].message); return }
          modal.classList.remove('is-active')
          document.documentElement.style.overflow = ''
          dcEditors.delete(id)
          const _tabContent = document.getElementById('tab-content-' + id)
          if (_tabContent) {
            fetch(BASE + '/datacenters/' + id, { headers: { 'HX-Request': 'true' } })
              .then(r => r.text())
              .then(html => {
                _tabContent.innerHTML = html
                htmx.process(_tabContent)
                renderTimestamps(_tabContent)
                initDcDetailTabs(id)
                initServerDetailTabs(_tabContent)
              })
              .catch(() => {})
          }
        } catch (err) {
          showError('Request failed — check your connection and try again.')
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

// ─── Tab reloads ──────────────────────────────────────────────────────────────

document.addEventListener('click', function (e) {
  const btn = e.target.closest('.js-dc-reload')
  if (!btn) return
  const id = btn.dataset.dcId
  const target = document.getElementById('tab-content-' + id)
  if (!target) return
  showDatacenterSkeleton(id)
  fetchWithMinDelay('/datacenters/' + id)
    .then(html => {
      target.innerHTML = html
      htmx.process(target)
      renderTimestamps(target)
      initDcDetailTabs(id)
      initServerDetailTabs(target)
    })
    .catch(() => {})
})

document.addEventListener('click', function (e) {
  const btn = e.target.closest('.js-srv-reload')
  if (!btn) return
  const url = btn.dataset.srvUrl
  const targetId = btn.dataset.srvTarget
  const target = document.getElementById(targetId)
  if (!target) return
  showServerSkeleton(targetId, btn.dataset.srvSkeleton)
  fetchWithMinDelay(url)
    .then(html => {
      target.innerHTML = html
      htmx.process(target)
      renderTimestamps(target)
      const srvDetailTabs = target.querySelector('[id^="srv-detail-tabs-"]')
      if (srvDetailTabs) {
        target.dataset.loaded = 'true'
        initServerDetailTabs(target)
        srvEditors.delete(srvDetailTabs.id.replace('srv-detail-tabs-', ''))
      }
      const dcDetailTabs = target.querySelector('[id^="dc-detail-tabs-"]')
      if (dcDetailTabs) {
        const id = dcDetailTabs.id.replace('dc-detail-tabs-', '')
        target.dataset.loaded = 'true'
        initDcDetailTabs(id)
        dcEditors.delete(id)
        initServerDetailTabs(target)
        const dcServersTable = target.querySelector('table[id^="dc-servers-table-"]')
        if (dcServersTable && !$.fn.DataTable.isDataTable(dcServersTable)) {
          new DataTable(dcServersTable, { paging: false, searching: false, info: false, ordering: true, select: { style: 'os' }, autoWidth: true, columnDefs: [{ className: 'dt-left', targets: 5 }] })
        }
      }
      const defaultTabLink = target.querySelector('.detlinks.is-active')
      if (defaultTabLink) openServerTab(defaultTabLink.id.replace(/-detlink$/, '-det'))
      target.querySelectorAll('[id$="-ev"]').forEach(el => {
        const serverId = el.id.split('-')[0]
        setTimeout(() => initServerEventsTable(serverId), 100)
      })
    })
    .catch(() => {})
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
      editor.set({ text: JSON.stringify(JSON.parse(initialJSON), null, 2) })
      srvEditors.set(id, editor)

      const errorEl = document.getElementById('srv-edit-error-' + id)
      const showError = (msg) => { errorEl.textContent = msg; errorEl.style.display = '' }
      const clearError = () => { errorEl.textContent = ''; errorEl.style.display = 'none' }

      document.getElementById('srv-edit-submit-' + id).addEventListener('click', async () => {
        const btn = document.getElementById('srv-edit-submit-' + id)
        clearError()
        btn.classList.add('is-loading')
        btn.disabled = true
        try {
          let vars
          try { vars = JSON.parse(editor.get().text) } catch (_) {
            showError('Invalid JSON — fix the syntax and try again.')
            return
          }
          const currentVersion = parseInt(modal.dataset.version, 10) || 0
          const idracSettings = vars.idracSettings ?? {}
          delete vars.idracSettings
          const idracOrbId = (modal.dataset.orbId || '') + '-idrac'
          const idracNamespace = (modal.dataset.orbId || '').split(':')[0]
          const now = new Date().toISOString()
          const currentUser = modal.dataset.currentUser || ''
          const resp = await fetch(BASE + '/api/v1/graphql', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              query: `mutation UpdateServerAndIdrac(
                $id: ID!, $hostname: String, $manufacturer: String, $model: String,
                $oobMAC: String, $rackPosition: Int, $serviceTag: String,
                $version: Int, $updatedBy: String!, $updatedAt: DateTime!,
                $idracInput: [AddIdracSettingsInput!]!
              ) {
                updateServer(input: {
                  filter: { id: [$id] }
                  set: {
                    hostname: $hostname, manufacturer: $manufacturer, model: $model,
                    oobMAC: $oobMAC, rackPosition: $rackPosition, serviceTag: $serviceTag,
                    version: $version, updatedBy: $updatedBy, updatedAt: $updatedAt
                  }
                }) {
                  server { id hostname }
                }
                addIdracSettings(input: $idracInput, upsert: true) {
                  numUids
                }
              }`,
              variables: {
                ...vars,
                id,
                orbId: modal.dataset.orbId || '',
                ifVersion: currentVersion,
                version: currentVersion + 1,
                updatedBy: currentUser,
                updatedAt: now,
                idracInput: [{
                  orbId: idracOrbId,
                  name: 'idrac',
                  namespace: { name: idracNamespace },
                  createdBy: currentUser,
                  createdAt: now,
                  updatedBy: currentUser,
                  updatedAt: now,
                  server: { id },
                  firmwareVersion: idracSettings.firmwareVersion ?? null,
                  sshEnabled: idracSettings.sshEnabled ?? null,
                  ipmiEnabled: idracSettings.ipmiEnabled ?? null,
                  lockdownModeEnabled: idracSettings.lockdownModeEnabled ?? null,
                  osToIdracPassThroughEnabled: idracSettings.osToIdracPassThroughEnabled ?? null,
                  usbManagementPortEnabled: idracSettings.usbManagementPortEnabled ?? null,
                  dhcpEnabled: idracSettings.dhcpEnabled ?? null,
                  racadmEnabled: idracSettings.racadmEnabled ?? null,
                }],
              },
            }),
          })
          if (!resp.ok) {
            if (resp.status === 409) {
              const body = await resp.json().catch(() => ({}))
              showError(body.error || 'Conflict — please reload and try again.')
            } else {
              showError(`Server error (${resp.status}) — try again.`)
            }
            return
          }
          const result = await resp.json()
          if (result.errors && result.errors.length > 0) { showError(result.errors[0].message); return }
          modal.classList.remove('is-active')
          document.documentElement.style.overflow = ''
          srvEditors.delete(id)
          htmx.ajax('GET', BASE + modal.dataset.reloadUrl, { target: '#' + modal.dataset.reloadTarget, swap: 'innerHTML' })
        } catch (err) {
          showError('Request failed — check your connection and try again.')
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
    auditTable.ajax.reload(() => { reloadBtn.removeClass('is-loading') }, false)
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
        pollRestore(data.jobId)
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

    fetch(BASE + '/api/v1/config-items/delete-preview?id=' + encodeURIComponent(id) + '&type=' + type)
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
      const r = await fetch(BASE + '/api/v1/config-items?id=' + encodeURIComponent(id) + '&type=' + type, { method: 'DELETE' })
      if (!r.ok) {
        const t = await r.text()
        throw new Error(t || 'Delete failed')
      }
      closeCfgDeleteModal()
      if (type === 'DataCenter') {
        const tabClose = document.querySelector('#tab-close-' + id)
        if (tabClose) tabClose.click()
        const reloadBtn = document.querySelector('#btn-reload-datacenters')
        if (reloadBtn) reloadBtn.click()
      } else if (type === 'Server' && dcId) {
        htmx.ajax('GET', BASE + '/datacenters/' + dcId, { target: '#tab-content-' + dcId, swap: 'innerHTML' })
      } else {
        const tabClose = document.querySelector('#tab-close-srv-' + id)
        if (tabClose) tabClose.click()
        const reloadBtn = document.querySelector('#btn-reload-servers')
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
window.testBackupConnection = testBackupConnection
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
