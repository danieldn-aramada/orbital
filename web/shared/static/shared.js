// shared.js — utilities used by both orbital and orb

export const BASE = window.ORBITAL_BASE || ''

// ─── Tab management ───────────────────────────────────────────────────────────

export class TabItem {
  constructor(displayName, id) {
    this.displayName = displayName
    this.id = id
  }
}

export function unloadTab(itemId) {
  $(`#tab-${itemId}`).parent().remove()
  $(`#tab-content-${itemId}`).remove()
}

export function loadTab(displayName, itemId) {
  const html = `<li class="tab">
    <a id="tab-${itemId}" data-target="tab-content-${itemId}" role="tab" aria-selected="false" tabindex="-1"
      hx-get="${BASE}/servers/${itemId}" hx-trigger="click" hx-target="#tab-content-${itemId}" hx-swap="innerHTML">
      ${displayName}
      <span class="pl-2">
        <button id="tab-close-${itemId}">
          <i class="fa-solid fa-xmark" style="font-size: 0.8em;"></i>
        </button>
      </span>
    </a>
  </li>`

  const content = `<div class="tab-content" id="tab-content-${itemId}" role="tabpanel" style="display:none">`

  $('#tablist').append(html)
  $('.app-main').append(content)

  htmx.process(document.querySelector(`#tab-${itemId}`))
  htmx.process(document.querySelector(`#tab-content-${itemId}`))

  const tabLink = document.getElementById(`tab-${itemId}`)
  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(`tab-content-${itemId}`)
    setCurrentTab(`tab-${itemId}`)
  })

  const tabClose = document.getElementById(`tab-close-${itemId}`)
  tabClose.addEventListener('click', (event) => {
    event.stopPropagation()
    unloadTab(itemId)
    deleteTab(displayName, itemId)
    document.getElementById('tab-summary').click()
    replaceCurrentTab(`tab-${itemId}`, 'tab-summary')
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

export function closeTab(id) {
  document.querySelector(`#tab-close-${id}`).click()
  document.querySelector(`#btn-reload-servers`).click()
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

export function showDatacenterSkeleton(id) {
  const target = document.getElementById(`tab-content-${id}`)
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
    const orbId = tab && tab.dataset.orbId
    if (!orbId) return
    const panel = document.getElementById(auditPanelId)
    if (!panel) return
    fetch(BASE + `/api/v1/audit-log?orbId=${encodeURIComponent(orbId)}&limit=50`, {
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

export function initServerDetailTabs(root) {
  const tabContainer = root.querySelector('[id^="srv-detail-tabs-"]')
  if (!tabContainer) return

  const tabs = tabContainer.querySelectorAll('li[data-panel]')
  const srvId = tabContainer.id.replace('srv-detail-tabs-', '')
  const auditPanelId = `srv-panel-audit-${srvId}`

  function loadAuditPanel() {
    const tab = [...tabs].find(t => t.dataset.panel === auditPanelId)
    const orbId = tab && tab.dataset.orbId
    if (!orbId) return
    const panel = document.getElementById(auditPanelId)
    if (!panel) return
    fetch(BASE + `/api/v1/audit-log?orbId=${encodeURIComponent(orbId)}&limit=50`, {
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
    tabContainer.parentElement.querySelectorAll('[id^="srv-panel-"]').forEach(panel => {
      panel.style.display = panel.id === panelId ? '' : 'none'
    })
    if (panelId === auditPanelId) loadAuditPanel()
  }

  tabs.forEach(tab => {
    tab.addEventListener('click', () => activatePanel(tab.dataset.panel))
  })
}

// ─── HTMX afterSwap — shared tab init and timestamp rendering ─────────────────

document.addEventListener('htmx:afterSwap', (evt) => {
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
        columnDefs: [{ className: 'dt-left', targets: 5 }],
        createdRow: function (row) { row.style.cursor = 'pointer'; row.title = 'Double-click to open' },
      })
    }
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

  btn.addEventListener('click', async () => {
    const text = btn.dataset.copyText || ''
    const label = btn.querySelector('span:last-child')
    const icon = btn.querySelector('i')
    const orig = { label: label.textContent, icon: icon.className }

    try {
      await navigator.clipboard.writeText(text)
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
    } catch (_) {
      // Clipboard API blocked (non-secure context or denied permission) —
      // fall back to selecting the code text so the user can Ctrl+C manually.
      const codeEl = document.getElementById('device-code-value')
      if (!codeEl) return
      const range = document.createRange()
      range.selectNodeContents(codeEl)
      const sel = window.getSelection()
      sel.removeAllRanges()
      sel.addRange(range)
      label.textContent = 'Press Ctrl+C'
      setTimeout(() => { label.textContent = orig.label }, 2000)
    }
  })
}

document.addEventListener('DOMContentLoaded', initDeviceCodeCopy)

// ─── DataCenter / Server tab loading ──────────────────────────────────────────
//
// Shared between orbital and orb. Both apps' /datacenters/:id and /servers/:id
// routes return an HTML fragment when HX-Request: true is sent — we use HTMX
// to load that fragment into the new tab's content div.

export function loadDataCenterTab(displayName, id) {
  const tabHtml = `<li class="tab">
    <a id="tab-${id}" data-target="tab-content-${id}" role="tab" aria-selected="false" tabindex="-1">
      ${displayName}
      <span class="pl-2">
        <button id="tab-close-${id}">
          <i class="fa-solid fa-xmark" style="font-size: 0.8em;"></i>
        </button>
      </span>
    </a>
  </li>`
  const contentHtml = `<div class="tab-content" id="tab-content-${id}" role="tabpanel" style="display:none"></div>`

  $('#tablist').append(tabHtml)
  $('.app-main').append(contentHtml)

  const tabLink = document.getElementById(`tab-${id}`)
  const tabContent = document.getElementById(`tab-content-${id}`)

  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(`tab-content-${id}`)
    setCurrentTab(`tab-${id}`)
    if (!tabContent.dataset.loaded) {
      htmx.ajax('GET', BASE + '/datacenters/' + id, { target: '#tab-content-' + id, swap: 'innerHTML' })
    }
  })

  document.getElementById(`tab-close-${id}`).addEventListener('click', (event) => {
    event.stopPropagation()
    localStorage.removeItem(`dc-detail-tab-${id}`)
    unloadTab(id)
    deleteTab(displayName, id)
    document.getElementById('tab-summary').click()
    replaceCurrentTab(`tab-${id}`, 'tab-summary')
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

export function loadServerListTab(displayName, id) {
  const tabHtml = `<li class="tab">
    <a id="tab-srv-${id}" data-target="tab-content-srv-${id}" role="tab" aria-selected="false" tabindex="-1">
      ${displayName}
      <span class="pl-2">
        <button id="tab-close-srv-${id}">
          <i class="fa-solid fa-xmark" style="font-size: 0.8em;"></i>
        </button>
      </span>
    </a>
  </li>`
  const contentHtml = `<div class="tab-content" id="tab-content-srv-${id}" role="tabpanel" style="display:none"></div>`

  $('#tablist').append(tabHtml)
  $('.app-main').append(contentHtml)

  const tabLink = document.getElementById(`tab-srv-${id}`)
  const tabContent = document.getElementById(`tab-content-srv-${id}`)

  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(`tab-content-srv-${id}`)
    setCurrentTab(`tab-srv-${id}`)
    if (!tabContent.dataset.loaded) {
      htmx.ajax('GET', BASE + '/servers/' + id, { target: '#tab-content-srv-' + id, swap: 'innerHTML' })
    }
  })

  document.getElementById(`tab-close-srv-${id}`).addEventListener('click', (event) => {
    event.stopPropagation()
    deleteServerTab(displayName, id)
    replaceCurrentTab(`tab-srv-${id}`, 'tab-summary')
    tabLink.parentElement.remove()
    tabContent.remove()
    document.getElementById('tab-summary').click()
  })
}

// initDatacenterTabRestoration restores DC tabs from localStorage on page load.
// Call on window.load on pages that have #datacenter-table.
export function initDatacenterTabRestoration() {
  if (!document.getElementById('datacenter-table')) return

  if (new URLSearchParams(window.location.search).get('fresh') === '1') {
    localStorage.removeItem('datacenterTabs')
    localStorage.removeItem('tabCurrent')
    history.replaceState(null, '', BASE + '/')
  }

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
    if (!document.getElementById(`tab-srv-${openId}`)) {
      loadServerListTab(displayName, openId)
      saveServerTab(displayName, openId)
    }
    document.getElementById(`tab-srv-${openId}`)?.click()
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
  fetch(BASE + '/api/v1/inventory')
    .then(r => r.json())
    .then(json => {
      const items = json.items ?? []
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
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-inventory' } },
        ] },
        { pageLength: { menu: [50, 100, 250] } },
      ],
      topEnd: { search: { placeholder: 'Search inventory' } },
    },
    select: { style: 'os' },
    autoWidth: true,
    scrollX: true,
    scrollY: 400,
    scrollCollapse: true,
    pageLength: 50,
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

  if (!cached) {
    inventoryFetch(items => {
      inventoryTable.clear().rows.add(items).draw()
      populateDropdowns()
      if (savedNamespace) applyNamespaceFilter(savedNamespace)
    })
  } else {
    populateDropdowns()
  }

  const reloadButton = inventoryTable.button('reload:name').node()
  inventoryTable.button('reload:name').node().on('click', function () {
    inventoryTable.clear().draw()
    reloadButton.addClass('is-loading')
    sessionStorage.removeItem(INVENTORY_CACHE_KEY)
    setTimeout(() => {
      inventoryFetch(items => {
        inventoryTable.rows.add(items).draw()
        populateDropdowns()
        const nsSelect = document.getElementById('inventory-namespace-select')
        const currentNs = nsSelect ? nsSelect.value : ''
        if (currentNs) applyNamespaceFilter(currentNs)
        reloadButton.removeClass('is-loading')
      })
    }, 250)
  })

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
          { extend: 'excel', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-excel"></i><span>Excel</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Excel' },
          { extend: 'csv', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-text"></i><span>CSV</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'CSV' },
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
      url: BASE + '/api/v1/graphql',
      type: 'POST',
      contentType: 'application/json',
      data: () => JSON.stringify({ query: `{ queryDataCenter { id orbId name createdBy createdAt serversAggregate { count } } }` }),
      dataSrc: (json) => (json.data?.queryDataCenter ?? []).map(dc => ({
        id: dc.id,
        orbId: dc.orbId ?? '—',
        name: dc.name,
        createdBy: dc.createdBy ?? '',
        createdAt: dc.createdAt ?? '',
        serverCount: dc.serversAggregate?.count ?? 0,
      })),
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
        { buttons: [
          { extend: 'excel', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-excel"></i><span>Excel</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Excel' },
          { extend: 'csv', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-text"></i><span>CSV</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'CSV' },
          { extend: 'copy', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-copy"></i><span>Copy</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Copy' },
          { extend: 'colvis', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa fa-columns"></i><span>Select</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Select Columns' },
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-servers' } },
        ] },
        { pageLength: { menu: [25, 50, 100, 250] } },
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
      { data: 'oobIP' },
      { data: 'hostname' },
      { data: 'serviceTag' },
      { data: 'model' },
      { data: 'rack' },
      { data: 'id' },
      { data: 'orbId' },
    ],
    columnDefs: [{ targets: [6, 7], visible: false, searchable: true }],
    ajax: {
      url: BASE + '/api/v1/graphql',
      type: 'POST',
      contentType: 'application/json',
      data: () => JSON.stringify({
        query: `{ queryServer {
          id orbId hostname serviceTag model
          oobIP { address }
          rack { name }
          dataCenter { name }
        } }`,
      }),
      dataSrc: (json) => (json.data?.queryServer ?? []).map(s => ({
        id: s.id,
        orbId: s.orbId ?? '—',
        hostname: s.hostname ?? '—',
        serviceTag: s.serviceTag ?? '—',
        model: s.model ?? '—',
        oobIP: s.oobIP?.address ?? '—',
        rack: s.rack?.name ?? '—',
        dataCenter: s.dataCenter?.name ?? '—',
      })),
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
