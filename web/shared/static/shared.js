// shared.js — utilities used by both orbital and orb

export const BASE = window.ORBITAL_BASE || ''

// safeDomId converts an orbId into a DOM/CSS-selector-safe identifier.
// orbIds typically contain ":" (e.g. "2f-uae:5HSC3D4"), which is a pseudo-class
// operator in CSS selectors and breaks `$('#tab-...')` and querySelector. We
// store the raw orbId in URL paths (encoded) and in data-orb-id attributes;
// for DOM element IDs we map non-alphanumeric chars to "_".
export function safeDomId(orbId) {
  return String(orbId).replace(/[^A-Za-z0-9_-]/g, '_')
}

// Bump when the localStorage tab schema changes. Older entries keyed by
// DGraph UID are dropped on first load post-migration so users don't see dead
// tabs reopen against /datacenters/0x... URLs that no longer exist.
const TAB_STATE_SCHEMA = 'v2-orbid'
function migrateLegacyTabState() {
  if (localStorage.tabStateSchema === TAB_STATE_SCHEMA) return
  localStorage.removeItem('datacenterTabs')
  localStorage.removeItem('serverTabs')
  localStorage.removeItem('dcTabCurrent')
  localStorage.removeItem('srvTabCurrent')
  localStorage.removeItem('tabCurrent')
  localStorage.tabStateSchema = TAB_STATE_SCHEMA
}
migrateLegacyTabState()

// gqlErrorMessage formats GraphQL `errors[]` for display. Returns null on
// clean responses, or a "message (path)" string otherwise. Multiple errors
// are joined with "; ". Use this for both modal mutations and read queries —
// HTTP 200 from GraphQL does NOT mean success; the body may carry errors.
export function gqlErrorMessage(json) {
  if (!json?.errors?.length) return null
  return json.errors.map(e => {
    const path = Array.isArray(e.path) ? e.path.join('.') : ''
    const msg = e.message || 'unknown error'
    return path ? `${msg} (${path})` : msg
  }).join('; ')
}
window.gqlErrorMessage = gqlErrorMessage

// gqlSurfaceErrors is the read-only-query counterpart: logs to console and
// returns false so callers can bail. No UI banner — read queries that fail
// should leave the table empty rather than overlay an alert on top of nav.
// For modal mutations, call gqlErrorMessage() directly and route the message
// into the modal's own error notification.
export function gqlSurfaceErrors(json, label) {
  const msg = gqlErrorMessage(json)
  if (!msg) return true
  // eslint-disable-next-line no-console
  console.error(`[graphql] ${label} failed:`, msg, json.errors)
  return false
}
window.gqlSurfaceErrors = gqlSurfaceErrors

// ─── Tab management ───────────────────────────────────────────────────────────

export class TabItem {
  constructor(displayName, id) {
    this.displayName = displayName
    this.id = id
  }
}

export function unloadTab(orbId) {
  const domId = safeDomId(orbId)
  document.getElementById(`tab-${domId}`)?.parentElement?.remove()
  document.getElementById(`tab-content-${domId}`)?.remove()
}

export function loadTab(displayName, orbId) {
  const domId = safeDomId(orbId)
  const url = `${BASE}/servers/${encodeURIComponent(orbId)}`
  const html = `<li class="tab">
    <a id="tab-${domId}" data-target="tab-content-${domId}" role="tab" aria-selected="false" tabindex="-1"
      hx-get="${url}" hx-trigger="click" hx-target="#tab-content-${domId}" hx-swap="innerHTML">
      ${displayName}
      <span class="pl-2">
        <button id="tab-close-${domId}">
          <i class="fa-solid fa-xmark" style="font-size: 0.8em;"></i>
        </button>
      </span>
    </a>
  </li>`

  const content = `<div class="tab-content" id="tab-content-${domId}" role="tabpanel" style="display:none">`

  $('#tablist').append(html)
  $('.app-main').append(content)

  const tabLink = document.getElementById(`tab-${domId}`)
  const tabContent = document.getElementById(`tab-content-${domId}`)
  htmx.process(tabLink)
  htmx.process(tabContent)

  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(`tab-content-${domId}`)
    setCurrentTab(`tab-${domId}`)
  })

  const tabClose = document.getElementById(`tab-close-${domId}`)
  tabClose.addEventListener('click', (event) => {
    event.stopPropagation()
    unloadTab(orbId)
    deleteTab(displayName, orbId)
    document.getElementById('tab-summary').click()
    replaceCurrentTab(`tab-${domId}`, 'tab-summary')
  })
}

function getLastTab() {}

export function deleteTab(displayName, itemId) {
  const tabToDelete = new TabItem(displayName, itemId)
  if (localStorage.datacenterTabs) {
    const s = new Set(JSON.parse(localStorage.datacenterTabs))
    s.delete(JSON.stringify(tabToDelete))
    localStorage.datacenterTabs = JSON.stringify([...s])
  }
}

export function saveTab(displayName, itemId) {
  const tabToAdd = new TabItem(displayName, itemId)
  if (localStorage.datacenterTabs) {
    const s = new Set(JSON.parse(localStorage.datacenterTabs))
    if (!s.has(JSON.stringify(tabToAdd))) {
      s.add(JSON.stringify(tabToAdd))
      localStorage.datacenterTabs = JSON.stringify([...s])
    }
  } else {
    const s = new Set([JSON.stringify(tabToAdd)])
    localStorage.datacenterTabs = JSON.stringify([...s])
  }
}

export function closeTab(orbId) {
  const domId = safeDomId(orbId)
  document.getElementById(`tab-close-${domId}`)?.click()
  document.getElementById(`btn-reload-servers`)?.click()
}

export function getTabStorageKey() {
  if (document.getElementById('server-list-table')) return 'srvTabCurrent'
  return 'dcTabCurrent'
}

export function setCurrentTab(id) {
  localStorage[getTabStorageKey()] = id
}

export function removeCurrentTab(id) {
  const key = getTabStorageKey()
  if (localStorage[key] === id) localStorage.removeItem(key)
}

export function replaceCurrentTab(currentId, targetId) {
  const key = getTabStorageKey()
  if (localStorage[key] === currentId) localStorage.setItem(key, targetId)
}

export function getCurrentTab() {
  return localStorage[getTabStorageKey()]
}

export function activateTab(selected) {
  ;(document.querySelectorAll('li.tab') || []).forEach((tab) => {
    tab.classList.toggle('is-active', tab === selected)
  })
}

export function displayTabContent(id) {
  ;(document.querySelectorAll('.tab-content') || []).forEach((tabContent) => {
    tabContent.style.display = tabContent.id === id ? 'block' : 'none'
  })
}

export function changeTabs(e) {
  const targetTab = e.target
  const tabList = targetTab.parentNode
  tabList
    .querySelectorAll(':scope > [aria-selected="true"]')
    .forEach((t) => t.setAttribute('aria-selected', false))
  targetTab.setAttribute('aria-selected', true)
}

// ─── Timestamps ───────────────────────────────────────────────────────────────

export function formatTimestamp(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  const pad = (n) => n.toString().padStart(2, '0')
  const year = d.getFullYear()
  const month = pad(d.getMonth() + 1)
  const day = pad(d.getDate())
  const hours = pad(d.getHours())
  const minutes = pad(d.getMinutes())
  const seconds = pad(d.getSeconds())
  const tz = d.toLocaleTimeString('en-us', { timeZoneName: 'short' }).split(' ')[2]
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds} ${tz}`
}

export function relativeTime(iso) {
  if (!iso) return ''
  const diff = (new Date(iso) - Date.now()) / 1000
  const abs = Math.abs(diff)
  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })
  if (abs < 60)         return rtf.format(Math.round(diff), 'second')
  if (abs < 3600)       return rtf.format(Math.round(diff / 60), 'minute')
  if (abs < 86400)      return rtf.format(Math.round(diff / 3600), 'hour')
  if (abs < 86400 * 7)  return rtf.format(Math.round(diff / 86400), 'day')
  return formatTimestamp(iso)
}

export function renderTimestamps(root) {
  ;(root || document).querySelectorAll('[data-timestamp]').forEach(el => {
    const iso = el.dataset.timestamp
    if (!iso) return
    el.textContent = relativeTime(iso)
    el.title = formatTimestamp(iso)
  })
}

document.addEventListener('DOMContentLoaded', () => renderTimestamps(document))

// ─── Server events DataTable ──────────────────────────────────────────────────

export const serverTables = new Map()

export function initServerEventsTable(serverId) {
  const tableId = `${serverId}-ev`
  const $table = $(`#${tableId}`)

  if ($.fn.dataTable.isDataTable($table)) {
    const existingTable = $table.DataTable()
    existingTable.ajax.reload(null, false)
    existingTable.columns.adjust().draw(false)
    setTimeout(() => {
      const wrapper = document.querySelector('[id$="-ev_wrapper"] > .columns.is-multiline')
      if (wrapper) wrapper.style.display = 'none'
    }, 50)
    return
  }

  const table = $table.DataTable({
    dom: '',
    scrollX: true,
    searching: false,
    paging: false,
    autoWidth: true,
    order: [[0, 'desc']],
    ajax: {
      url: BASE + `/api/v1/servers/${serverId}/events`,
      method: 'GET',
      dataSrc: '',
      deferRender: true,
      cache: true,
      timeout: 30000,
    },
    columnDefs: [
      { target: 0, width: '80px' },
      { target: 1, width: '190px' },
      { target: 2, width: '140px' },
      { target: 3, className: 'wrap-text', width: '400px' },
      { target: 4, className: 'no-wrap-text' },
    ],
    columns: [
      { data: 'type' },
      { data: 'timestamp', render: (data) => formatTimestamp(data) },
      { data: 'userId' },
      { data: 'message' },
      {
        data: 'details',
        render: function (data) {
          if (!data || !data.diff) return ''
          const diffLines = data.diff.split('\n')
          const filtered = []
          let skip = false
          for (const line of diffLines) {
            if (line.startsWith('@')) {
              skip = line.includes('"version"')
              if (!skip) filtered.push(line)
              continue
            }
            if (!skip) filtered.push(line)
          }
          const colored = filtered.map(line => {
            if (line.startsWith('+')) return `<span class="diff-added">${line}</span>`
            if (line.startsWith('-')) return `<span class="diff-removed">${line}</span>`
            return line
          }).join('\n')
          return `<pre class="diff-output">${colored}</pre>`
        },
      },
    ],
  })
  serverTables.set(tableId, table)
}

