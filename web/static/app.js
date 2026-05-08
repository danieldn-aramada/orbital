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
      hx-get="/servers/${itemId}" hx-trigger="click" hx-target="#tab-content-${itemId}" hx-swap="innerHTML">
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
        url: '/api/v1/servers',
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
      taserverTableble.ajax.url('/api/v1/servers').load(()=>{
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
      url: `/api/v1/servers/${serverId}/events`,
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
  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      const panelId = tab.dataset.panel
      tabs.forEach(t => t.classList.remove('is-active'))
      tab.classList.add('is-active')
      tabContainer.parentElement.querySelectorAll('[id^="dc-panel-"]').forEach(panel => {
        panel.style.display = panel.id === panelId ? '' : 'none'
      })
    })
  })
}

function loadDataCenterTab(displayName, id) {
  const tabHtml = `<li class="tab">
    <a id="tab-${id}" data-target="tab-content-${id}" role="tab" aria-selected="false" tabindex="-1"
      hx-get="/datacenters/${id}" hx-trigger="click" hx-target="#tab-content-${id}" hx-swap="innerHTML">
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

  tabLink.addEventListener('htmx:before-request', (e) => {
    if (tabContent.dataset.loaded) {
      e.preventDefault()
      return
    }
    showDatacenterSkeleton(id)
  })

  tabContent.addEventListener('htmx:after-swap', () => {
    tabContent.dataset.loaded = 'true'
    initDcDetailTabs(id)
  })

  const tabClose = document.getElementById(`tab-close-${id}`)
  tabClose.addEventListener('click', (event) => {
    event.stopPropagation()
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
      top2Start: {
        pageLength: { menu: [5, 10, 25, 50] },
      },
      topStart: {
        buttons: [
          { extend: 'excel', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.72rem;"><i class="fa-regular fa-file-excel"></i><span>Excel</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Excel' },
          { extend: 'csv', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.72rem;"><i class="fa-regular fa-file-text"></i><span>CSV</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'CSV' },
          { extend: 'copy', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.72rem;"><i class="fa-regular fa-copy"></i><span>Copy</span></span>', className: 'is-link is-outlined is-small', titleAttr: 'Copy' },
          { extend: 'colvis', text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.72rem;"><i class="fa fa-columns"></i><span>Select</span></span>', className: 'is-link is-small', titleAttr: 'Select Columns' },
          { text: '<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.72rem;"><i class="fa-solid fa-rotate-right"></i><span>Reload</span></span>', className: 'is-link is-small', titleAttr: 'Reload', name: 'reload', attr: { id: 'btn-reload-datacenters' } },
        ],
      },
      topEnd: { search: { placeholder: 'Type search here' } },
    },
    select: { style: 'os' },
    autoWidth: true,
    scrollX: true,
    scrollY: 500,
    scrollCollapse: true,
    language: {
      infoEmpty: 'No data centers to show',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'data centers', 1: 'data center' },
    },
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
      url: '/graphql',
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

document.addEventListener('DOMContentLoaded', () => {
  if (!document.getElementById('backup-table')) return

  document.querySelectorAll('li.tab a[data-target]').forEach((a) => {
    a.addEventListener('click', () => {
      activateTab(a.parentElement)
      displayTabContent(a.dataset.target)
      setCurrentTab(a.id)
    })
  })

  new DataTable('#backup-table', {
    layout: {
      topEnd: { search: { placeholder: 'Type search here' } },
    },
    autoWidth: true,
    scrollX: true,
    language: {
      infoEmpty: 'No backups to show',
      info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
      entries: { _: 'backups', 1: 'backup' },
    },
    columns: [
      { data: 'timestamp' },
      { data: 'schemaVersion' },
      { data: 'size' },
      { data: 'checksum' },
      { data: 'blobPath' },
    ],
    data: [],
  })
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
