const BASE = window.ORBITAL_BASE || '';


class TabItem {
  constructor(displayName, id) {
    this.displayName = displayName;
    this.id = id;
  }
}

function unloadTab(itemId) {
  $(`#tab-${itemId}`).parent().remove() // removes li
  $(`#tab-content-${itemId}`).remove()
}

function loadTab(displayName, itemId) {
  // add tab to tablist
  var html = `<li class="tab">
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

  var content = `<div class="tab-content" id="tab-content-${itemId}" role="tabpanel" style="display:none">`

  $('#tablist').append(html);
  $('.app-main').append(content);

  htmx.process(document.querySelector(`#tab-${itemId}`))
  htmx.process(document.querySelector(`#tab-content-${itemId}`))


  // ensure same tab event listener 
  var tabLink = document.getElementById(`tab-${itemId}`)
  tabLink.addEventListener('click', () => {
    var tabContentId = `tab-content-${itemId}`
    activateTab(tabLink.parentElement)
    displayTabContent(tabContentId);
    setCurrentTab(`tab-${itemId}`)
  });

  // tab close event listener ... ideally, when you close a tab you're on, you get the next tab to the right.
  // for now we just go back to the main 'servers' tab
  var tabClose = document.getElementById(`tab-close-${itemId}`)
  tabClose.addEventListener('click', (event) => {
    event.stopPropagation() // allows click on main tab below to work? Got idea from https://stackoverflow.com/questions/64687523/adding-event-listener-to-elements-on-click-of-another-in-loop
    unloadTab(itemId) 
    deleteTab(displayName, itemId)
    document.getElementById('tab-summary').click()  // go back to main tab
    // $('#tab-servers')[0].click();  // this works too
    // remove tab from current if focused
    replaceCurrentTab(`tab-${itemId}`, 'tab-summary')
  });
}

// TODO: get last tab from 'tabs'. if none, return null
function getLastTab() {

}

function deleteTab(displayName, itemId) {
  let tabToDelete = new TabItem(displayName, itemId)

  if (localStorage.datacenterTabs) {
    console.log('looking in existing entry')
    let s = new Set(JSON.parse(localStorage.datacenterTabs))
    if (s.has(JSON.stringify(tabToDelete))) {
      console.log('removing from existing entry')
      s.delete(JSON.stringify(tabToDelete))
    localStorage.datacenterTabs = JSON.stringify([...s])
    }
  } else {
    console.log('no entry.. error?')
  }
}

function saveTab(displayName, itemId) {
  let tabToAdd = new TabItem(displayName, itemId)

  if (localStorage.datacenterTabs) {
    console.log('existing entry')
    let s = new Set(JSON.parse(localStorage.datacenterTabs))
    if (!s.has(JSON.stringify(tabToAdd))) {
      console.log('adding to existing entry')
      s.add(JSON.stringify(tabToAdd))
    localStorage.datacenterTabs = JSON.stringify([...s])
    }
  } else {
    console.log('new entry')
    let s = new Set([JSON.stringify(tabToAdd)])
    localStorage.datacenterTabs = JSON.stringify([...s])
  }
}



// close tab and trigger reload
function closeTab(id){
  document.querySelector(`#tab-close-${id}`).click()
  document.querySelector(`#btn-reload-servers`).click()
}

function getTabStorageKey() {
  if (document.getElementById('server-list-table')) return 'srvTabCurrent'
  return 'dcTabCurrent'
}

function setCurrentTab(id) {
  localStorage[getTabStorageKey()] = id
}

function removeCurrentTab(id) {
  const key = getTabStorageKey()
  if (localStorage[key] == id) localStorage.removeItem(key)
}

function replaceCurrentTab(currentId, targetId) {
  const key = getTabStorageKey()
  if (localStorage[key] == currentId) localStorage.setItem(key, targetId)
}

function getCurrentTab() {
  return localStorage[getTabStorageKey()]
}

// places is-active on tab element, removes is-active on others
function activateTab(selected) {
  (document.querySelectorAll('li.tab') || []).forEach((tab) => {
    if (tab == selected) {
      tab.classList.add('is-active')
    } else {
      tab.classList.remove('is-active')
    }
  })
}

// displays tab-content element with matching id, closes other tab-content elements
function displayTabContent(id) {
  (document.querySelectorAll('.tab-content') || []).forEach((tabContent) => {
    if (tabContent.id === id) {
      tabContent.style.display = 'block';
    } else {
      tabContent.style.display = 'none';
    }
  })
}

function changeTabs(e) {
  const targetTab = e.target;
  const tabList = targetTab.parentNode;
  const tabGroup = tabList.parentNode;

  // Remove all current selected tabs
  tabList
    .querySelectorAll(':scope > [aria-selected="true"]')
    .forEach((t) => t.setAttribute("aria-selected", false));

  // Set this tab as selected
  targetTab.setAttribute("aria-selected", true);

  // Hide all tab panels
  // tabGroup
  //   .querySelectorAll(':scope > [role="tabpanel"]')
  //   .forEach((p) => p.setAttribute("hidden", true));

  // Show the selected panel
  // tabGroup
  //   .querySelector(`#${targetTab.getAttribute("aria-controls")}`)
  //   .removeAttribute("hidden");
}

