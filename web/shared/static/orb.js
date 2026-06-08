// orb.js — orb-specific page logic

import { BASE } from './shared.js'

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

// ─── Orb servers DataTable ────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('orb-servers-table')) return

  const orbServersTable = $('#orb-servers-table').DataTable({
    pageLength: 25,
    order: [[0, 'asc']],
    columns: [
      { data: 'hostname' },
      { data: 'serviceTag' },
      { data: 'model' },
      { data: 'manufacturer' },
      { data: 'oobIP' },
      { data: 'rack' },
      { data: 'rackPosition' },
    ],
    ajax: {
      url: BASE + '/graphql',
      type: 'POST',
      contentType: 'application/json',
      data: () => JSON.stringify({
        query: `{ queryServer {
          id orbId hostname serviceTag model manufacturer oobMAC rackPosition
          oobIP { address }
          rack { name }
        } }`,
      }),
      dataSrc: (json) => (json.data?.queryServer ?? []).map(s => ({
        id: s.id,
        orbId: s.orbId ?? '—',
        hostname: s.hostname ?? '—',
        serviceTag: s.serviceTag ?? '—',
        model: s.model ?? '—',
        manufacturer: s.manufacturer ?? '—',
        oobIP: s.oobIP?.address ?? '—',
        rack: s.rack?.name ?? '—',
        rackPosition: s.rackPosition || '—',
      })),
    },
    createdRow: function (row) { row.style.cursor = 'pointer' },
  })

  $('#orb-servers-table tbody').on('click', 'tr', function () {
    const data = orbServersTable.row(this).data()
    if (data && data.id) window.location = BASE + '/servers/' + data.id
  })
})

// ─── Orb DC page ──────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  const page = document.getElementById('orb-dc-page')
  if (!page) return

  const loading = document.getElementById('orb-dc-loading')
  const content = document.getElementById('orb-dc-content')
  const empty = document.getElementById('orb-dc-empty')

  fetch(BASE + '/graphql', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      query: `{
        queryDataCenter {
          id orbId name createdAt updatedAt
          namespace { name }
          racks(order: { asc: name }) { id name }
          serversAggregate { count }
          servers(order: { asc: rackPosition }) {
            id orbId hostname serviceTag model manufacturer
            oobIP { address }
            rackPosition
            rack { name }
          }
        }
      }`,
    }),
  })
    .then(r => r.json())
    .then(json => {
      loading.style.display = 'none'
      const list = json.data?.queryDataCenter ?? []
      if (list.length === 0) { empty.style.display = ''; return }
      const dc = list[0]

      document.getElementById('orb-dc-name').textContent = dc.name ?? '—'
      document.getElementById('orb-dc-server-count').textContent = dc.serversAggregate?.count ?? '—'
      document.getElementById('orb-dc-rack-count').textContent = dc.racks?.length ?? '—'
      document.getElementById('orb-dc-orb-id').textContent = dc.orbId || '—'
      document.getElementById('orb-dc-namespace').textContent = dc.namespace?.name || '—'
      document.getElementById('orb-dc-created-at').textContent = dc.createdAt || '—'
      document.getElementById('orb-dc-updated-at').textContent = dc.updatedAt || '—'

      const servers = dc.servers ?? []
      $('#orb-dc-servers-table').DataTable({
        pageLength: 25,
        order: [[4, 'asc'], [5, 'asc']],
        data: servers.map(s => ({
          id: s.id,
          hostname: s.hostname ?? '—',
          serviceTag: s.serviceTag ?? '—',
          model: s.model ?? '—',
          oobIP: s.oobIP?.address ?? '—',
          rack: s.rack?.name ?? '—',
          rackPosition: s.rackPosition || '—',
        })),
        columns: [
          { data: 'hostname' },
          { data: 'serviceTag' },
          { data: 'model' },
          { data: 'oobIP' },
          { data: 'rack' },
          { data: 'rackPosition' },
        ],
        createdRow: function (row) { row.style.cursor = 'pointer' },
      })

      $('#orb-dc-servers-table tbody').on('click', 'tr', function () {
        const table = $('#orb-dc-servers-table').DataTable()
        const data = table.row(this).data()
        if (data && data.id) window.location = BASE + '/servers/' + data.id
      })

      content.style.display = ''
    })
    .catch(() => { loading.style.display = 'none'; empty.style.display = '' })
})

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