export function openServerTab(tabId) {
  const serverId = tabId.split('-')[0]
  document.querySelectorAll(`[id^="${serverId}-"].detcontent`).forEach(d => d.classList.add('is-hidden'))
  document.querySelectorAll(`[id^="${serverId}-"].detlinks`).forEach(el => el.classList.remove('is-active'))
  const panel = document.getElementById(tabId)
  if (panel) panel.classList.remove('is-hidden')
  const header = document.getElementById(tabId + '-link') || document.getElementById(tabId.replace('-det', '-detlink'))
  if (header) header.classList.add('is-active')
  if (tabId.endsWith('-ev-det')) {
    setTimeout(() => initServerEventsTable(serverId), 100)
  }
}

// ─── Skeletons + detail tab helpers ──────────────────────────────────────────

export function showDatacenterSkeleton(orbId) {
  const target = document.getElementById(`tab-content-${safeDomId(orbId)}`)
  if (!target) return
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  const summary = ['Name', 'Servers', 'Racks', 'Asset Data']
    .map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')
  const meta = ['Namespace', 'Orb ID', 'Created By', 'Created At', 'Last Updated', 'Last Updated By']
    .map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')
  const srvRows = Array.from({ length: 10 }, () =>
    `<tr>${['', '', '', '', '', ''].map(() => `<td>${s()}</td>`).join('')}</tr>`
  ).join('')
  target.innerHTML = `
    <div class="fixed-grid has-3-cols mb-0">
      <div class="columns m-0">
        <div class="column pt-0 pl-0">
          <button class="button is-rounded is-small is-link mt-1 is-loading" disabled>
            <span class="icon"><i class="fa-solid fa-refresh"></i></span><span>Reload</span>
          </button>
          <button class="button is-rounded is-small is-link mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-pen-to-square"></i></span><span>Edit</span>
          </button>
          <button class="button is-rounded is-small is-danger mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-trash"></i></span><span>Delete</span>
          </button>
        </div>
      </div>
      <div class="grid">
        <div class="cell is-col-span-2 is-row-span-1">
          <article class="box">
            <p class="is-size-4 pb-4">Data Center Summary</p>
            <div style="overflow-x:auto"><table class="table is-fullwidth"><tbody>${summary}</tbody></table></div>
          </article>
        </div>
        <div class="cell is-row-span-1">
          <article class="box" style="height:100%">
            <p class="is-size-4 mb-4">Metadata</p>
            <div style="overflow-x:auto"><table class="table mb-0"><tbody>${meta}</tbody></table></div>
          </article>
        </div>
        <div class="cell is-col-span-3">
          <article class="box pb-2">
            <p class="is-size-4 pb-4">Details</p>
            <div class="tabs is-boxed">
              <ul>
                <li class="is-active"><a><span class="icon is-small"><i class="fa-solid fa-server"></i></span><span>Servers</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-table-cells"></i></span><span>Racks</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-triangle-exclamation"></i></span><span>Divergence Reports</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-clock-rotate-left"></i></span><span>Audit Log</span></a></li>
              </ul>
            </div>
            <div style="min-height:400px">
              <table class="table is-striped is-fullwidth is-size-7 mt-2">
                <thead><tr><th>OOB IP</th><th>Model</th><th>Service Tag</th><th>Hostname</th><th>Rack</th><th>Rack Position</th></tr></thead>
                <tbody>${srvRows}</tbody>
              </table>
            </div>
          </article>
        </div>
      </div>
    </div>`
}

export function showClusterSkeleton(orbId) {
  const domId = safeDomId(orbId)
  const target = document.getElementById('tab-content-cluster-' + domId)
  if (!target) return
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  const summary = [
    'Name', 'Data Center', 'Provider', 'Cluster Type', 'Tinkerbell IP',
    'K8s Version', 'Control Plane Endpoint', 'CNI', 'Environment', 'Nodes',
  ].map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')
  const meta = ['Namespace', 'Orb ID', 'Created By', 'Created At', 'Last Updated', 'Last Updated By']
    .map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')

  target.innerHTML = `
    <div class="fixed-grid has-3-cols mb-0">
      <div class="columns m-0">
        <div class="column pt-0 pl-0">
          <button class="button is-rounded is-small is-link mt-1 is-loading" disabled>
            <span class="icon"><i class="fa-solid fa-refresh"></i></span><span>Reload</span>
          </button>
          <button class="button is-rounded is-small is-link mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-pen-to-square"></i></span><span>Edit</span>
          </button>
          <button class="button is-rounded is-small is-danger mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-trash"></i></span><span>Delete</span>
          </button>
        </div>
      </div>
      <div class="grid">
        <div class="cell is-col-span-2 is-row-span-1">
          <article class="box">
            <p class="is-size-4 pb-4">Cluster Summary</p>
            <div style="overflow-x:auto"><table class="table is-fullwidth"><tbody>${summary}</tbody></table></div>
          </article>
        </div>
        <div class="cell is-row-span-1">
          <article class="box" style="height:100%">
            <p class="is-size-4 mb-4">Metadata</p>
            <div style="overflow-x:auto"><table class="table mb-0"><tbody>${meta}</tbody></table></div>
          </article>
        </div>
        <div class="cell is-col-span-3">
          <article class="box pb-2">
            <p class="is-size-4 pb-4">Details</p>
            <div class="tabs is-boxed">
              <ul>
                <li class="is-active"><a><span class="icon is-small"><i class="fa-solid fa-server"></i></span><span>Nodes</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-floppy-disk"></i></span><span>Backups</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-sitemap"></i></span><span>Workload Clusters</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-clock-rotate-left"></i></span><span>Audit Log</span></a></li>
              </ul>
            </div>
            <div style="min-height:200px"><span class="is-skeleton" style="display:block;height:120px">&nbsp;</span></div>
          </article>
        </div>
      </div>
    </div>`
}

export function showServerSkeleton(targetId, variant) {
  const target = document.getElementById(targetId)
  if (!target) return
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  const rows = [
    'Data Center', 'Hostname', 'Manufacturer', 'Model',
    'OOB IP', 'OOB MAC', 'Rack', 'Rack Position', 'Service Tag',
  ].map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')
  const meta = ['Namespace', 'Orb ID', 'Created By', 'Created At', 'Last Updated', 'Last Updated By']
    .map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')

  const isOrb = variant === 'orb'
  const buttons = isOrb ? `
          <button class="button is-rounded is-small is-link mt-1 is-loading" disabled>
            <span class="icon"><i class="fa-solid fa-refresh"></i></span><span>Reload</span>
          </button>
          <button class="button is-rounded is-small is-warning mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-pen-to-square"></i></span><span>Override</span>
          </button>` : `
          <button class="button is-rounded is-small is-link mt-1 is-loading" disabled>
            <span class="icon"><i class="fa-solid fa-refresh"></i></span><span>Reload</span>
          </button>
          <button class="button is-rounded is-small is-link mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-pen-to-square"></i></span><span>Edit</span>
          </button>
          <button class="button is-rounded is-small is-danger mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-trash"></i></span><span>Delete</span>
          </button>`
  const detailTabs = isOrb ? `
                <li class="is-active"><a><span class="icon is-small"><i class="fa-solid fa-microchip"></i></span><span>iDRAC Settings</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-hard-drive"></i></span><span>Storage</span></a></li>` : `
                <li class="is-active"><a><span class="icon is-small"><i class="fa-solid fa-microchip"></i></span><span>iDRAC Settings</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-hard-drive"></i></span><span>Storage</span></a></li>
                <li><a><span class="icon is-small"><i class="fa-solid fa-clock-rotate-left"></i></span><span>Audit Log</span></a></li>`

  target.innerHTML = `
    <div class="fixed-grid has-3-cols mb-0">
      <div class="columns m-0">
        <div class="column pt-0 pl-0">${buttons}
        </div>
      </div>
      <div class="grid">
        <div class="cell is-col-span-2 is-row-span-1">
          <article class="box">
            <p class="is-size-4 pb-4">Server Summary</p>
            <div style="overflow-x:auto"><table class="table is-fullwidth"><tbody>${rows}</tbody></table></div>
          </article>
        </div>
        <div class="cell is-row-span-1">
          <article class="box" style="height:100%">
            <p class="is-size-4 mb-4">Metadata</p>
            <div style="overflow-x:auto"><table class="table mb-0"><tbody>${meta}</tbody></table></div>
          </article>
        </div>
        <div class="cell is-col-span-3">
          <article class="box pb-2">
            <p class="is-size-4 pb-4">Details</p>
            <div class="tabs is-boxed">
              <ul>${detailTabs}
              </ul>
            </div>
            <div style="min-height:300px">
              <table class="table is-fullwidth mt-2"><tbody>${[
                'Firmware Version', 'SSH Enabled', 'USB Mgmt Port Enabled',
                'OS-to-iDRAC Pass-through', 'IPMI Enabled', 'Lockdown Mode',
                'DHCP Enabled', 'RACADM Enabled',
              ].map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')}</tbody></table>
            </div>
          </article>
        </div>
      </div>
    </div>`
}