function formatTimestamp(iso) {
  if (!iso) return "";
  const d = new Date(iso);

  const pad = (n) => n.toString().padStart(2, '0');
  const year = d.getFullYear();
  const month = pad(d.getMonth() + 1);
  const day = pad(d.getDate());
  const hours = pad(d.getHours());
  const minutes = pad(d.getMinutes());
  const seconds = pad(d.getSeconds());

  // Get short timezone like "PDT" or "MST"
  const tz = d.toLocaleTimeString('en-us', { timeZoneName: 'short' }).split(' ')[2];

  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds} ${tz}`;
}

function relativeTime(iso) {
  if (!iso) return '';
  const diff = (new Date(iso) - Date.now()) / 1000; // seconds, negative = past
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
  if (abs < 60)        return rtf.format(Math.round(diff), 'second');
  if (abs < 3600)      return rtf.format(Math.round(diff / 60), 'minute');
  if (abs < 86400)     return rtf.format(Math.round(diff / 3600), 'hour');
  if (abs < 86400 * 7) return rtf.format(Math.round(diff / 86400), 'day');
  return formatTimestamp(iso);
}

function renderTimestamps(root) {
  (root || document).querySelectorAll('[data-timestamp]').forEach(el => {
    const iso = el.dataset.timestamp;
    if (!iso) return;
    el.textContent = relativeTime(iso);
    el.title = formatTimestamp(iso);
    el.style.cursor = 'help';
  });
}


/**
 * Initialize or refresh a server events DataTable.
 * @param {string} serverId - e.g. "1"
*/
const serverTables = new Map();
function initServerEventsTable(serverId) {
  const tableId = `${serverId}-ev`;
  const $table = $(`#${tableId}`);

  // If table already exists, refresh and fix layout
  if ($.fn.dataTable.isDataTable($table)) {
      const existingTable = $table.DataTable();
      existingTable.ajax.reload(null, false); // false = don’t reset pagination
      existingTable.columns.adjust().draw(false);

      // Hide the empty wrapper div after redraw
      setTimeout(() => {
          const wrapper = document.querySelector('[id$="-ev_wrapper"] > .columns.is-multiline');
          if (wrapper) {
              wrapper.style.display = 'none';
          } 
      }, 50);
      return;
  }

  const table = $table.DataTable({
    dom: '', // ✅ no wrapper divs at all
    scrollX: true,
    searching: false,
    paging: false,
    autoWidth: true,
    order: [[0, 'desc']],
    ajax: {
      url: BASE + `/api/v1/servers/${serverId}/events`,
      method: "GET",
      dataSrc: "",
      deferRender: true,
      cache: true,
      timeout: 30000, // timeout 30 secs
    },
    columnDefs: [
        { target: 0, width: "80px" },
        { target: 1, width: "190px" },
        { target: 2, width: "140px" },
        { target: 3, className: "wrap-text", width: "400px" },
        { target: 4, className: "no-wrap-text"},
    ],
    columns: [
      { data: "type" },
      {
          data: "timestamp",
          render: (data) => formatTimestamp(data)
      },
      { data: "userId" },
      { data: "message" },
      {
        data: "details",
        render: function (data, type, row) {
          if (!data || !data.diff) return "";

          const diffLines = data.diff.split("\n");
          const filtered = [];
          let skip = false;

          for (const line of diffLines) {
            if (line.startsWith("@")) {
                skip = line.includes('"version"');
                if (!skip) filtered.push(line);
                continue;
            }
            if (!skip) filtered.push(line);
          }

          const colored = filtered.map(line => {
            if (line.startsWith("+")) return `<span class="diff-added">${line}</span>`;
            if (line.startsWith("-")) return `<span class="diff-removed">${line}</span>`;
            return line;
          }).join("\n");

          return `<pre class="diff-output">${colored}</pre>`;
        },
      }
    ]
  });
  serverTables.set(tableId, table);
}

/**
 * Show a specific tab panel for a server and hide the others.
 * @param {string} tabId - e.g. "1-ev-det"
 */
function openServerTab(tabId) {
  const serverId = tabId.split('-')[0];

  // Hide all tab content for this server
  document.querySelectorAll(`[id^="${serverId}-"].detcontent`).forEach(d => d.classList.add('is-hidden'));
  // Deactivate all tab headers for this server
  document.querySelectorAll(`[id^="${serverId}-"].detlinks`).forEach(el => el.classList.remove('is-active'));

  // Show selected content and activate its tab
  const panel = document.getElementById(tabId);
  if (panel) panel.classList.remove('is-hidden');

  const header = document.getElementById(tabId + '-link') || document.getElementById(tabId.replace('-det','-detlink'));
  if (header) header.classList.add('is-active');

  // Initialize or refresh events table if this is the events tab
  if (tabId.endsWith('-ev-det')) {
      setTimeout(() => {
          initServerEventsTable(serverId);
      }, 100); // delay ensures panel is visible before DataTables layout calc
  }
}

// ─── Todo toast (works for dynamically loaded content) ───────────────────────

document.addEventListener('click', (e) => {
  if (e.target.closest('.todo')) {
    displayTodoToast()
  }
})

// ─── Data Centers ────────────────────────────────────────────────────────────

