const BASE = window.ORBITAL_BASE || '';

let serverTable = null;

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

  if (localStorage.tabs) {
    console.log('looking in existing entry')
    let s = new Set(JSON.parse(localStorage.tabs))
    if (s.has(JSON.stringify(tabToDelete))) {
      console.log('removing from existing entry')
      s.delete(JSON.stringify(tabToDelete))
    localStorage.tabs = JSON.stringify([...s])
    }
  } else {
    console.log('no entry.. error?')
  }
}

function saveTab(displayName, itemId) {
  let tabToAdd = new TabItem(displayName, itemId)

  if (localStorage.tabs) {
    console.log('existing entry')
    let s = new Set(JSON.parse(localStorage.tabs))
    if (!s.has(JSON.stringify(tabToAdd))) {
      console.log('adding to existing entry')
      s.add(JSON.stringify(tabToAdd))
    localStorage.tabs = JSON.stringify([...s])
    }
  } else {
    console.log('new entry')
    let s = new Set([JSON.stringify(tabToAdd)])
    localStorage.tabs = JSON.stringify([...s])
  }
}



// close tab and trigger reload
function closeTab(id){
  document.querySelector(`#tab-close-${id}`).click()
  document.querySelector(`#btn-reload-servers`).click()
}

// save current tab
function setCurrentTab(id) {
  localStorage.tabCurrent = id
}

function removeCurrentTab(id) {
  if (localStorage?.tabCurrent == id) {
    localStorage.removeItem(tabCurrent)
  }
}

function replaceCurrentTab(currentId, targetId) {
  if (localStorage?.tabCurrent == currentId) {
    localStorage.setItem('tabCurrent', targetId)
  }
}