export function fetchWithMinDelay(url, minMs = 500) {
  return Promise.all([
    fetch(BASE + url, { headers: { 'HX-Request': 'true' } }).then(r => r.text()),
    new Promise(resolve => setTimeout(resolve, minMs)),
  ]).then(([html]) => html)
}

export function initDcDetailTabs(id) {
  const tabContainer = document.getElementById(`dc-detail-tabs-${id}`)
  if (!tabContainer) return

  const tabs = tabContainer.querySelectorAll('li[data-panel]')
  const storageKey = `dc-detail-tab-${id}`
  const auditPanelId = `dc-panel-audit-${id}`

  function loadAuditPanel() {
    const tab = [...tabs].find(t => t.dataset.panel === auditPanelId)
    if (!tab) return
    // Templates can embed the full subgraph orbId list in data-related-orb-ids
    // so the audit panel pulls events for the parent AND its nested config
    // items (e.g. Server + IdracSettings + StorageControllers) in one call.
    // Falls back to data-orb-id when the related list is missing.
    const related = (tab.dataset.relatedOrbIds || tab.dataset.orbId || '')
      .split(',').map(s => s.trim()).filter(Boolean)
    if (related.length === 0) return
    const panel = document.getElementById(auditPanelId)
    if (!panel) return
    const qs = related.map(id => `orbId=${encodeURIComponent(id)}`).join('&')
    fetch(BASE + `/api/v1/audit-log?${qs}&limit=50`, {
      headers: { 'HX-Request': 'true' },
    })
      .then(r => r.text())
      .then(html => { panel.innerHTML = html; renderTimestamps(panel) })
      .catch(() => {})
  }

  function activatePanel(panelId) {
    tabs.forEach(t => t.classList.remove('is-active'))
    const active = [...tabs].find(t => t.dataset.panel === panelId)
    if (active) active.classList.add('is-active')
    tabContainer.parentElement.querySelectorAll('[id^="dc-panel-"]').forEach(panel => {
      panel.style.display = panel.id === panelId ? '' : 'none'
    })
    if (panelId === auditPanelId) loadAuditPanel()
  }

  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      localStorage.setItem(storageKey, tab.dataset.panel)
      activatePanel(tab.dataset.panel)
    })
  })

  const saved = localStorage.getItem(storageKey)
  if (saved) activatePanel(saved)
}

// DataTables renders the page-length <select> bare; Bulma requires a <div class="select"> wrapper.
export function dtWrapLengthSelect(api) {
  $(api.table().container()).find('div.dt-length select').wrap('<div class="select is-small"></div>')
}

// ipv4SortKey returns a zero-padded form of an IPv4 dotted-decimal address so
// lexicographic sort matches numeric octet order. "10.20.21.41" → "010.020.021.041"
// (sorts before "10.20.21.100" → "010.020.021.100"). Empty/non-IPv4 input passes
// through unchanged so DataTables can still group it consistently.
export function ipv4SortKey(addr) {
  if (!addr) return ''
  const parts = String(addr).split('.')
  if (parts.length !== 4) return String(addr)
  return parts.map((n) => n.padStart(3, '0')).join('.')
}

// dtIPv4Render is a DataTables `render` function (orthogonal data) for IPv4
// columns. Returns the padded sort key for `type === 'sort'`/`'type'` and the
// original string for display/filter. Apply via columns/columnDefs render.
export function dtIPv4Render(data, type) {
  if (type === 'sort' || type === 'type') return ipv4SortKey(data)
  return data == null ? '' : data
}

export function initServerDetailTabs(root) {
  const tabContainer = root.querySelector('[id^="srv-detail-tabs-"]')
  if (!tabContainer) return

  const tabs = tabContainer.querySelectorAll('li[data-panel]')
  const srvId = tabContainer.id.replace('srv-detail-tabs-', '')
  const auditPanelId = `srv-panel-audit-${srvId}`

  // Active panel persists across edit-then-reload cycles. Without this the
  // post-submit htmx.ajax re-renders the fragment with iDRAC active (template
  // default), bouncing the user off the tab they were on (e.g. Audit Log).
  const activeKey = `srv-tab-active:${srvId}`

  function loadAuditPanel() {
    const tab = [...tabs].find(t => t.dataset.panel === auditPanelId)
    if (!tab) return
    // Templates can embed the full subgraph orbId list in data-related-orb-ids
    // so the audit panel pulls events for the parent AND its nested config
    // items (e.g. Server + IdracSettings + StorageControllers) in one call.
    // Falls back to data-orb-id when the related list is missing.
    const related = (tab.dataset.relatedOrbIds || tab.dataset.orbId || '')
      .split(',').map(s => s.trim()).filter(Boolean)
    if (related.length === 0) return
    // Scoped lookup, not document.getElementById — see scope comment below.
    const panel = tabContainer.parentElement.querySelector('#' + CSS.escape(auditPanelId))
    if (!panel) return
    const qs = related.map(id => `orbId=${encodeURIComponent(id)}`).join('&')
    fetch(BASE + `/api/v1/audit-log?${qs}&limit=50`, {
      headers: { 'HX-Request': 'true' },
    })
      .then(r => r.text())
      .then(html => { panel.innerHTML = html; renderTimestamps(panel) })
      .catch(() => {})
  }

  // Pair each tab li with its target panel element up front, scoped to the
  // SAME article container as the tabs. SCOPING IS LOAD-BEARING: the user can
  // have the same server open in TWO contexts simultaneously — once as a
  // standalone server tab (tab-content-srv-X), and once as a drilled-in
  // server from a DC tab (tab-content-{DCId}). Both contexts contain
  // srv-panel-idrac-X et al. with IDENTICAL ids (HTML technically allows
  // duplicate ids; modern browsers honor them with document.getElementById
  // returning the first match). A global document.getElementById would grab
  // the wrong panel and update display on the OTHER tab content — leaving
  // the visible one at the template default (iDRAC active, audit hidden),
  // even though the tab classes here update correctly. Result: tab nav says
  // Audit but content stays iDRAC.
  const scope = tabContainer.parentElement
  const panelPairs = [...tabs].map(t => ({
    tab: t,
    panel: scope.querySelector('#' + CSS.escape(t.dataset.panel)),
  })).filter(p => p.panel)

  function activatePanel(panelId) {
    for (const { tab, panel } of panelPairs) {
      if (tab.dataset.panel === panelId) {
        tab.classList.add('is-active')
        panel.style.removeProperty('display')
      } else {
        tab.classList.remove('is-active')
        panel.style.setProperty('display', 'none')
      }
    }
    if (panelId === auditPanelId) loadAuditPanel()
    sessionStorage.setItem(activeKey, panelId)
  }

  for (const { tab } of panelPairs) {
    tab.addEventListener('click', () => activatePanel(tab.dataset.panel))
  }

  const saved = sessionStorage.getItem(activeKey)
  if (saved && panelPairs.some(p => p.tab.dataset.panel === saved)) {
    activatePanel(saved)
  }
}