function showDatacenterSkeleton(id) {
  const target = document.getElementById(`tab-content-${id}`)
  if (!target) return
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  const summary = [
    'Name', 'Servers', 'Racks', 'Asset Data',
  ].map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')
  const meta = [
    'Namespace', 'Orb ID', 'Created By', 'Created At', 'Last Updated', 'Last Updated By',
  ].map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')
  const srvRows = Array.from({ length: 10 }, () =>
    `<tr>${['','','','','',''].map(() => `<td>${s()}</td>`).join('')}</tr>`
  ).join('')
  target.innerHTML = `
    <div class="fixed-grid has-3-cols mb-0">
      <div class="columns m-0">
        <div class="column pt-0 pl-0">
          <button class="button is-rounded is-small is-warning mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-gauge-high"></i></span><span>Grafana</span>
          </button>
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

function showServerSkeleton(targetId, variant) {
  const target = document.getElementById(targetId)
  if (!target) return
  const s = () => `<span class="is-skeleton" style="display:block">&nbsp;</span>`
  const rows = [
    'Data Center', 'Hostname', 'Manufacturer', 'Model',
    'OOB IP', 'OOB MAC', 'Rack', 'Rack Position', 'Service Tag',
  ].map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')
  const meta = [
    'Namespace', 'Orb ID', 'Created By', 'Created At', 'Last Updated', 'Last Updated By',
  ].map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')

  const isOrb = variant === 'orb'
  const buttons = isOrb ? `
          <button class="button is-rounded is-small is-link mt-1 is-loading" disabled>
            <span class="icon"><i class="fa-solid fa-refresh"></i></span><span>Reload</span>
          </button>
          <button class="button is-rounded is-small is-warning mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-pen-to-square"></i></span><span>Override</span>
          </button>` : `
          <button class="button is-rounded is-small is-warning mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-gauge-high"></i></span><span>Grafana</span>
          </button>
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
                'Firmware Version','SSH Enabled','USB Mgmt Port Enabled',
                'OS-to-iDRAC Pass-through','IPMI Enabled','Lockdown Mode',
                'DHCP Enabled','RACADM Enabled',
              ].map(l => `<tr><td style="white-space:nowrap;width:1%">${l}</td><td>${s()}</td></tr>`).join('')}</tbody></table>
            </div>
          </article>
        </div>
      </div>
    </div>`
}

function fetchWithMinDelay(url, minMs = 500) {
  return Promise.all([
    fetch(BASE + url, { headers: { 'HX-Request': 'true' } }).then(r => r.text()),
    new Promise(resolve => setTimeout(resolve, minMs)),
  ]).then(([html]) => html)
}

function initDcDetailTabs(id) {
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
    fetch(BASE + `/api/v1/events?orbId=${encodeURIComponent(orbId)}&limit=50`, {
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

// DataTables renders the page-length <select> bare, with no wrapper. Bulma's
// select component requires a <div class="select"> wrapper to render its own
// CSS arrow (via ::after) and apply is-small sizing. Without this, the browser's
// native arrow overlaps the selected value and sizing is inconsistent with other
// is-small elements. Called via initComplete on each DataTable.
function dtWrapLengthSelect(api) {
  $(api.table().container()).find('div.dt-length select').wrap('<div class="select is-small"></div>')
}

function initServerDetailTabs(root) {
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
    fetch(BASE + `/api/v1/events?orbId=${encodeURIComponent(orbId)}&limit=50`, {
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
    tab.addEventListener('click', () => {
      activatePanel(tab.dataset.panel)
    })
  })
}

// ── Server drill-down (dblclick row → server detail) ─────────────────────────

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

function loadDataCenterTab(displayName, id) {
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

  const tabClose = document.getElementById(`tab-close-${id}`)
  tabClose.addEventListener('click', (event) => {
    event.stopPropagation()
    localStorage.removeItem(`dc-detail-tab-${id}`)
    unloadTab(id)
    deleteTab(displayName, id)
    document.getElementById('tab-summary').click()
    replaceCurrentTab(`tab-${id}`, 'tab-summary')
  })
}

// ─── Inventory page (/inventory, /) ──────────────────────────────────────────

const INVENTORY_CACHE_KEY = 'inventoryCache'

function inventoryFetch(onData) {
  fetch(BASE + '/api/v1/inventory')
    .then(r => r.json())
    .then(json => {
      const items = json.items ?? []
      sessionStorage.setItem(INVENTORY_CACHE_KEY, JSON.stringify(items))
      onData(items)
    })
}

document.addEventListener('DOMContentLoaded', () => {
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
    searchCols: [
      savedType ? { search: savedType } : null,
      null, null, null, null, null,
    ],
    initComplete: function () {
      dtWrapLengthSelect(this.api())

      const typeSelect = document.getElementById('inventory-type-select')
      typeSelect.addEventListener('change', function () {
        localStorage.setItem('inventoryTypeFilter', this.value)
        inventoryTable.column(0).search(this.value, { exact: !!this.value }).draw()
      })

      const nsSelect = document.getElementById('inventory-namespace-select')
      nsSelect.addEventListener('change', function () {
        localStorage.setItem('inventoryNamespaceFilter', this.value)
        applyNamespaceFilter(this.value)
      })

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
    nsSelect.options.length = 1
    const seen = new Set()
    inventoryTable.column(1).data().each(orbId => {
      const ns = orbId ? orbId.split(':')[0] : ''
      if (ns && !seen.has(ns)) seen.add(ns)
    })
    Array.from(seen).sort().forEach(ns => nsSelect.add(new Option(ns, ns)))
    if (savedNamespace) nsSelect.value = savedNamespace
  }

  // If no cache, fetch now and populate
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
        const currentNs = document.getElementById('inventory-namespace-select').value
        if (currentNs) applyNamespaceFilter(currentNs)
        reloadButton.removeClass('is-loading')
      })
    }, 250)
  })

})

// ─── Hint banners ────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  const banner = document.getElementById('hint-banner-dblclick')
  if (!banner) return
  const KEY = document.getElementById('server-list-table')
    ? 'hint-dblclick-dismissed-srv'
    : 'hint-dblclick-dismissed-dc'
  if (!sessionStorage.getItem(KEY)) {
    banner.style.display = ''
  }
  document.getElementById('hint-banner-dblclick-dismiss').addEventListener('click', () => {
    sessionStorage.setItem(KEY, '1')
    banner.style.display = 'none'
  })
})

// ─── Data Centers page (/datacenters) ────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('datacenter-table')) return

  // Wire up static tabs (e.g. Summary) — dynamic tabs get their own listeners in loadDataCenterTab
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
      url: BASE + '/graphql',
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

  $('#datacenter-table tbody').on('dblclick', 'tr', function () {
    const displayName = this.cells[0].innerText
    const id = datacenterTable.row(this).data().id
    const tab = document.getElementById(`tab-${id}`)
    if (tab) {
      tab.click()
    } else {
      loadDataCenterTab(displayName, id)
      saveTab(displayName, id)
      document.getElementById(`tab-${id}`).click()
    }
  })
})

// ─── Servers page ────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
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
    columnDefs: [
      { targets: [6, 7], visible: false, searchable: true },
    ],
    ajax: {
      url: BASE + '/graphql',
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

  $('#server-list-table tbody').on('dblclick', 'tr', function () {
    const data = serverListTable.row(this).data()
    if (!data) return
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
  })
})

function saveServerTab(displayName, id) {
  const item = JSON.stringify(new TabItem(displayName, id))
  const s = new Set(localStorage.serverTabs ? JSON.parse(localStorage.serverTabs) : [])
  s.add(item)
  localStorage.serverTabs = JSON.stringify([...s])
}

function deleteServerTab(displayName, id) {
  const item = JSON.stringify(new TabItem(displayName, id))
  const s = new Set(localStorage.serverTabs ? JSON.parse(localStorage.serverTabs) : [])
  s.delete(item)
  localStorage.serverTabs = JSON.stringify([...s])
}

function loadServerListTab(displayName, id) {
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

window.addEventListener('load', () => {
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
  if (currentTabId) {
    document.getElementById(currentTabId)?.click()
  }
})

// Double-click on server row in DC detail panel → navigate to /servers?open=<id>
document.addEventListener('dblclick', (e) => {
  const row = e.target.closest('tr[data-server-id]')
  if (!row) return
  const id = row.dataset.serverId
  const label = row.dataset.displayName || id
  window.location.href = BASE + '/servers?open=' + encodeURIComponent(id) + '&label=' + encodeURIComponent(label)
})

// ─── Backups ──────────────────────────────────────────────────────────────────

const backupStatusColors = {
  completed: 'is-success',
  skipped:   'is-info',
  running:   'is-warning',
  pending:   'is-warning',
  failed:    'is-danger',
}

let pendingDeleteId = null

function formatBytes(bytes) {
  if (!bytes) return '—'
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / 1048576).toFixed(1) + ' MB'
}

function renderBackups(jobs) {
  const tbody = document.getElementById('backup-tbody')
  if (!tbody) return
  if (!jobs || jobs.length === 0) {
    tbody.innerHTML = '<tr><td colspan="6" class="has-text-grey has-text-centered">No backups yet.</td></tr>'
    return
  }
  tbody.innerHTML = jobs.map(j => {
    const tag = backupStatusColors[j.status] || 'is-light'
    const checksumDisplay = j.checksum
      ? `<span class="is-family-monospace is-size-7" style="word-break:break-all;">${j.checksum}</span> <button class="button is-small is-white" title="Copy checksum" onclick="navigator.clipboard.writeText('${j.checksum}').then(()=>{this.innerHTML='<span class=\\'icon\\'><i class=\\'fas fa-check\\'></i></span>';setTimeout(()=>{this.innerHTML='<span class=\\'icon\\'><i class=\\'fas fa-copy\\'></i></span>';},1200)})"><span class="icon"><i class="fas fa-copy"></i></span></button>`
      : '—'
    const statusCell = j.status === 'failed' && j.error
      ? `<span class="tag ${tag}">${j.status} ⚠</span><br><span class="has-text-danger is-size-7" style="display:block;max-width:400px;white-space:normal;word-break:break-word;margin-top:4px;">${j.error}</span>`
      : `<span class="tag ${tag}">${j.status}</span>`
    const canDelete = j.status !== 'running' && j.status !== 'pending'
    const actions = [
      j.status === 'completed' && j.s3Key
        ? `<a data-testid="backup-download-btn" class="button is-small is-light" onclick="downloadBackup('${j.id}')" title="Download"><span class="icon"><i class="fas fa-download"></i></span></a>`
        : '',
      canDelete
        ? `<a class="button is-small is-light has-text-danger" onclick="openDeleteModal('${j.id}', '${new Date(j.initiatedAt).toLocaleString()}')" title="Delete"><span class="icon"><i class="fas fa-trash"></i></span></a>`
        : '',
    ].join('')
    return `<tr>
      <td>${new Date(j.initiatedAt).toLocaleString()}</td>
      <td data-testid="backup-job-status">${statusCell}</td>
      <td>${j.initiatedBy || '—'}</td>
      <td>${formatBytes(j.sizeBytes)}</td>
      <td style="max-width:340px;">${checksumDisplay}</td>
      <td><div class="buttons is-right" style="gap: 0.25rem; flex-wrap: nowrap;">${actions}</div></td>
    </tr>`
  }).join('')
}

function loadBackups() {
  fetch(BASE + '/api/v1/backups')
    .then(r => r.json())
    .then(renderBackups)
    .catch(() => {})
}

function triggerBackup() {
  const btn = document.getElementById('btn-backup')
  const msg = document.getElementById('backup-status-msg')
  btn.classList.add('is-loading')
  btn.disabled = true
  msg.style.display = 'none'

  fetch(BASE + '/api/v1/backups', { method: 'POST' })
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
    fetch(BASE + '/api/v1/backups/' + jobId)
      .then(r => r.json())
      .then(data => {
        loadBackups()
        if (data.status === 'completed' || data.status === 'skipped' || data.status === 'failed') {
          clearInterval(interval)
          btn.classList.remove('is-loading')
          btn.disabled = false
        }
      })
      .catch(() => { clearInterval(interval); btn.classList.remove('is-loading'); btn.disabled = false })
  }, 2000)
}

function downloadBackup(id) {
  fetch(BASE + '/api/v1/backups/' + id + '/download')
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

  fetch(BASE + '/api/v1/backups/' + pendingDeleteId, { method: 'DELETE' })
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

window.addEventListener('load', () => {
  if (!document.getElementById('datacenter-table')) return

  if (new URLSearchParams(window.location.search).get('fresh') === '1') {
    localStorage.removeItem('datacenterTabs')
    localStorage.removeItem('tabCurrent')
    history.replaceState(null, '', '/')
  }

  if (!localStorage.datacenterTabs) return
  const tabSet = new Set(JSON.parse(localStorage.datacenterTabs))
  tabSet.forEach(tabData => {
    const { displayName, id } = JSON.parse(tabData)
    loadDataCenterTab(displayName, id)
  })
  const currentTabId = getCurrentTab()
  if (currentTabId) {
    document.getElementById(currentTabId)?.click()
  }
})

// ─── Shared Listeners ────────────────────────────────────────────────────────

function addEventListeners() {
  // HTMX Listener
  //
  // HTML fragments returned from htmx requests might need event listners
  // applied. For exmaple, model triggers from server.gohtml
  document.addEventListener('htmx:afterRequest', (evt) => {
    console.log('event here');
    console.dir(evt);

    // Modal trigger
    (document.querySelectorAll('.js-modal-trigger') || []).forEach((trigger) => {
      const modalId = trigger.dataset.target; // dataset-target attribute
      const target = document.getElementById(modalId);
      trigger.addEventListener('click', () => {
        console.log('clicked open modal for server')
        console.log(target)
        openModal(target);
      });
    });

    // Modal close
    (document.querySelectorAll('.js-modal-close') || []).forEach((close) => {
      const target = close.closest('.modal');
      close.addEventListener('click', () => {
        closeModal(target);
      });
    });
  });

  // listeners
  document.addEventListener('DOMContentLoaded', () => {
    // Listener on tab anchor elements
    (document.querySelectorAll('li.tab a') || []).forEach((a) => {
      const tabContentId = a.dataset.target; // data-target attribute
      a.addEventListener('click', () => {
        activateTab(a.parentElement)
        displayTabContent(tabContentId);
        setCurrentTab(a.id)
      });
    });

    // Listener on login notification delete button
    (document.querySelectorAll('.notification .delete') || []).forEach(($delete) => {
      const $notification = $delete.parentNode;
      $delete.addEventListener('click', () => {
        $notification.parentNode.removeChild($notification);
      });
    });


    // Add a keyboard event to close all modals
    document.addEventListener('keydown', (event) => {
      if(event.key === "Escape") {
        closeAllModals();
      }
    });

    // Submit button
    $("#button-submit").click(function(event) {
      event.preventDefault(); // disables default validation box so we need to handle
      $(this).toggleClass('is-link');
    });
  });

  window.addEventListener("DOMContentLoaded", () => {
    // Only handle one particular tablist; if you have multiple tab
    // lists (might even be nested), you have to apply this code for each one
    const tabList = document.querySelector('[role="tablist"]');
    if (!tabList) {
      return
    }
    const tabs = tabList.querySelectorAll(':scope [role="tab"]');

    // // Add a click event handler to each tab
    // tabs.forEach((tab) => {
    //   tab.addEventListener("click", changeTabs);
    // });

    // Enable arrow navigation between tabs in the tab list
    let tabFocus = 0;

    tabList.addEventListener("keydown", (e) => {
      // Move right
      if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
        tabs[tabFocus].setAttribute("tabindex", -1);
        if (e.key === "ArrowRight") {
          tabFocus++;
          // If we're at the end, go to the start
          if (tabFocus >= tabs.length) {
            tabFocus = 0;
          }
          // Move left
        } else if (e.key === "ArrowLeft") {
          tabFocus--;
          // If we're at the start, move to the end
          if (tabFocus < 0) {
            tabFocus = tabs.length - 1;
          }
        }

        tabs[tabFocus].setAttribute("tabindex", 0);
        tabs[tabFocus].focus();
      }

      else if (e.key === "Enter") {
        console.log('enter')
        console.dir(tabs[tabFocus])
        tabs[tabFocus].click()
      }
    });
  });

  document.addEventListener('DOMContentLoaded', () => {
    const menuLinks = document.querySelectorAll('.app-menu-link');

    menuLinks.forEach(link => {
      link.addEventListener('click', (e) => {
        e.preventDefault();
        const section = link.dataset.section;
        const sublist = document.querySelector(`.app-menu-section-sublist[data-sublist="${section}"]`);
        const isOpen = link.classList.contains('is-open');

        // Close all
        document.querySelectorAll('.app-menu-link.is-open').forEach(l => l.classList.remove('is-open'));
        document.querySelectorAll('.app-menu-section-sublist.is-open').forEach(s => s.classList.remove('is-open'));

        // Open the one clicked (if not already open)
        if (!isOpen) {
          link.classList.add('is-open');
          sublist.classList.add('is-open');
        }
      });
    });

    // Highlight the active link based on current URL
    const currentPath = window.location.pathname;
    const activeLink = document.querySelector(`.app-menu-sublink[href="${currentPath}"]`);
    if (activeLink) {
      activeLink.classList.add('is-active');
      const parentSublist = activeLink.closest('.app-menu-section-sublist');
      if (parentSublist) {
        parentSublist.classList.add('is-open');
        parentSublist.previousElementSibling.classList.add('is-open');
      }
    }
  });

  /**
   * Handle HTMX swaps — ensures the active tab reopens and tables render correctly.
   */
  document.body.addEventListener('htmx:afterSwap', (evt) => {
    const swapped = evt.detail.target;
    if (!swapped || !swapped.querySelector) return;

    // Find which tab is currently active in the swapped content
    const defaultTabLink = swapped.querySelector('.detlinks.is-active');
    if (defaultTabLink) {
        const panelId = defaultTabLink.id.replace(/-detlink$/, '-det');
        openServerTab(panelId);
    }

    // If the events table was part of this swap, ensure it's initialized
    const evTables = swapped.querySelectorAll('[id$="-ev"]');
    evTables.forEach(el => {
        const serverId = el.id.split('-')[0];
        setTimeout(() => {
            initServerEventsTable(serverId);
        }, 100);
    });

  });
}

// ── Export page ───────────────────────────────────────────────────────────────

let exportPollTimer = null

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('export-jobs-tbody')) return

  const select = document.getElementById('export-datacenter-select')
  const submitBtn = document.getElementById('export-submit-btn')

  fetch(BASE + '/graphql', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ query: '{ queryDataCenter { id name } }' }),
  })
    .then(r => r.json())
    .then(json => {
      const dcs = json.data?.queryDataCenter ?? []
      select.innerHTML = '<option value="" disabled selected>— select a data center —</option>'
      dcs.forEach(dc => {
        const opt = document.createElement('option')
        opt.value = dc.id
        opt.textContent = dc.name
        select.appendChild(opt)
      })
      select.addEventListener('change', () => {
        submitBtn.disabled = !select.value
      })
    })
    .catch(() => {
      select.innerHTML = '<option value="" disabled selected>Failed to load data centers</option>'
    })

  loadExportJobsTable()
})

function handleExportSubmit() {
  const select = document.getElementById('export-datacenter-select')
  const id = select.value
  if (!id) return

  const submitBtn = document.getElementById('export-submit-btn')
  submitBtn.classList.add('is-loading')
  submitBtn.disabled = true

  fetch(BASE + `/api/v1/datacenters/${id}/export`, { method: 'POST' })
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
        const label = job.status === 'running' ? 'Exporting…' : 'Pending…'
        showExportStatus('is-info', 'fa-spinner fa-spin', label)
      }
    })
    .catch(() => {
      exportPollTimer = setTimeout(() => pollExportStatus(jobId), 3000)
    })
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

  fetch(BASE + '/api/v1/export/jobs')
    .then(r => r.json())
    .then(jobs => {
      tbody.innerHTML = jobs.length === 0
        ? '<tr><td colspan="7" class="has-text-grey">No export jobs yet.</td></tr>'
        : jobs.map(job => {
            const actions = []
            if (job.status === 'completed') {
              actions.push(`<a data-testid="export-download-btn" class="button is-small is-link is-outlined" href="${BASE}/api/v1/export/jobs/${job.jobId}/download"><span class="icon"><i class="fa-solid fa-download"></i></span><span>Download</span></a>`)
              if (ociConfigured) {
                const publishLabel = job.published ? 'Publish Again' : 'Publish'
                actions.push(`<button class="button is-small is-warning is-outlined" onclick="publishExportJob('${job.jobId}')"><span class="icon"><i class="fa-solid fa-box-archive"></i></span><span>${publishLabel}</span></button>`)
              }
            }
            actions.push(`<button class="button is-small is-danger is-outlined" title="Delete" onclick="deleteExportJob('${job.jobId}')"><span class="icon"><i class="fa-solid fa-trash"></i></span></button>`)

            const statusCell = exportJobStatusBadge(job.status, job.published)
            return `<tr>
              <td style="font-family:monospace;font-size:0.7rem;">${job.jobId}</td>
              <td>${job.dataCenter ?? '—'}</td>
              <td data-testid="export-job-status">${statusCell}</td>
              <td>${fmtTime(job.createdAt)}</td>
              <td>${fmtTime(job.startedAt)}</td>
              <td>${fmtTime(job.completedAt)}</td>
              <td style="white-space:nowrap;"><div style="display:flex;gap:0.25rem;align-items:center;">${actions.join('')}</div></td>
            </tr>`
          }).join('')
    })
    .catch(() => {})
}

function exportJobStatusBadge(status, published) {
  const colorMap = {
    pending:   'is-warning is-light',
    running:   'is-info is-light',
    completed: 'is-success is-light',
    failed:    'is-danger is-light',
    stale:     'is-light',
  }
  let badge = `<span class="tag ${colorMap[status] ?? ''}">${status}</span>`
  if (published) {
    badge += ` <span class="tag is-primary is-light ml-1">published</span>`
  }
  return badge
}

function publishExportJob(jobId) {
  fetch(BASE + `/api/v1/export/jobs/${jobId}/publish`, { method: 'POST' })
    .then(r => r.json())
    .then(res => {
      if (res.error) {
        alert(`Publish failed: ${res.error}`)
        return
      }
      loadExportJobsTable()
      pollPublishJob(res.artifactId)
    })
    .catch(() => alert('Failed to start publish.'))
}

function pollPublishJob(artifactId) {
  fetch(BASE + `/api/v1/oci/artifacts/${artifactId}`)
    .then(r => r.json())
    .then(a => {
      if (a.status === 'completed' || a.status === 'failed') {
        loadExportJobsTable()
        return
      }
      setTimeout(() => pollPublishJob(artifactId), 2000)
    })
    .catch(() => setTimeout(() => pollPublishJob(artifactId), 3000))
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

function fmtTime(iso) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

// ── Edge Delivery page ────────────────────────────────────────────────────────

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
  fetch(BASE + '/api/v1/oci/artifacts')
    .then(r => r.json())
    .then(artifacts => {
      tbody.innerHTML = artifacts.length === 0
        ? '<tr><td colspan="9" class="has-text-grey">No artifacts yet.</td></tr>'
        : artifacts.map(a => `<tr>
            <td>${a.datacenterName}</td>
            <td style="font-family:monospace;font-size:0.7rem">${a.repository}</td>
            <td><span class="tag is-light">${a.tag}</span></td>
            <td style="white-space:nowrap;">${a.digest ? `<div style="display:flex;align-items:center;gap:0.25rem;"><span class="is-family-monospace is-size-7">${a.digest.substring(0, 19)}…</span><button class="button is-small is-white" title="Copy digest" onclick="navigator.clipboard.writeText('${a.digest}').then(()=>{this.innerHTML='<span class=\\'icon\\'><i class=\\'fas fa-check\\'></i></span>';setTimeout(()=>{this.innerHTML='<span class=\\'icon\\'><i class=\\'fas fa-copy\\'></i></span>';},1200)})"><span class="icon"><i class="fas fa-copy"></i></span></button></div>` : '—'}</td>
            <td>${a.signed ? '<span class="tag is-success is-light">signed</span>' : '<span class="tag is-light">unsigned</span>'}</td>
            <td>${a.enriched ? '<span class="tag is-info is-light">enriched</span>' : '—'}${a.enricherError ? `<p class="has-text-danger is-size-7 mt-1">${a.enricherError}</p>` : ''}</td>
            <td>${artifactStatusBadge(a.status)}</td>
            <td>${fmtTime(a.initiatedAt)}</td>
            <td class="has-text-danger is-size-7">${a.error ?? ''}</td>
          </tr>`).join('')
    })
    .catch(() => {})
    .finally(() => { minDelay.then(() => { if (btn) btn.classList.remove('is-loading') }) })
}

function artifactStatusBadge(status) {
  const colorMap = {
    pending:   'is-warning is-light',
    pushing:   'is-info is-light',
    completed: 'is-success is-light',
    failed:    'is-danger is-light',
  }
  return `<span class="tag ${colorMap[status] ?? ''}">${status}</span>`
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

  if (_cachedPublicKey) {
    _showPublicKey(_cachedPublicKey)
    return
  }

  btn.classList.add('is-loading')
  fetch(BASE + '/api/v1/oci/public-key')
    .then(r => { if (!r.ok) throw new Error('Failed'); return r.text() })
    .then(key => {
      _cachedPublicKey = key
      _showPublicKey(key)
    })
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

// ── Backups page ──────────────────────────────────────────────────────────────

function testBackupConnection() {
  const btn = document.getElementById('btn-test-backup-connection')
  const result = document.getElementById('backup-connection-result')
  btn.classList.add('is-loading')
  result.textContent = ''
  fetch(BASE + '/api/v1/backups/test-connection', { method: 'POST' })
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

// ── Timestamp rendering ───────────────────────────────────────────────────────

document.addEventListener('htmx:afterSwap', (evt) => {
  const target = evt.detail && evt.detail.target
  if (!target) return
  renderTimestamps(target)

  const dcDetailTabs = target.querySelector('[id^="dc-detail-tabs-"]')
  if (dcDetailTabs) {
    const id = dcDetailTabs.id.replace('dc-detail-tabs-', '')
    target.dataset.loaded = 'true'
    initDcDetailTabs(id)
    dcEditors.delete(id)
    initServerDetailTabs(target)

    const dcServersTable = target.querySelector('[id^="dc-servers-table-"]')
    if (dcServersTable && !$.fn.DataTable.isDataTable(dcServersTable)) {
      new DataTable(dcServersTable, {
        paging: false,
        searching: false,
        info: false,
        ordering: true,
        select: { style: 'os' },
        autoWidth: true,
        createdRow: function (row) { row.style.cursor = 'pointer'; row.title = 'Double-click to open' },
      })
    }
    return
  }

  const srvDetailTabs = target.querySelector('[id^="srv-detail-tabs-"]')
  if (srvDetailTabs) {
    target.dataset.loaded = 'true'
    initServerDetailTabs(target)
    const srvId = srvDetailTabs.id.replace('srv-detail-tabs-', '')
    srvEditors.delete(srvId)
  }
})

// ── DataCenter edit modal ─────────────────────────────────────────────────────

window.dcEditors = new Map()
const dcEditors = window.dcEditors

document.addEventListener('click', function (e) {
  const editBtn = e.target.closest('[data-dc-edit-id]')
  if (editBtn) {
    const id = editBtn.dataset.dcEditId
    const modal = document.getElementById('edit-modal-dc-' + id)
    if (!modal) return

    // Initialize editor lazily so it renders into a visible container
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
          const resp = await fetch(BASE + '/graphql', {
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

// ── Tab reloads ───────────────────────────────────────────────────────────────

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
        const dcServersTable = target.querySelector('[id^="dc-servers-table-"]')
        if (dcServersTable && !$.fn.DataTable.isDataTable(dcServersTable)) {
          new DataTable(dcServersTable, { paging: false, searching: false, info: false, ordering: true, select: { style: 'os' }, autoWidth: true })
        }
      }
      const defaultTabLink = target.querySelector('.detlinks.is-active')
      if (defaultTabLink) {
        openServerTab(defaultTabLink.id.replace(/-detlink$/, '-det'))
      }
      target.querySelectorAll('[id$="-ev"]').forEach(el => {
        const serverId = el.id.split('-')[0]
        setTimeout(() => initServerEventsTable(serverId), 100)
      })
    })
    .catch(() => {})
})

// ── Server edit modal ─────────────────────────────────────────────────────────

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
          const resp = await fetch(BASE + '/graphql', {
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
  // Collapse all existing whitespace so the formatter starts from a clean single-line input
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
      i++ // skip the space after comma
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
  try {
    d = typeof details === 'string' ? JSON.parse(details) : details
  } catch (_) {
    return null
  }

  if (!d.query) return null

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
      url: BASE + '/api/v1/events?limit=200',
      dataSrc: (json) => json.events ?? [],
    },
    createdRow: function (row, data) {
      row.dataset.details = typeof data.details === 'string'
        ? data.details
        : JSON.stringify(data.details)
      // Hide the expand arrow for events with no expandable payload (e.g. REST-triggered events)
      if (!renderPayload(data.details)) {
        row.querySelector('td.dt-control')?.classList.remove('dt-control')
      }
    },
  })

  // Expand/collapse payload on row click
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

// ─── Restore ─────────────────────────────────────────────────────────────────

const restoreJobLogStore = {}

function loadRestoreJobs() {
  const tbody = document.getElementById('restore-tbody')
  if (!tbody) return
  fetch(BASE + '/api/v1/restore')
    .then(r => r.json())
    .then(jobs => {
      if (!jobs.length) {
        tbody.innerHTML = '<tr><td colspan="6" class="has-text-grey has-text-centered">No restore jobs yet.</td></tr>'
        return
      }
      tbody.innerHTML = jobs.map(j => {
        const startedAt = j.startedAt ? new Date(j.startedAt).toLocaleString() : (j.createdAt ? new Date(j.createdAt).toLocaleString() : '—')
        const statusClass = { completed: 'is-success', failed: 'is-danger', running: 'is-warning', pending: 'is-light' }[j.status] || 'is-light'
        const duration = (j.startedAt && j.completedAt)
          ? Math.round((new Date(j.completedAt) - new Date(j.startedAt)) / 1000) + 's'
          : (j.status === 'running' ? 'Running...' : '—')
        const backupLabel = j.backupKey ? j.backupKey.split('/').pop() : (j.backupId ? j.backupId.substring(0, 8) + '...' : '—')
        let logBtn = ''
        if (j.log || j.error) {
          restoreJobLogStore[j.id] = { log: j.log || '', error: j.error || '' }
          logBtn = `<button class="button is-small is-light" onclick="openRestoreLogModal('${j.id}')">Log</button>`
        }
        return `<tr>
          <td>${startedAt}</td>
          <td><span class="tag ${statusClass}">${j.status}</span></td>
          <td style="font-size:0.8rem;">${backupLabel}</td>
          <td>${j.createdBy || '—'}</td>
          <td>${duration}</td>
          <td>${logBtn}</td>
        </tr>`
      }).join('')
    })
    .catch(() => {
      if (tbody) tbody.innerHTML = '<tr><td colspan="6" class="has-text-grey has-text-centered">Failed to load.</td></tr>'
    })
}

function loadRestoreBackupSelect() {
  const sel = document.getElementById('restore-backup-select')
  if (!sel) return
  fetch(BASE + '/api/v1/backups')
    .then(r => r.json())
    .then(backups => {
      const completed = backups.filter(b => b.status === 'completed')
      if (!completed.length) {
        sel.innerHTML = '<option value="">No completed backups available</option>'
        return
      }
      sel.innerHTML = completed.map(b => {
        const label = b.s3Key ? b.s3Key.split('/').pop() : b.id.substring(0, 8) + '...'
        const date = b.initiatedAt ? new Date(b.initiatedAt).toLocaleString() : ''
        return `<option value="${b.id}">${label} (${date})</option>`
      }).join('')
    })
    .catch(() => { sel.innerHTML = '<option value="">Failed to load backups</option>' })
}

function triggerRestore() {
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

  fetch(BASE + '/api/v1/restore', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ backupId: sel.value }),
  })
    .then(r => r.json())
    .then(data => {
      if (data.error) {
        msg.textContent = data.error
        msg.style.display = ''
        btn.classList.remove('is-loading')
        btn.disabled = false
      } else {
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
    fetch(BASE + '/api/v1/restore/' + jobId)
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

function openRestoreLogModal(jobId) {
  const entry = restoreJobLogStore[jobId] || {}
  const parts = []
  if (entry.log) parts.push(entry.log)
  if (entry.error) parts.push('Error: ' + entry.error)
  document.getElementById('restore-log-content').textContent = parts.join('\n') || '(no output)'
  document.getElementById('restore-log-modal').classList.add('is-active')
}

function closeRestoreLogModal() {
  document.getElementById('restore-log-modal').classList.remove('is-active')
}

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('restore-tbody')) return
  loadRestoreJobs()
  loadRestoreBackupSelect()
})

// Clear all tab state and localStorage on logout
document.addEventListener('DOMContentLoaded', () => {
  const logoutForm = document.querySelector('form[action$="/user/logout"]')
  if (!logoutForm) return
  logoutForm.addEventListener('submit', () => {
    localStorage.clear()
    sessionStorage.clear()
  })
})

// ── Orb import ──────────────────────────────────────────────────────────────

let orbImportPollTimer = null

async function handleOrbImport(tag) {
  orbShowImportStatus('is-info', 'fa-spinner fa-spin', `Importing ${tag}…`)
  fetch(BASE + '/api/v1/import', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tag }),
  })
    .then(r => r.json())
    .then(data => {
      if (data.error) {
        orbShowImportStatus('is-warning', 'fa-triangle-exclamation', data.error)
        return
      }
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
        const label = data.status === 'running' ? 'Importing…' : 'Pending…'
        orbShowImportStatus('is-info', 'fa-spinner fa-spin', label)
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
  if (!tbody) return
  fetch(BASE + '/api/v1/import/tags')
    .then(r => r.json())
    .then(data => {
      const tags = data.tags || []
      if (tags.length === 0) {
        tbody.innerHTML = '<tr><td colspan="3" class="has-text-grey">No versions available.</td></tr>'
        return
      }
      // Newest first (tags arrive oldest-first from the registry)
      tbody.innerHTML = [...tags].reverse().map(t => `
        <tr data-tag="${t}">
          <td><strong>${t}</strong></td>
          <td class="has-text-grey">${BASE.includes(t) ? '—' : ''}</td>
          <td>
            <button class="button is-info is-small" onclick="handleOrbImport('${t}')">
              <span class="icon"><i class="fa-solid fa-download"></i></span>
              <span>Import</span>
            </button>
          </td>
        </tr>`).join('')
    })
    .catch(() => {
      const tbody = document.getElementById('orb-tags-tbody')
      if (tbody) tbody.innerHTML = '<tr><td colspan="3" class="has-text-danger">Failed to load tags.</td></tr>'
    })
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

async function handleOrbCourierUpload() {
  const fileInput = document.getElementById('orb-courier-file')
  if (!fileInput?.files[0]) return
  const fd = new FormData()
  fd.append('bundle', fileInput.files[0])
  orbShowImportStatus('is-info', 'fa-spinner fa-spin', 'Uploading bundle…')
  fetch(BASE + '/api/v1/import/upload', { method: 'POST', body: fd })
    .then(r => r.json())
    .then(data => {
      if (data.error) {
        orbShowImportStatus('is-warning', 'fa-triangle-exclamation', data.error)
        return
      }
      orbShowImportStatus('is-info', 'fa-spinner fa-spin', `Importing ${data.tag}…`)
      pollOrbImport()
    })
    .catch(() => orbShowImportStatus('is-danger', 'fa-circle-xmark', 'Upload failed.'))
}

// --- Orb servers DataTable ---

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
    if (data && data.id) {
      window.location = BASE + '/servers/' + data.id
    }
  })
})

// --- Orb DC page ---

document.addEventListener('DOMContentLoaded', () => {
  const page = document.getElementById('orb-dc-page')
  if (!page) return

  const dcSlug = page.dataset.dcSlug
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
      if (list.length === 0) {
        empty.style.display = ''
        return
      }
      const dc = list[0]

      document.getElementById('orb-dc-name').textContent = dc.name ?? '—'
      document.getElementById('orb-dc-server-count').textContent = dc.serversAggregate?.count ?? '—'
      document.getElementById('orb-dc-rack-count').textContent = dc.racks?.length ?? '—'
      document.getElementById('orb-dc-orb-id').textContent = dc.orbId || '—'
      document.getElementById('orb-dc-namespace').textContent = dc.namespace?.name || '—'
      document.getElementById('orb-dc-created-at').textContent = dc.createdAt || '—'
      document.getElementById('orb-dc-updated-at').textContent = dc.updatedAt || '—'

      const overrideBtn = document.getElementById('orb-dc-override-btn')
      if (overrideBtn) {
        overrideBtn.dataset.dcOverrideId = dc.id
        overrideBtn.dataset.dcOverrideOrbId = dc.orbId || ''
      }
      const submit = document.getElementById('orb-dc-override-submit')
      if (submit) {
        submit.dataset.dcId = dc.id
        submit.dataset.dcOrbId = dc.orbId || ''
      }
      const nameInput = document.getElementById('orb-dc-override-name')
      if (nameInput) nameInput.value = dc.name ?? ''
      const intentName = document.getElementById('orb-dc-intent-name')
      if (intentName) intentName.textContent = dc.name ?? ''

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
        if (data && data.id) {
          window.location = BASE + '/servers/' + data.id
        }
      })

      content.style.display = ''
    })
    .catch(() => {
      loading.style.display = 'none'
      empty.style.display = ''
    })
})

// ── Orb divergence publish ────────────────────────────────────────────────────

function publishDivergence() {
  const btn = document.getElementById('publish-btn')
  const toast = document.getElementById('publish-toast')
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