// get current tab
function getCurrentTab() {
  return localStorage.tabCurrent
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

function loadServerTable() {
  if (serverTable) {
    serverTable.ajax.reload();
  } else {
    serverTable = new DataTable('#server-table', {
      pageLength: 15, 
      lengthMenu: [[15, 20, 50, -1], [15, 20, 50, "All"]],
      initComplete: function () {
        let pagination = document.querySelector('nav.pagination')
        if (pagination) {
          pagination.classList.add('is-small')
        }
      },
      drawCallback: function () {
        let pagination = document.querySelector('nav.pagination')
        if (pagination) {
          pagination.classList.add('is-small')
        }
      },
      layout: {
        topStart: {
          pageLength: {},
          buttons: [
            {
              extend: 'excel',
              text: '<span><i class="fa-regular fa-file-excel"></i></span>\n<span>Excel</span>',
              className: 'is-link is-small',
              titleAttr: 'Excel',
              title: '',
              filename: 'assets-servers'
            }, 
            {
              extend:'copy',
              className: 'is-link is-small',
              text: '<span><i class="fa-regular fa-copy"></i></span>\n<span>Copy</span>',
              titleAttr: 'Copy',
              title: null
            },
            {
              text: '<span><i class="fa-solid fa-rotate-right"></i></span>\n<span>Reload</span>',
              className: 'is-link is-small',
              titleAttr: 'Reload',
              name: 'reload',
              attr: {
                id: 'btn-reload-servers'
              }
            }
          ],
        },
        topEnd: {
          search: {
            placeholder: 'Type search here',
          }
        },
      },
      select: {
          style: 'os',
      },
      autoWidth: true,
      scrollX: false,
      scrollY: 700,
      scrollCollapse: true,
      language: {
        infoEmpty: 'No servers to show',
        info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
        entries: {
            _: 'servers',
            1: 'server',
        },
      },
      initComplete: function () { dtWrapLengthSelect(this.api()) },

      columns: [
        {
          data: 'galleonName',
          width: '10%'
        },
        {
          data: 'bmcIp',
          width: '12%'
        },
        {
          data: 'model',
          width: '10%'
        },
        {
          data: 'bootMode',
          width: '12%'
        },
        {
          data: 'bootSequence'
        },
        {
          data: 'pxeDevice1'
        },
      ],
      columnDefs: [
        {
          targets: 0,
        },
        {
          targets: 1,
        },
        {
          targets: 2,
          className: 'dt-head-left dt-body-left',
        },
        {
          targets: 3,
        },
        {
          targets: 4,
        },
        {
          targets: 5,
          className: 'dt-head-left dt-body-left',
        }
      ],
      ajax: {
        url: BASE + '/api/v1/servers',
        dataSrc: ''
      },
    });
  }
  serverTable.columns.adjust();

  $('#server-table').on('dblclick', 'tr', function () {
    // use object name as tab name 
    var displayName = this.cells[1].innerText   // bmcIP
    var id = serverTable.row(this).data().id
    console.log('tabId:', id)
    var tab = document.getElementById(`tab-${id}`)
    if (tab) {
      console.log('tab already opened so clicking it...')
      tab.click()
    } else {
      loadTab(displayName, id)
      saveTab(displayName, id)
      document.getElementById(`tab-${id}`).click()
    }
  });

  const reloadButton = serverTable.button('reload:name').node()
  serverTable.button('reload:name').node().on('click', function (e) {
    serverTable.clear().draw()
    reloadButton.addClass('is-loading')
    setTimeout(() => {
      taserverTableble.ajax.url(BASE + '/api/v1/servers').load(()=>{
          reloadButton.removeClass('is-loading')
      })
    }, 250);
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

  const skeletonRows = Array.from({ length: 10 }, () => `
    <tr>
      <td><span class="is-skeleton">10.20.21.00</span></td>
      <td><span class="is-skeleton">PowerEdge R750</span></td>
      <td><span class="is-skeleton">XXXXXXXXX</span></td>
      <td><span class="is-skeleton">r03-u14.houston-galleon</span></td>
      <td><span class="is-skeleton">A.OF.C.09</span></td>
      <td><span class="is-skeleton">00</span></td>
    </tr>`).join('')

  target.innerHTML = `
    <div class="fixed-grid has-3-cols mb-0">
      <div class="columns m-0">
        <div class="column pt-0 pl-0">
          <button class="button is-rounded is-small is-warning mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-gauge-high"></i></span>
            <span>Grafana</span>
          </button>
          <button class="button is-rounded is-small is-link mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-refresh"></i></span>
            <span>Reload</span>
          </button>
          <button class="button is-rounded is-small is-link mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-pen-to-square"></i></span>
            <span>Edit</span>
          </button>
          <button class="button is-rounded is-small is-danger mt-1" disabled>
            <span class="icon"><i class="fa-solid fa-trash"></i></span>
            <span>Delete</span>
          </button>
        </div>
      </div>

      <div class="grid mt-2">
        <div class="cell is-col-span-2">
          <article class="box">
            <p class="is-size-4 pb-4">Data Center Summary</p>
            <table class="table is-fullwidth">
              <tbody>
                <tr><td style="width:40%">Name</td><td><span class="is-skeleton">colo-galleon</span></td></tr>
                <tr><td>Servers</td><td><span class="is-skeleton">00</span></td></tr>
                <tr><td>Created By</td><td><span class="is-skeleton">admin@example.com</span></td></tr>
                <tr><td>Created At</td><td><span class="is-skeleton">2024-01-01 00:00:00</span></td></tr>
              </tbody>
            </table>
          </article>
        </div>
        <div class="cell">
          <article class="box" style="height:100%">
            <p class="is-size-4 mb-4">Metadata</p>
            <table class="table mb-0">
              <tbody>
                <tr><td style="width:40%">Namespace</td><td><span class="is-skeleton">colo</span></td></tr>
                <tr><td>Orb ID</td><td><span class="is-skeleton">colo:colo-galleon</span></td></tr>
              </tbody>
            </table>
          </article>
        </div>
        <div class="cell is-col-span-3">
          <article class="box pb-2">
            <p class="is-size-4 pb-4">Details</p>
            <table class="table is-striped is-fullwidth is-size-7 mt-2">
              <thead>
                <tr>
                  <th>OOB IP</th><th>Model</th><th>Service Tag</th><th>Hostname</th><th>Rack</th>
                  <th>Rack Position</th>
                </tr>
              </thead>
              <tbody>${skeletonRows}</tbody>
            </table>
          </article>
        </div>
      </div>
    </div>
  </div>`
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
    htmx.ajax('GET', BASE + `/api/v1/events?orbId=${encodeURIComponent(orbId)}&limit=50`, {
      target: `#${auditPanelId}`,
      swap: 'innerHTML',
    })
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
    htmx.ajax('GET', BASE + `/api/v1/events?orbId=${encodeURIComponent(orbId)}&limit=50`, {
      target: `#${auditPanelId}`,
      swap: 'innerHTML',
    })
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
    <a id="tab-${id}" data-target="tab-content-${id}" role="tab" aria-selected="false" tabindex="-1"
      hx-get="${BASE}/datacenters/${id}" hx-trigger="click" hx-target="#tab-content-${id}" hx-swap="innerHTML">
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

  htmx.process(document.querySelector(`#tab-${id}`))

  const tabLink = document.getElementById(`tab-${id}`)
  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(`tab-content-${id}`)
    setCurrentTab(`tab-${id}`)
  })
  const tabContent = document.getElementById(`tab-content-${id}`)

  tabLink.addEventListener('htmx:beforeRequest', (e) => {
    if (tabContent.dataset.loaded) {
      e.preventDefault()
      return
    }
    showDatacenterSkeleton(id)
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
    language: {
      infoEmpty: 'No data centers to show',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'data centers', 1: 'data center' },
    },
    initComplete: function () { dtWrapLengthSelect(this.api()) },
    columns: [
      { data: 'name' },
      { data: 'serverCount' },
      { data: 'createdBy' },
      { data: 'createdAt' },
      { data: 'id' },
    ],
    columnDefs: [
      { targets: 0 },
      { targets: 1, className: 'dt-body-left dt-head-left' },
      { targets: 2 },
      { targets: 3 },
      { targets: 4, visible: false },
    ],
    ajax: {
      url: BASE + '/graphql',
      type: 'POST',
      contentType: 'application/json',
      data: () => JSON.stringify({ query: `{ queryDataCenter { id name createdBy createdAt serversAggregate { count } } }` }),
      dataSrc: (json) => (json.data?.queryDataCenter ?? []).map(dc => ({
        id: dc.id,
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

  const serverListTable = new DataTable('#server-list-table', {
    layout: {
      topStart: [
        { pageLength: { menu: [10, 25, 50, 100] } },
        { buttons: [
          { extend: 'excel', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-excel"></i><span>Excel</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Excel' },
          { extend: 'csv', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-file-text"></i><span>CSV</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'CSV' },
          { extend: 'copy', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-regular fa-copy"></i><span>Copy</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Copy' },
          { extend: 'colvis', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa fa-columns"></i><span>Select</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Select Columns' },
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-servers' } },
        ] },
      ],
      topEnd: { search: { placeholder: 'Type search here' } },
    },
    select: { style: 'os' },
    autoWidth: true,
    scrollX: true,
    scrollY: 400,
    scrollCollapse: true,
    language: {
      infoEmpty: 'No servers to show',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'servers', 1: 'server' },
    },
    initComplete: function () { dtWrapLengthSelect(this.api()) },
    columns: [
      { data: 'dataCenter' },
      { data: 'oobIP' },
      { data: 'hostname' },
      { data: 'serviceTag' },
      { data: 'model' },
      { data: 'rack' },
      { data: 'id' },
    ],
    columnDefs: [
      { targets: 6, visible: false },
    ],
    ajax: {
      url: BASE + '/graphql',
      type: 'POST',
      contentType: 'application/json',
      data: () => JSON.stringify({
        query: `{ queryServer {
          id hostname serviceTag model
          oobIP { address }
          rack { name }
          dataCenter { name }
        } }`,
      }),
      dataSrc: (json) => (json.data?.queryServer ?? []).map(s => ({
        id: s.id,
        hostname: s.hostname ?? '—',
        serviceTag: s.serviceTag ?? '—',
        model: s.model ?? '—',
        oobIP: s.oobIP?.address ?? '—',
        rack: s.rack?.name ?? '—',
        dataCenter: s.dataCenter?.name ?? '—',
      })),
    },
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

  if (!localStorage.serverTabs) return
  const tabSet = new Set(JSON.parse(localStorage.serverTabs))
  tabSet.forEach(tabData => {
    const { displayName, id } = JSON.parse(tabData)
    loadServerListTab(displayName, id)
  })
  const currentTabId = getCurrentTab()
  if (currentTabId) {
    document.getElementById(currentTabId)?.click()
  }
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
        ? `<a class="button is-small is-light" onclick="downloadBackup('${j.id}')" title="Download"><span class="icon"><i class="fas fa-download"></i></span></a>`
        : '',
      canDelete
        ? `<a class="button is-small is-light has-text-danger" onclick="openDeleteModal('${j.id}', '${new Date(j.initiatedAt).toLocaleString()}')" title="Delete"><span class="icon"><i class="fas fa-trash"></i></span></a>`
        : '',
    ].join('')
    return `<tr>
      <td>${new Date(j.initiatedAt).toLocaleString()}</td>
      <td>${statusCell}</td>
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
    localStorage.removeItem('tabs')
    localStorage.removeItem('tabCurrent')
    history.replaceState(null, '', '/')
  }

  if (!localStorage.tabs) return
  const tabSet = new Set(JSON.parse(localStorage.tabs))
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
              actions.push(`<a class="button is-small is-link is-outlined" href="${BASE}/api/v1/export/jobs/${job.jobId}/download"><span class="icon"><i class="fa-solid fa-download"></i></span><span>Download</span></a>`)
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
              <td>${statusCell}</td>
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
        ? '<tr><td colspan="8" class="has-text-grey">No artifacts yet.</td></tr>'
        : artifacts.map(a => `<tr>
            <td>${a.datacenterName}</td>
            <td style="font-family:monospace;font-size:0.7rem">${a.repository}</td>
            <td><span class="tag is-light">${a.tag}</span></td>
            <td style="white-space:nowrap;">${a.digest ? `<div style="display:flex;align-items:center;gap:0.25rem;"><span class="is-family-monospace is-size-7">${a.digest.substring(0, 19)}…</span><button class="button is-small is-white" title="Copy digest" onclick="navigator.clipboard.writeText('${a.digest}').then(()=>{this.innerHTML='<span class=\\'icon\\'><i class=\\'fas fa-check\\'></i></span>';setTimeout(()=>{this.innerHTML='<span class=\\'icon\\'><i class=\\'fas fa-copy\\'></i></span>';},1200)})"><span class="icon"><i class="fas fa-copy"></i></span></button></div>` : '—'}</td>
            <td>${a.signed ? '<span class="tag is-success is-light">signed</span>' : '<span class="tag is-light">unsigned</span>'}</td>
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

const dcEditors = new Map()

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
          htmx.ajax('GET', BASE + '/datacenters/' + id, { target: '#tab-content-' + id, swap: 'innerHTML' })
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
          const resp = await fetch(BASE + '/graphql', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              query: `mutation UpdateServer(
                $id: ID!, $hostname: String, $manufacturer: String, $model: String,
                $oobMAC: String, $rackPosition: Int, $serviceTag: String,
                $version: Int, $updatedBy: String!, $updatedAt: DateTime!
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
          srvEditors.delete(id)
          htmx.ajax('GET', modal.dataset.reloadUrl, { target: '#' + modal.dataset.reloadTarget, swap: 'innerHTML' })
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

const skipVars = new Set(['updatedBy', 'updatedAt'])

function formatGQL(query) {
  let indent = 0
  let out = ''
  let i = 0
  while (i < query.length) {
    const ch = query[i]
    if (ch === '{') {
      out += ' {\n' + '  '.repeat(++indent)
    } else if (ch === '}') {
      out = out.trimEnd()
      out += '\n' + '  '.repeat(--indent) + '}'
    } else if (ch === ',' && query[i + 1] === ' ') {
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
    autoWidth: true,
    scrollX: true,
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

// Clear all tab state and localStorage on logout
document.addEventListener('DOMContentLoaded', () => {
  const logoutForm = document.querySelector('form[action$="/user/logout"]')
  if (!logoutForm) return
  logoutForm.addEventListener('submit', () => {
    localStorage.clear()
    sessionStorage.clear()
  })
})