// ─── HTMX afterSettle — shared tab init and timestamp rendering ────────────────
//
// We listen on htmx:afterSettle, not htmx:afterSwap. Settle is htmx's phase
// for applying attribute changes ("settling") on elements matched across the
// swap. If our tab-restore logic runs in afterSwap, activatePanel removes the
// audit panel's inline style="display:none", and the settle phase IMMEDIATELY
// re-applies it from the template attribute — visibly hiding the panel even
// though the tab class is set to active. afterSettle runs LAST so our changes
// are authoritative.
document.addEventListener('htmx:afterSettle', (evt) => {
  const target = evt.detail && evt.detail.target
  if (!target) return
  renderTimestamps(target)

  const dcDetailTabs = target.querySelector('[id^="dc-detail-tabs-"]')
  if (dcDetailTabs) {
    const id = dcDetailTabs.id.replace('dc-detail-tabs-', '')
    target.dataset.loaded = 'true'
    initDcDetailTabs(id)
    initServerDetailTabs(target)
    const dcServersTable = target.querySelector('table[id^="dc-servers-table-"]')
    if (dcServersTable && !$.fn.DataTable.isDataTable(dcServersTable)) {
      new DataTable(dcServersTable, {
        paging: false,
        searching: false,
        info: false,
        ordering: true,
        select: { style: 'os' },
        autoWidth: true,
        columnDefs: [
          { className: 'dt-left', targets: 5 },
          { targets: 0, render: dtIPv4Render }, // OOB IP column — numeric sort by octet
        ],
        createdRow: function (row) { row.style.cursor = 'pointer'; row.title = 'Double-click to open' },
      })
    }
    return
  }

  const clusterDetailTabs = target.querySelector('[id^="cluster-detail-tabs-"]')
  if (clusterDetailTabs) {
    const id = clusterDetailTabs.id.replace('cluster-detail-tabs-', '')
    target.dataset.loaded = 'true'
    initClusterDetailTabs(id)
    return
  }

  const srvDetailTabs = target.querySelector('[id^="srv-detail-tabs-"]')
  if (srvDetailTabs) {
    target.dataset.loaded = 'true'
    initServerDetailTabs(target)
    const defaultTabLink = target.querySelector('.detlinks.is-active')
    if (defaultTabLink) {
      openServerTab(defaultTabLink.id.replace(/-detlink$/, '-det'))
    }
    target.querySelectorAll('[id$="-ev"]').forEach(el => {
      const serverId = el.id.split('-')[0]
      setTimeout(() => initServerEventsTable(serverId), 100)
    })
  }
})

// ─── Todo toast ───────────────────────────────────────────────────────────────

document.addEventListener('click', (e) => {
  if (e.target.closest('.todo')) displayTodoToast()
})

// ─── Hint banners ─────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  const banner = document.getElementById('hint-banner-dblclick')
  if (!banner) return
  const KEY = document.getElementById('server-list-table')
    ? 'hint-dblclick-dismissed-srv'
    : 'hint-dblclick-dismissed-dc'
  if (!sessionStorage.getItem(KEY)) banner.style.display = ''
  document.getElementById('hint-banner-dblclick-dismiss').addEventListener('click', () => {
    sessionStorage.setItem(KEY, '1')
    banner.style.display = 'none'
  })
})

// ─── Logout — clear tab state ─────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  const logoutForm = document.querySelector('form[action$="/user/logout"]')
  if (!logoutForm) return
  logoutForm.addEventListener('submit', () => {
    localStorage.clear()
    sessionStorage.clear()
  })
})

// ─── Clipboard copy (delegated) ───────────────────────────────────────────────

document.addEventListener('click', e => {
  const btn = e.target.closest('[data-copy-value]')
  if (!btn) return
  navigator.clipboard.writeText(btn.dataset.copyValue).then(() => {
    btn.innerHTML = '<span class="icon"><i class="fas fa-check"></i></span>'
    setTimeout(() => { btn.innerHTML = '<span class="icon"><i class="fas fa-copy"></i></span>' }, 1200)
  })
})

// ─── Device code SSO poller ───────────────────────────────────────────────────

function initDeviceCodePoller() {
  const el = document.getElementById('device-code-poller')
  if (!el) return

  const deviceCode = el.dataset.deviceCode
  const basePath = el.dataset.basePath || ''
  let interval = (parseInt(el.dataset.interval) || 5) * 1000

  const statusEl = document.getElementById('device-code-status')

  async function poll() {
    try {
      const resp = await fetch(`${basePath}/auth/device/poll`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ device_code: deviceCode }),
      })
      const data = await resp.json()
      if (data.status === 'complete') {
        if (statusEl) statusEl.textContent = 'Authenticated — redirecting…'
        window.location.href = basePath + '/'
        return
      }
      if (data.status === 'expired') {
        if (statusEl) statusEl.innerHTML = 'Code expired — <a href="">try again</a>.'
        return
      }
      if (data.interval) interval = data.interval * 1000
    } catch (_) {}
    setTimeout(poll, interval)
  }

  setTimeout(poll, interval)
}

document.addEventListener('DOMContentLoaded', initDeviceCodePoller)

// ─── Device code copy-to-clipboard ────────────────────────────────────────────
//
// Lets the user one-click copy the device code so they can paste it into
// the Microsoft device-login page in the other tab.

function initDeviceCodeCopy() {
  const btn = document.getElementById('device-code-copy')
  if (!btn) return

  // execCommand('copy') copies whatever is currently selected. Using a
  // hidden <textarea> is the maximally compatible approach: contenteditable
  // selection works in Chrome but flakes in Safari, and selecting a <p>
  // element's text content isn't reliably copyable across browsers.
  function legacyCopy(text) {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    // Position off-screen but still rendered (visibility:hidden / display:none
    // disqualify the textarea from being selected on iOS Safari).
    ta.style.position = 'fixed'
    ta.style.top = '0'
    ta.style.left = '0'
    ta.style.opacity = '0'
    ta.style.pointerEvents = 'none'
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    ta.setSelectionRange(0, text.length)
    let ok = false
    try { ok = document.execCommand('copy') } catch (_) { /* ignore */ }
    document.body.removeChild(ta)
    return ok
  }

  function showSuccess(label, icon, orig) {
    label.textContent = 'Copied!'
    icon.className = 'fa-solid fa-check'
    btn.classList.remove('is-light')
    btn.classList.add('is-success')
    setTimeout(() => {
      label.textContent = orig.label
      icon.className = orig.icon
      btn.classList.remove('is-success')
      btn.classList.add('is-light')
    }, 1500)
  }

  btn.addEventListener('click', () => {
    const text = btn.dataset.copyText || ''
    const label = btn.querySelector('span:last-child')
    const icon = btn.querySelector('i')
    const orig = { label: label.textContent, icon: icon.className }

    // Secure context (HTTPS or localhost): modern API is allowed. Non-secure
    // (plain HTTP on AKS dev): navigator.clipboard.writeText rejects, often
    // asynchronously after user-activation expires — skip it entirely and
    // go straight to the legacy path so execCommand runs inside the click.
    if (window.isSecureContext && navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(text).then(
        () => showSuccess(label, icon, orig),
        () => {
          if (legacyCopy(text)) showSuccess(label, icon, orig)
          else { label.textContent = 'Copy failed'; setTimeout(() => { label.textContent = orig.label }, 2500) }
        },
      )
      return
    }

    if (legacyCopy(text)) {
      showSuccess(label, icon, orig)
    } else {
      label.textContent = 'Copy failed'
      setTimeout(() => { label.textContent = orig.label }, 2500)
    }
  })
}

document.addEventListener('DOMContentLoaded', initDeviceCodeCopy)

// ─── DataCenter / Server tab loading ──────────────────────────────────────────
//
// Shared between orbital and orb. Both apps' /datacenters/:id and /servers/:id
// routes return an HTML fragment when HX-Request: true is sent — we use HTMX
// to load that fragment into the new tab's content div.

export function loadDataCenterTab(displayName, orbId) {
  const domId = safeDomId(orbId)
  const tabHtml = `<li class="tab">
    <a id="tab-${domId}" data-target="tab-content-${domId}" role="tab" aria-selected="false" tabindex="-1">
      ${displayName}
      <span class="pl-2">
        <button id="tab-close-${domId}">
          <i class="fa-solid fa-xmark" style="font-size: 0.8em;"></i>
        </button>
      </span>
    </a>
  </li>`
  const contentHtml = `<div class="tab-content" id="tab-content-${domId}" role="tabpanel" style="display:none"></div>`

  $('#tablist').append(tabHtml)
  $('.app-main').append(contentHtml)

  const tabLink = document.getElementById(`tab-${domId}`)
  const tabContent = document.getElementById(`tab-content-${domId}`)

  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(`tab-content-${domId}`)
    setCurrentTab(`tab-${domId}`)
    if (!tabContent.dataset.loaded) {
      htmx.ajax('GET', BASE + '/datacenters/' + encodeURIComponent(orbId), { target: tabContent, swap: 'innerHTML' })
    }
  })

  document.getElementById(`tab-close-${domId}`).addEventListener('click', (event) => {
    event.stopPropagation()
    localStorage.removeItem(`dc-detail-tab-${domId}`)
    unloadTab(orbId)
    deleteTab(displayName, orbId)
    document.getElementById('tab-summary').click()
    replaceCurrentTab(`tab-${domId}`, 'tab-summary')
  })
}

export function saveServerTab(displayName, id) {
  const item = JSON.stringify(new TabItem(displayName, id))
  const s = new Set(localStorage.serverTabs ? JSON.parse(localStorage.serverTabs) : [])
  s.add(item)
  localStorage.serverTabs = JSON.stringify([...s])
}

export function deleteServerTab(displayName, id) {
  const item = JSON.stringify(new TabItem(displayName, id))
  const s = new Set(localStorage.serverTabs ? JSON.parse(localStorage.serverTabs) : [])
  s.delete(item)
  localStorage.serverTabs = JSON.stringify([...s])
}

export function loadServerListTab(displayName, orbId) {
  const domId = safeDomId(orbId)
  const tabHtml = `<li class="tab">
    <a id="tab-srv-${domId}" data-target="tab-content-srv-${domId}" role="tab" aria-selected="false" tabindex="-1">
      ${displayName}
      <span class="pl-2">
        <button id="tab-close-srv-${domId}">
          <i class="fa-solid fa-xmark" style="font-size: 0.8em;"></i>
        </button>
      </span>
    </a>
  </li>`
  const contentHtml = `<div class="tab-content" id="tab-content-srv-${domId}" role="tabpanel" style="display:none"></div>`

  $('#tablist').append(tabHtml)
  $('.app-main').append(contentHtml)

  const tabLink = document.getElementById(`tab-srv-${domId}`)
  const tabContent = document.getElementById(`tab-content-srv-${domId}`)

  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(`tab-content-srv-${domId}`)
    setCurrentTab(`tab-srv-${domId}`)
    if (!tabContent.dataset.loaded) {
      htmx.ajax('GET', BASE + '/servers/' + encodeURIComponent(orbId), { target: tabContent, swap: 'innerHTML' })
    }
  })

  document.getElementById(`tab-close-srv-${domId}`).addEventListener('click', (event) => {
    event.stopPropagation()
    deleteServerTab(displayName, orbId)
    replaceCurrentTab(`tab-srv-${domId}`, 'tab-summary')
    tabLink.parentElement.remove()
    tabContent.remove()
    document.getElementById('tab-summary').click()
  })
}

// clearTabStateOnFresh wipes all per-user tab/UI state (open DC tabs, open
// server tabs, last-active tab id, …) when the URL has ?fresh=1 — set by the
// login redirect (see internal/handler/login.go + oidc.go). Login is a hard
// boundary; one user should not inherit another user's tabs from a previous
// session on the same machine.
//
// MUST run on every page load (not just pages that host the DC or server
// tables) — the login redirect lands on `/` (home), which only has the
// inventory table. If this were gated on table presence, the user would
// land on home, the ?fresh=1 would be ignored, then they'd navigate to
// /servers and find stale tabs.
function clearTabStateOnFresh() {
  if (new URLSearchParams(window.location.search).get('fresh') !== '1') return
  localStorage.removeItem('datacenterTabs')
  localStorage.removeItem('serverTabs')
  localStorage.removeItem('tabCurrent')
  history.replaceState(null, '', window.location.pathname)
}

// Run once at module load — covers home, /servers, /datacenters, and any
// future page. Idempotent: re-runs are no-ops once the query param is stripped.
clearTabStateOnFresh()

// initDatacenterTabRestoration restores DC tabs from localStorage on page load.
// Call on window.load on pages that have #datacenter-table.
export function initDatacenterTabRestoration() {
  if (!document.getElementById('datacenter-table')) return

  clearTabStateOnFresh()

  if (!localStorage.datacenterTabs) return
  const tabSet = new Set(JSON.parse(localStorage.datacenterTabs))
  tabSet.forEach(tabData => {
    const { displayName, id } = JSON.parse(tabData)
    loadDataCenterTab(displayName, id)
  })
  const currentTabId = getCurrentTab()
  if (currentTabId) document.getElementById(currentTabId)?.click()
}

// initServerListTabRestoration restores server tabs from localStorage and
// handles ?open=<id>&label=<label> deep-links from the DC detail panel.
// Call on window.load on pages that have #server-list-table.
export function initServerListTabRestoration() {
  if (!document.getElementById('server-list-table')) return

  clearTabStateOnFresh()

  if (localStorage.serverTabs) {
    const tabSet = new Set(JSON.parse(localStorage.serverTabs))
    tabSet.forEach(tabData => {
      const { displayName, id } = JSON.parse(tabData)
      loadServerListTab(displayName, id)
    })
  }

  const params = new URLSearchParams(window.location.search)
  const openId = params.get('open')
  const openLabel = params.get('label')
  if (openId) {
    const displayName = openLabel || openId
    const openDomId = safeDomId(openId)
    if (!document.getElementById(`tab-srv-${openDomId}`)) {
      loadServerListTab(displayName, openId)
      saveServerTab(displayName, openId)
    }
    document.getElementById(`tab-srv-${openDomId}`)?.click()
    history.replaceState(null, '', BASE + '/servers')
    return
  }

  const currentTabId = getCurrentTab()
  if (currentTabId) document.getElementById(currentTabId)?.click()
}

// ─── Inventory / DataCenter / Server list tables ──────────────────────────────
//
// These three DataTables are shared between orbital and orb. The core
// DataTable construction, columns, ajax, and dataSrc are identical because
// both apps render the same DGraph schema. App-specific behaviors (row
// double-click action, currently: orbital opens a tab, orb navigates to a
// detail page) are injected via the `opts` callbacks.

export const INVENTORY_CACHE_KEY = 'inventoryCache'

export function inventoryFetch(onData) {
  const query = `query LoadInventory { queryConfigItem { id __typename orbId name createdBy createdAt } }`
  fetch(BASE + '/graphql', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ query }),
  })
    .then(r => r.json())
    .then(json => {
      if (!gqlSurfaceErrors(json, 'Load inventory')) { onData([]); return }
      const items = (json.data?.queryConfigItem ?? []).map(it => ({
        uid: it.id,
        type: it.__typename,
        orbId: it.orbId,
        name: it.name ?? '',
        createdBy: it.createdBy ?? '',
        createdAt: it.createdAt ?? '',
      }))
      sessionStorage.setItem(INVENTORY_CACHE_KEY, JSON.stringify(items))
      onData(items)
    })
}

// initInventoryTable initializes the inventory DataTable on pages that have
// #inventory-table. Pure read-only display — no app-specific hooks needed.
export function initInventoryTable() {
  if (!document.getElementById('inventory-table')) return

  const savedType = localStorage.getItem('inventoryTypeFilter') || ''
  const savedNamespace = localStorage.getItem('inventoryNamespaceFilter') || ''
  const cached = sessionStorage.getItem(INVENTORY_CACHE_KEY)
  const initialData = cached ? JSON.parse(cached) : []

  const typeFilterEl = $('<div class="select is-small" style="margin-right:0.25rem"><select id="inventory-type-select"><option value="">All Types</option></select></div>')

  const inventoryTable = new DataTable('#inventory-table', {
    layout: {
      topStart: [
        typeFilterEl,
        { buttons: [
          { extend: 'excel', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-excel"></i><span>Excel</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Excel', title: '', filename: 'config-items' },
          { extend: 'csv', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-text"></i><span>CSV</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'CSV', title: '', filename: 'config-items' },
          { extend: 'copy', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-copy"></i><span>Copy</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Copy' },
          { extend: 'colvis', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa fa-columns"></i><span>Select</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Select Columns' },
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-inventory' }, action: function () { reloadInventory() } },
        ] },
        { pageLength: { menu: [100, 250, 500] } },
      ],
      topEnd: { search: { placeholder: 'Search inventory' } },
    },
    select: { style: 'os' },
    autoWidth: true,
    scrollX: true,
    scrollY: 400,
    scrollCollapse: true,
    pageLength: 250,
    stateSave: true,
    language: {
      infoEmpty: 'No config items to show',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'items', 1: 'item' },
    },
    searchCols: [savedType ? { search: savedType } : null, null, null, null, null, null],
    initComplete: function () {
      dtWrapLengthSelect(this.api())

      document.getElementById('inventory-type-select').addEventListener('change', function () {
        localStorage.setItem('inventoryTypeFilter', this.value)
        inventoryTable.column(0).search(this.value, { exact: !!this.value }).draw()
      })

      const nsSelect = document.getElementById('inventory-namespace-select')
      if (nsSelect) {
        nsSelect.addEventListener('change', function () {
          localStorage.setItem('inventoryNamespaceFilter', this.value)
          applyNamespaceFilter(this.value)
        })
      }

      if (savedNamespace) applyNamespaceFilter(savedNamespace)
    },
    columns: [
      { data: 'type' },
      { data: 'orbId' },
      { data: 'name' },
      { data: 'createdBy' },
      { data: 'createdAt', render: (val) => val ? val.replace('T', ' ').replace('Z', '') : '' },
      { data: 'uid' },
    ],
    columnDefs: [
      { targets: 0, width: '10%' },
      { targets: 1, width: '20%' },
      { targets: 2, width: '20%' },
      { targets: 3, width: '15%' },
      { targets: 4, width: '15%', className: 'dt-left' },
      { targets: 5, visible: false },
    ],
    data: initialData,
  })

  function applyNamespaceFilter(ns) {
    inventoryTable.column(1).search(ns ? '^' + ns + ':' : '', { regex: true }).draw()
  }

  function populateDropdowns() {
    const typeSelect = document.getElementById('inventory-type-select')
    typeSelect.options.length = 1
    inventoryTable.column(0).data().unique().sort().each(type => {
      typeSelect.add(new Option(type, type))
    })
    if (savedType) typeSelect.value = savedType

    const nsSelect = document.getElementById('inventory-namespace-select')
    if (!nsSelect) return
    nsSelect.options.length = 1
    const seen = new Set()
    inventoryTable.column(1).data().each(orbId => {
      const ns = orbId ? orbId.split(':')[0] : ''
      if (ns && !seen.has(ns)) seen.add(ns)
    })
    Array.from(seen).sort().forEach(ns => nsSelect.add(new Option(ns, ns)))
    if (savedNamespace) nsSelect.value = savedNamespace
  }

  function revalidate(onDone) {
    inventoryFetch(items => {
      inventoryTable.clear().rows.add(items).draw()
      populateDropdowns()
      const nsSelect = document.getElementById('inventory-namespace-select')
      const currentNs = nsSelect ? nsSelect.value : savedNamespace
      if (currentNs) applyNamespaceFilter(currentNs)
      if (onDone) onDone()
    })
  }

  if (!cached) {
    revalidate()
  } else {
    populateDropdowns()
    if (savedNamespace) applyNamespaceFilter(savedNamespace)
  }

  // Refetch when the user returns focus — covers the "switch to terminal,
  // run `make seed`, switch back" case without firing on every in-app navigation.
  // `visibilitychange` covers tab switching / minimize; `focus` covers app
  // switching when both windows stay on screen (e.g. browser + terminal side-by-side).
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') revalidate()
  })
  window.addEventListener('focus', () => revalidate())

  function reloadInventory() {
    sessionStorage.removeItem(INVENTORY_CACHE_KEY)
    inventoryTable.clear().draw()
    const btn = document.getElementById('btn-reload-inventory')
    if (btn) btn.classList.add('is-loading')
    const minDelay = new Promise(r => setTimeout(r, 500))
    new Promise(resolve => inventoryFetch(items => resolve(items)))
      .then(items => minDelay.then(() => items))
      .then(items => {
        inventoryTable.clear().rows.add(items).draw()
        populateDropdowns()
        const nsSelect = document.getElementById('inventory-namespace-select')
        const currentNs = nsSelect ? nsSelect.value : savedNamespace
        if (currentNs) applyNamespaceFilter(currentNs)
        if (btn) btn.classList.remove('is-loading')
      })
  }

  return inventoryTable
}

// initDatacenterTable initializes #datacenter-table. opts.onRowOpen is called
// on row double-click with the row's data object; the app supplies what
// "open" means (orbital: open tab; orb: navigate to detail page).
export function initDatacenterTable(opts = {}) {
  if (!document.getElementById('datacenter-table')) return

  document.querySelectorAll('li.tab a[data-target]').forEach((a) => {
    a.addEventListener('click', () => {
      activateTab(a.parentElement)
      displayTabContent(a.dataset.target)
      setCurrentTab(a.id)
    })
  })

  const datacenterTable = new DataTable('#datacenter-table', {
    layout: {
      topStart: [
        { pageLength: { menu: [5, 10, 25, 50] } },
        { buttons: [
          { extend: 'copy', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-copy"></i><span>Copy</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Copy' },
          { extend: 'colvis', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa fa-columns"></i><span>Select</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Select Columns' },
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-datacenters' } },
        ] },
      ],
      topEnd: { search: { placeholder: 'Type search here' } },
    },
    select: { style: 'os' },
    autoWidth: true,
    scrollX: true,
    scrollY: 400,
    scrollCollapse: true,
    stateSave: true,
    language: {
      infoEmpty: 'No data centers to show',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'data centers', 1: 'data center' },
    },
    initComplete: function () { dtWrapLengthSelect(this.api()) },
    createdRow: function (row) { row.style.cursor = 'pointer'; row.title = 'Double-click to open' },
    columns: [
      { data: 'name' },
      { data: 'serverCount' },
      { data: 'createdBy' },
      { data: 'createdAt' },
      { data: 'id' },
      { data: 'orbId' },
    ],
    columnDefs: [
      { targets: 0 },
      { targets: 1, className: 'dt-body-left dt-head-left' },
      { targets: 2 },
      { targets: 3 },
      { targets: [4, 5], visible: false, searchable: true },
    ],
    ajax: {
      url: BASE + '/graphql',
      type: 'POST',
      contentType: 'application/json',
      data: () => JSON.stringify({ query: `query LoadDataCenters { queryDataCenter { id orbId name createdBy createdAt serversAggregate { count } } }` }),
      dataSrc: (json) => {
        if (!gqlSurfaceErrors(json, 'Load data centers')) return []
        return (json.data?.queryDataCenter ?? []).map(dc => ({
          id: dc.id,
          orbId: dc.orbId ?? '—',
          name: dc.name,
          createdBy: dc.createdBy ?? '',
          createdAt: dc.createdAt ?? '',
          serverCount: dc.serversAggregate?.count ?? 0,
        }))
      },
    },
  })

  const reloadButton = datacenterTable.button('reload:name').node()
  datacenterTable.button('reload:name').node().on('click', function () {
    datacenterTable.clear().draw()
    reloadButton.addClass('is-loading')
    setTimeout(() => {
      datacenterTable.ajax.reload(() => { reloadButton.removeClass('is-loading') })
    }, 250)
  })

  if (typeof opts.onRowOpen === 'function') {
    $('#datacenter-table tbody').on('dblclick', 'tr', function () {
      const data = datacenterTable.row(this).data()
      if (!data) return
      opts.onRowOpen(data, this)
    })
  }

  return datacenterTable
}

// initServerListTable initializes #server-list-table. opts.onRowOpen is
// called on row double-click with the row's data object.
export function initServerListTable(opts = {}) {
  if (!document.getElementById('server-list-table')) return

  document.querySelectorAll('li.tab a[data-target]').forEach((a) => {
    a.addEventListener('click', () => {
      activateTab(a.parentElement)
      displayTabContent(a.dataset.target)
      setCurrentTab(a.id)
    })
  })

  const dcFilterEl = $('<div class="select is-small" style="margin-right:0.25rem"><select id="server-dc-select"><option value="">All Data Centers</option></select></div>')

  const serverListTable = new DataTable('#server-list-table', {
    pageLength: 50,
    layout: {
      topStart: [
        dcFilterEl,
        { pageLength: { menu: [25, 50, 100, 250] } },
        { buttons: [
          { extend: 'copy', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-copy"></i><span>Copy</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Copy' },
          { extend: 'colvis', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa fa-columns"></i><span>Select</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Select Columns' },
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-servers' } },
        ] },
      ],
      topEnd: { search: { placeholder: 'Search servers' } },
    },
    select: { style: 'os' },
    autoWidth: true,
    scrollX: true,
    scrollY: 'calc(100vh - 340px)',
    scrollCollapse: true,
    stateSave: true,
    language: {
      infoEmpty: 'No servers to show',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'servers', 1: 'server' },
    },
    initComplete: function () {
      dtWrapLengthSelect(this.api())

      const dcCol = this.api().column(0)
      dcCol.data().unique().sort().each(function (dc) {
        document.getElementById('server-dc-select').add(new Option(dc, dc))
      })
      const saved = localStorage.getItem('server-dc-filter')
      if (saved) {
        const el = document.getElementById('server-dc-select')
        el.value = saved
        dcCol.search(saved, { exact: true }).draw()
      }
      document.getElementById('server-dc-select').addEventListener('change', function () {
        if (this.value) {
          localStorage.setItem('server-dc-filter', this.value)
        } else {
          localStorage.removeItem('server-dc-filter')
        }
        dcCol.search(this.value, { exact: !!this.value }).draw()
      })
    },
    columns: [
      { data: 'dataCenter' },
      { data: 'oobIP', render: dtIPv4Render },
      { data: 'hostname' },
      { data: 'serviceTag' },
      { data: 'model' },
      { data: 'rack' },
      { data: 'id' },
      { data: 'orbId' },
    ],
    columnDefs: [{ targets: [6, 7], visible: false, searchable: true }],
    ajax: {
      url: BASE + '/graphql',
      type: 'POST',
      contentType: 'application/json',
      data: () => JSON.stringify({
        query: `query LoadServers { queryServer {
          id orbId hostname serviceTag model
          oobIP { address }
          rack { name }
          dataCenter { name }
        } }`,
      }),
      dataSrc: (json) => {
        if (!gqlSurfaceErrors(json, 'Load servers')) return []
        return (json.data?.queryServer ?? []).map(s => ({
          id: s.id,
          orbId: s.orbId ?? '—',
          hostname: s.hostname ?? '—',
          serviceTag: s.serviceTag ?? '—',
          model: s.model ?? '—',
          oobIP: s.oobIP?.address ?? '—',
          rack: s.rack?.name ?? '—',
          dataCenter: s.dataCenter?.name ?? '—',
        }))
      },
    },
    createdRow: function (row) { row.style.cursor = 'pointer'; row.title = 'Double-click to open' },
  })

  const reloadButton = serverListTable.button('reload:name').node()
  serverListTable.button('reload:name').node().on('click', function () {
    serverListTable.clear().draw()
    reloadButton.addClass('is-loading')
    setTimeout(() => {
      serverListTable.ajax.reload(() => { reloadButton.removeClass('is-loading') })
    }, 250)
  })

  if (typeof opts.onRowOpen === 'function') {
    $('#server-list-table tbody').on('dblclick', 'tr', function () {
      const data = serverListTable.row(this).data()
      if (!data) return
      opts.onRowOpen(data, this)
    })
  }

  return serverListTable
}

// initClusterTable initializes #cluster-table. opts.onRowOpen is called on
// row double-click with the row's data object. Query uses the polymorphic
// queryKubernetesCluster interface so future provider types appear without
// JS changes (add a fragment to fetch the type-specific columns).
export function initClusterTable(opts = {}) {
  if (!document.getElementById('cluster-table')) return

  document.querySelectorAll('li.tab a[data-target]').forEach((a) => {
    a.addEventListener('click', () => {
      activateTab(a.parentElement)
      displayTabContent(a.dataset.target)
      setCurrentTab(a.id)
    })
  })

  // Same DC filter pattern as initServerListTable — populated from column 1
  // (Data Center; column 0 is the expand toggle) on initComplete and persisted
  // in localStorage.
  const dcFilterEl = $('<div class="select is-small" style="margin-right:0.25rem"><select id="cluster-dc-select"><option value="">All Data Centers</option></select></div>')

  // Builds the HTML for the workload child rows shown under an expanded
  // management cluster. Returned as a string of <tr> elements; DataTables
  // jQuery-parses multiple TRs and inserts each as a child row that aligns
  // with the parent's columns.
  const escapeHtml = (s) => String(s ?? '').replace(/[&<>"']/g, m => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[m]))
  // Expand every parent row that has workload children. Called from
  // initComplete (first load) and from the reload-button callback (every
  // subsequent AJAX refresh — DataTables drops attached child rows when the
  // underlying data is replaced, but the chevron column re-renders from
  // row.children, so without re-expansion the chevron stays ▼ while the
  // children disappear).
  //
  // Idempotent: if a child is already shown (e.g. on sort/filter/page-change
  // where DataTables preserves child state), this no-ops for that row.
  const expandAllClusterChildren = (api) => {
    api.rows().every(function () {
      const data = this.data()
      if (data.children && data.children.length > 0 && !this.child.isShown()) {
        this.child($(buildClusterChildTrs(data.children))).show()
        $(this.node()).addClass('shown')
      }
    })
  }

  // Child rows mirror the parent column layout. Each cell sits under its
  // corresponding parent column — alignment must match what DataTables applies
  // to the parent row (see columnDefs above). Nodes is left-aligned per the
  // dt-left columnDefs entry.
  //
  // .trim() on each TR is load-bearing: jQuery parses the joined string, and
  // whitespace between TRs becomes text nodes that DataTables would render as
  // blank child rows. Trim removes the surrounding whitespace; .join('') with
  // no separator keeps the TRs adjacent so the parser sees only elements.
  const buildClusterChildTrs = (children) => children.map(c => `
    <tr class="cluster-child-row" data-cluster-orb-id="${escapeHtml(c.orbId)}" data-display-name="${escapeHtml(c.name)}" style="cursor:pointer; background:#fafafa" title="Double-click to open">
      <td></td>
      <td><span class="has-text-grey mr-1">└</span>${escapeHtml(c.name)}</td>
      <td>${escapeHtml(c.dataCenter)}</td>
      <td>${escapeHtml(c.provider)}</td>
      <td>${escapeHtml(c.clusterType)}</td>
      <td>${escapeHtml(c.kubernetesVersion)}</td>
      <td>${escapeHtml(c.environment)}</td>
      <td>${escapeHtml(c.cni)}</td>
      <td>${escapeHtml(c.nodes)}</td>
    </tr>
  `.trim()).join('')

  const clusterTable = new DataTable('#cluster-table', {
    pageLength: 25,
    layout: {
      topStart: [
        dcFilterEl,
        { pageLength: { menu: [10, 25, 50] } },
        { buttons: [
          { extend: 'copy', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-copy"></i><span>Copy</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Copy' },
          { extend: 'colvis', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa fa-columns"></i><span>Select</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Select Columns' },
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-clusters' } },
        ] },
      ],
      topEnd: { search: { placeholder: 'Search clusters' } },
    },
    select: { style: 'os' },
    autoWidth: true,
    scrollX: true,
    scrollY: 'calc(100vh - 340px)',
    scrollCollapse: true,
    // stateSave intentionally OFF while the cluster table column layout is
    // still in flux — leaving it on caused header/body to misalign whenever
    // the column count changed (e.g. adding the chevron column at index 0).
    // Re-enable once the columns are stable.
    stateSave: false,
    // No initial sort: the data array is already pre-sorted in tree order
    // (managements first, their workloads as child rows below). Without
    // `order: []`, DataTables defaults to [[0,'asc']] and stamps a sort
    // indicator on the (unorderable) chevron column header.
    order: [],
    language: {
      infoEmpty: 'No clusters to show',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'clusters', 1: 'cluster' },
    },
    initComplete: function () {
      dtWrapLengthSelect(this.api())

      // DC filter targets column 2 (Name is now at column 1, DC moved to 2
      // after the column swap).
      const dcCol = this.api().column(2)
      dcCol.data().unique().sort().each(function (dc) {
        document.getElementById('cluster-dc-select').add(new Option(dc, dc))
      })
      const saved = localStorage.getItem('cluster-dc-filter')
      if (saved) {
        const el = document.getElementById('cluster-dc-select')
        el.value = saved
        dcCol.search(saved, { exact: true }).draw()
      }
      document.getElementById('cluster-dc-select').addEventListener('change', function () {
        if (this.value) {
          localStorage.setItem('cluster-dc-filter', this.value)
        } else {
          localStorage.removeItem('cluster-dc-filter')
        }
        dcCol.search(this.value, { exact: !!this.value }).draw()
      })

      expandAllClusterChildren(this.api())
    },
    columns: [
      {
        // Expand toggle column — only renders the icon for rows that have
        // workload children (managements with workloads). Empty for workloads
        // (they're already nested) and childless management/standalone rows.
        data: null,
        orderable: false,
        searchable: false,
        className: 'cluster-toggle-cell',
        defaultContent: '',
        width: '1%',
        render: (data, type, row) => {
          if (type !== 'display') return ''
          if (!row.children || row.children.length === 0) return ''
          return '<span class="cluster-toggle">▼</span>'
        },
      },
      {
        data: 'name',
        // Orthogonal data: include child workload names in the search text so
        // typing a workload name surfaces its (expanded) parent.
        render: (data, type, row) => {
          if (type === 'filter') return data + ' ' + (row.workloadSearchText || '')
          return data
        },
      },
      { data: 'dataCenter' },
      { data: 'provider' },
      { data: 'clusterType' },
      { data: 'kubernetesVersion' },
      { data: 'environment' },
      { data: 'cni' },
      { data: 'nodes' },
      { data: 'id' },
      { data: 'orbId' },
    ],
    // Column index map (after the toggle column at 0):
    //   0 toggle  1 Name  2 Data Center  3 Provider  4 Cluster Type
    //   5 K8s Version  6 Environment  7 CNI  8 Nodes  9 ID  10 Orb ID
    // Tweak any column's width by adding/updating an entry below.
    columnDefs: [
      { targets: [9, 10], visible: false, searchable: true },
      { targets: 1, width: '20%' },    // name
      { targets: 2, width: '12%' },    // data center
      { targets: 3, width: '8%' },     // provider
      { targets: 4, width: '10%' },    // cluster type
      { targets: 5, width: '100px' },
      { targets: 6, width: '100px' },  // Environment — short values (dev/stage/prod)
      { targets: 7, width: '8%' },     // CNI 
      { targets: 8, className: 'dt-left dt-head-left' },  // Nodes — left-align to match other columns
    ],
    ajax: {
      url: BASE + '/graphql',
      type: 'POST',
      contentType: 'application/json',
      data: () => JSON.stringify({
        query: `query LoadClusters {
          queryKubernetesCluster {
            __typename
            kubernetesVersion
            cni
            environment
            provider
            dataCenter { name }
            nodesAggregate { count }
            ... on ConfigItem { id orbId name }
            ... on EksaKubernetesCluster {
              clusterType
              managementCluster { orbId }
            }
          }
        }`,
      }),
      dataSrc: (json) => {
        if (!gqlSurfaceErrors(json, 'Load clusters')) return []
        const mapRow = (c) => ({
          id: c.id,
          orbId: c.orbId ?? '—',
          name: c.name ?? '—',
          provider: c.provider ?? '—',
          clusterType: c.clusterType ?? '—',
          kubernetesVersion: c.kubernetesVersion ?? '—',
          environment: c.environment ?? '—',
          cni: c.cni ?? '—',
          dataCenter: c.dataCenter?.name ?? '—',
          nodes: c.nodesAggregate?.count ?? 0,
          managementOrbId: c.managementCluster?.orbId || null,
        })

        const mapped = (json.data?.queryKubernetesCluster ?? []).map(mapRow)

        // Bucket workloads by their parent orbId; everything else (management,
        // standalone, orphaned workloads with no parent) goes to the top.
        const byParent = {}
        const tops = []
        for (const r of mapped) {
          if (r.managementOrbId) {
            byParent[r.managementOrbId] = byParent[r.managementOrbId] || []
            byParent[r.managementOrbId].push(r)
          } else {
            tops.push({ ...r, children: [] })
          }
        }
        // Workloads whose parent isn't in the result set — surface at top level
        // rather than dropping them silently.
        for (const r of mapped) {
          if (r.managementOrbId && !tops.find(t => t.orbId === r.managementOrbId)) {
            // Orphan: parent not in this query result. Show as top-level row.
            if (!tops.find(t => t.orbId === r.orbId)) {
              tops.push({ ...r, children: [] })
            }
          }
        }
        for (const t of tops) {
          t.children = byParent[t.orbId] || []
          t.workloadSearchText = t.children.map(c => c.name).join(' ')
        }
        return tops
      },
    },
    createdRow: function (row) { row.style.cursor = 'pointer'; row.title = 'Double-click to open' },
  })

  const reloadButton = clusterTable.button('reload:name').node()
  clusterTable.button('reload:name').node().on('click', function () {
    clusterTable.clear().draw()
    reloadButton.addClass('is-loading')
    setTimeout(() => {
      clusterTable.ajax.reload(() => {
        reloadButton.removeClass('is-loading')
        // Re-expand parent rows — AJAX reload drops the child rows that were
        // attached before the refresh.
        expandAllClusterChildren(clusterTable)
      })
    }, 250)
  })

  // Toggle expand/collapse of workload children on click of the icon cell.
  $('#cluster-table tbody').on('click', 'td.cluster-toggle-cell', function (e) {
    e.stopPropagation()
    const tr = $(this).closest('tr')
    const row = clusterTable.row(tr)
    const data = row.data()
    if (!data || !data.children || data.children.length === 0) return
    if (row.child.isShown()) {
      row.child.hide()
      tr.removeClass('shown')
      $(this).find('.cluster-toggle').text('▶')
    } else {
      row.child($(buildClusterChildTrs(data.children))).show()
      tr.addClass('shown')
      $(this).find('.cluster-toggle').text('▼')
    }
  })

  if (typeof opts.onRowOpen === 'function') {
    // Double-click on a parent row → open via onRowOpen. Skip:
    //   1. Child rows (cluster-child-row class) — handled by orbital.js's
    //      data-cluster-orb-id global listener via /clusters?open=.
    //   2. Toggle-cell targets — fast double-clicks on the chevron would
    //      otherwise toggle twice AND open the row.
    $('#cluster-table tbody').on('dblclick', 'tr', function (e) {
      if (this.classList.contains('cluster-child-row')) return
      if ($(e.target).closest('td').hasClass('cluster-toggle-cell')) return
      const data = clusterTable.row(this).data()
      if (!data) return
      opts.onRowOpen(data, this)
    })
  }

  return clusterTable
}

// loadClusterTab opens a cluster detail tab via HTMX swap into a new tab content
// pane. Mirrors loadDataCenterTab / loadServerListTab structure.
export function loadClusterTab(displayName, orbId) {
  const domId = safeDomId(orbId)
  const tabHtml = `<li class="tab">
    <a id="tab-cluster-${domId}" data-target="tab-content-cluster-${domId}" role="tab" aria-selected="false" tabindex="-1">
      ${displayName}
      <span class="pl-2">
        <button id="tab-close-cluster-${domId}">
          <i class="fa-solid fa-xmark" style="font-size: 0.8em;"></i>
        </button>
      </span>
    </a>
  </li>`
  const contentHtml = `<div class="tab-content" id="tab-content-cluster-${domId}" role="tabpanel" style="display:none"></div>`

  $('#tablist').append(tabHtml)
  $('.app-main').append(contentHtml)

  const tabLink = document.getElementById(`tab-cluster-${domId}`)
  const tabContent = document.getElementById(`tab-content-cluster-${domId}`)

  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(`tab-content-cluster-${domId}`)
    setCurrentTab(`tab-cluster-${domId}`)
    if (!tabContent.dataset.loaded) {
      htmx.ajax('GET', BASE + '/clusters/' + encodeURIComponent(orbId), { target: tabContent, swap: 'innerHTML' })
    }
  })

  document.getElementById(`tab-close-cluster-${domId}`).addEventListener('click', (event) => {
    event.stopPropagation()
    deleteClusterTab(displayName, orbId)
    replaceCurrentTab(`tab-cluster-${domId}`, 'tab-summary')
    tabLink.parentElement.remove()
    tabContent.remove()
    document.getElementById('tab-summary').click()
  })
}

export function saveClusterTab(displayName, orbId) {
  const item = JSON.stringify(new TabItem(displayName, orbId))
  const s = new Set(localStorage.clusterTabs ? JSON.parse(localStorage.clusterTabs) : [])
  s.add(item)
  localStorage.clusterTabs = JSON.stringify([...s])
}

export function deleteClusterTab(displayName, orbId) {
  const item = JSON.stringify(new TabItem(displayName, orbId))
  const s = new Set(localStorage.clusterTabs ? JSON.parse(localStorage.clusterTabs) : [])
  s.delete(item)
  localStorage.clusterTabs = JSON.stringify([...s])
}

// initClusterTabRestoration restores cluster tabs from localStorage on page
// load AND handles ?open=<orbId>&label=<name> deep-links from cross-cluster
// navigation (workload's "Management Cluster" link, "Workload Clusters" sub-tab
// rows). Call on window.load on pages that have #cluster-table.
export function initClusterTabRestoration() {
  if (!document.getElementById('cluster-table')) return

  clearTabStateOnFresh()

  if (localStorage.clusterTabs) {
    const tabSet = new Set(JSON.parse(localStorage.clusterTabs))
    tabSet.forEach(tabData => {
      const { displayName, id } = JSON.parse(tabData)
      loadClusterTab(displayName, id)
    })
  }

  const params = new URLSearchParams(window.location.search)
  const openId = params.get('open')
  const openLabel = params.get('label')
  if (openId) {
    const displayName = openLabel || openId
    const openDomId = safeDomId(openId)
    if (!document.getElementById(`tab-cluster-${openDomId}`)) {
      loadClusterTab(displayName, openId)
      saveClusterTab(displayName, openId)
    }
    document.getElementById(`tab-cluster-${openDomId}`)?.click()
    history.replaceState(null, '', BASE + '/clusters')
    return
  }

  const currentTabId = getCurrentTab()
  if (currentTabId) document.getElementById(currentTabId)?.click()
}

// Detail-tab sub-tabs (Nodes / Audit Log) inside a cluster tab. Same shape as
// initDcDetailTabs but scoped to `cluster-detail-tabs-…` / `cluster-panel-…`
// element IDs.
export function initClusterDetailTabs(domId) {
  const tabContainer = document.getElementById(`cluster-detail-tabs-${domId}`)
  if (!tabContainer) return

  const tabs = tabContainer.querySelectorAll('li[data-panel]')
  const storageKey = `cluster-detail-tab-${domId}`
  const auditPanelId = `cluster-panel-audit-${domId}`

  function loadAuditPanel() {
    const tab = [...tabs].find(t => t.dataset.panel === auditPanelId)
    if (!tab) return
    // Templates can embed the full subgraph orbId list in data-related-orb-ids
    // so the audit panel pulls events for the cluster AND its nested ConfigItems
    // (nodes, backup wrapper, etcd/velero/s3sync) in one call. Falls back to
    // data-orb-id when the related list is missing. Matches the DC + Server
    // audit-tab pattern.
    const related = (tab.dataset.relatedOrbIds || tab.dataset.orbId || '')
      .split(',').map(s => s.trim()).filter(Boolean)
    if (related.length === 0) return
    const panel = document.getElementById(auditPanelId)
    if (!panel) return
    const qs = related.map(id => `orbId=${encodeURIComponent(id)}`).join('&')
    fetch(BASE + `/api/v1/audit-log?${qs}&limit=50`, {
      headers: { 'HX-Request': 'true' },
    })
      .then(r => r.text())
      .then(html => { panel.innerHTML = html; renderTimestamps(panel) })
      .catch(() => {})
  }

  function activatePanel(panelId) {
    tabs.forEach(t => t.classList.remove('is-active'))
    const active = [...tabs].find(t => t.dataset.panel === panelId)
    if (active) active.classList.add('is-active')
    tabContainer.parentElement.querySelectorAll('[id^="cluster-panel-"]').forEach(panel => {
      panel.style.display = panel.id === panelId ? '' : 'none'
    })
    if (panelId === auditPanelId) loadAuditPanel()
  }

  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      localStorage.setItem(storageKey, tab.dataset.panel)
      activatePanel(tab.dataset.panel)
    })
  })

  const saved = localStorage.getItem(storageKey)
  if (saved) activatePanel(saved)
}

