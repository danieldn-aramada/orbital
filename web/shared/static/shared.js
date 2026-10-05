// shared.js — utilities used by both orbital and orb

export const BASE = window.ORBITAL_BASE || ''

// AUDIT_PANEL_LIMIT mirrors layout.AuditPanelDefaultLimit from the Go side
// (internal/web/data/layout/ui.go). Injected via window.ORBITAL_CONFIG in
// base.gohtml; the fallback covers the no-template case (e.g. unit tests).
const AUDIT_PANEL_LIMIT = (window.ORBITAL_CONFIG && window.ORBITAL_CONFIG.auditPanelLimit) || 200

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

// Mirror the OS `prefers-color-scheme` onto <html class="dark"> — the hook
// DataTables' bundled CSS keys its dark theme on (`html.dark ...` /
// `:root[data-bs-theme=dark]`). Orbital drives dark mode via prefers-color-scheme,
// so without this the DataTables-provided chrome (sort arrows, responsive detail
// modal, child-row borders) stays light while the rest of the page flips. Runs at
// module load — before any DOMContentLoaded DataTables init.
{
  const mq = window.matchMedia('(prefers-color-scheme: dark)')
  const apply = () => document.documentElement.classList.toggle('dark', mq.matches)
  apply()
  mq.addEventListener('change', apply)
}

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

// getTabStorageKey names where "which tab was I on" is stored for THIS page.
//
// Per SLUG on the generic list page, because /servers, /clusters, /data-centers
// and /network-devices are all the same page: one shared key means the tab you
// left open on one page is looked up on another, where that element does not
// exist, and the restore silently does nothing. `genericTabs` has been
// slug-qualified since it was written; this is the same fact, finally spelled
// the same way.
//
// The old `#server-list-table` branch was removed with the page it named — it
// survived the migration as dead code, which is how EVERY generic list page
// ended up writing the data-centre page's key.
export function getTabStorageKey() {
  const generic = document.getElementById('generic-table')
  if (generic) return `${GENERIC_TAB_CURRENT_PREFIX}${generic.dataset.slug}`
  return 'dcTabCurrent'   // orb still has a hand-written data-centre page
}

export const GENERIC_TAB_CURRENT_PREFIX = 'genericTabCurrent:'

export function setCurrentTab(id) {
  localStorage[getTabStorageKey()] = id
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

// displayTabContent shows one tab panel and hides the others.
//
// ⚠️ Only panels that HAVE an id participate. `.tab-content` is also the house
// layout wrapper (main.scss gives it the box and min-width rules), so the detail
// FRAGMENT swapped into a tab opens with its own class="tab-content" and no id.
// Hiding that one was permanent damage: this function can only ever un-hide by
// matching an id, so an id-less element it hides can never come back — the
// panel stayed display:block around a child that was display:none, and every
// detail tab went blank the moment a second one was opened. An element with no
// id cannot be the target, so there is never a reason to touch it.
export function displayTabContent(id) {
  ;(document.querySelectorAll('.tab-content') || []).forEach((tabContent) => {
    if (!tabContent.id) return
    tabContent.style.display = tabContent.id === id ? 'block' : 'none'
  })
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
            <div class="table-container"><table class="table is-fullwidth"><tbody>${summary}</tbody></table></div>
          </article>
        </div>
        <div class="cell is-row-span-1">
          <article class="box" style="height:100%">
            <p class="is-size-4 mb-4">Metadata</p>
            <div class="table-container"><table class="table mb-0"><tbody>${meta}</tbody></table></div>
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
            <div class="table-container"><table class="table is-fullwidth"><tbody>${rows}</tbody></table></div>
          </article>
        </div>
        <div class="cell is-row-span-1">
          <article class="box" style="height:100%">
            <p class="is-size-4 mb-4">Metadata</p>
            <div class="table-container"><table class="table mb-0"><tbody>${meta}</tbody></table></div>
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

// initDetailTabs wires the standard "detail-tabs with optional lazy-loaded
// Audit Log tab" pattern used by DC, Server, and Cluster detail pages.
//
// Conventions enforced by this function:
//   • tabContainer is the <div class="tabs is-boxed" id="…"> element.
//   • Each tab <li> declares its panel target via data-panel="…", and the
//     matching <div id="…"> lives as a sibling of tabContainer.
//   • The audit tab is identified by carrying data-orb-id (no other tab does).
//     Optional data-related-orb-ids carries a CSV of subgraph orbIds so the
//     audit panel can aggregate events for the parent and its nested items.
//   • Active-tab persistence is keyed off tabContainer.id, which is already
//     unique on the page and already prefixed by page family.
//
// Options:
//   scoped  — default true. When true, panel lookups scope to
//             tabContainer.parentElement instead of document. This protects
//             against the dual-rendering case where the same entity ID appears
//             twice on the page (e.g. a server tab open both standalone and
//             drilled in from a DC tab). Both DOM trees contain identically-
//             ID'd panels; an unscoped getElementById would update the wrong
//             one, leaving the visible tab nav out of sync with its content.
//   storage — 'local' (default) or 'session'. The active tab is restored on
//             page reload from the chosen storage.
export function initDetailTabs(tabContainer, options = {}) {
  if (!tabContainer) return
  const { scoped = true, storage = 'local' } = options

  const tabs = tabContainer.querySelectorAll('li[data-panel]')
  const storageObj = storage === 'session' ? sessionStorage : localStorage
  const storageKey = `tab-active:${tabContainer.id}`

  const scope = tabContainer.parentElement
  const findPanel = scoped
    ? (id) => scope.querySelector('#' + CSS.escape(id))
    : (id) => document.getElementById(id)

  const auditTab = [...tabs].find(t => t.dataset.orbId)
  const auditPanelId = auditTab ? auditTab.dataset.panel : null

  const panelPairs = [...tabs].map(t => ({
    tab: t,
    panel: findPanel(t.dataset.panel),
  })).filter(p => p.panel)

  function loadAuditPanel(before) {
    if (!auditTab) return
    const panel = findPanel(auditPanelId)
    if (!panel) return
    // Re-pin once the rows arrive: the panel is empty when the click is
    // handled, so the height it settles at is not the height we corrected
    // against, and the strip drifts by however many rows came back.
    loadAuditPanelForTab(auditTab, panel, () => {
      if (before === undefined) return
      pinStrip(before)
      reserveShortfall(before, tabContainer.parentElement)
    })
  }

  function activatePanel(panelId) {
    // Pin the tab strip in the viewport across the switch.
    //
    // Panels differ enormously in height — a data centre's Servers tab is 190
    // rows and its Racks tab is 24 — so swapping one for the other collapses
    // the page by over a thousand pixels, the browser clamps scrollY to the
    // new maximum, and the strip you just clicked flies 569px down the screen.
    // Measured on /data-centers/colo:colo-galleon: 2387px → 961px.
    //
    // This is not new behaviour in this function; the hand-written pages used
    // it unchanged. It only became jarring when the panel set went from four
    // hand-picked tabs of similar size to one derived per relationship.
    const before = tabContainer.getBoundingClientRect().top
    // Cleared first so each switch measures the real content height rather
    // than whatever the previous switch reserved.
    const host = tabContainer.parentElement
    if (host) host.style.removeProperty('min-height')

    for (const { tab, panel } of panelPairs) {
      if (tab.dataset.panel === panelId) {
        tab.classList.add('is-active')
        panel.style.removeProperty('display')
      } else {
        tab.classList.remove('is-active')
        panel.style.setProperty('display', 'none')
      }
    }
    if (panelId === auditPanelId) loadAuditPanel(before)
    storageObj.setItem(storageKey, panelId)

    // After the reflow, put the strip back where it was. Instant, not smooth:
    // this is a correction, and animating it would BE the jump.
    pinStrip(before)

    // Scrolling back is not always possible. Switching a 190-row panel for a
    // 24-row one can leave the document SHORTER than the scroll position it
    // was at, so the browser clamps scrollY and the strip stays displaced
    // however much we ask it to move — 568px on a data centre page.
    //
    // So reserve the shortfall, and only the shortfall: extend the panel host
    // just enough that the strip can sit where it did. A short panel on an
    // otherwise short page reserves nothing, which is why this is not a blanket
    // min-height — reserving the tallest panel's height everywhere would put
    // 1,400px of whitespace under a four-row table.
    reserveShortfall(before, host)
  }

  // pinStrip scrolls so the tab strip sits where it did before the switch.
  function pinStrip(before) {
    const delta = tabContainer.getBoundingClientRect().top - before
    if (delta) window.scrollBy({ top: delta, behavior: 'instant' })
  }

  function reserveShortfall(before, host) {
    const off = tabContainer.getBoundingClientRect().top - before
    if (Math.abs(off) <= 1 || !host) return
    host.style.minHeight = (host.offsetHeight + Math.abs(off)) + 'px'
    pinStrip(before)
  }

  for (const { tab } of panelPairs) {
    tab.addEventListener('click', () => activatePanel(tab.dataset.panel))
  }

  const saved = storageObj.getItem(storageKey)
  if (saved && panelPairs.some(p => p.tab.dataset.panel === saved)) {
    activatePanel(saved)
  }
}

// splitOrbIds parses a data-related-orb-ids CSV, falling back to a single orbId
// when the attribute is absent or empty.
function splitOrbIds(csv, fallback) {
  const ids = String(csv || '').split(',').map(s => s.trim()).filter(Boolean)
  if (ids.length) return ids
  return fallback ? [fallback] : []
}

// subtreeOrbIds returns a rendered ConfigItem's orbId plus every orbId it owns,
// read from the data-related-orb-ids the page handler already emits
// (collectRelatedOrbIDs in Go — see AUDIT.md, the page composer owns the
// parent→child knowledge, not the API).
//
// Any per-node query about "this item" needs this list, not the bare orbId: a
// change to an owned child records the CHILD's orbId and never the parent's, so
// asking about the parent alone answers "nothing here" while something is in
// flight. Both consumers — the audit panel and the pending-change lookups — read
// the same attribute so they can never disagree about what the item covers.
export function subtreeOrbIds(orbId, scope = document) {
  if (!orbId) return []
  // Attribute-selector values are quoted, so only " and \ need escaping;
  // CSS.escape would mangle the ":" every orbId carries.
  const q = String(orbId).replace(/(["\\])/g, '\\$1')
  const el = scope.querySelector(`[data-related-orb-ids][data-orb-id="${q}"]`)
  return splitOrbIds(el && el.dataset.relatedOrbIds, orbId)
}

function loadAuditPanelForTab(tab, panel, onLoaded) {
  // data-related-orb-ids embeds the full subgraph (parent + nested ConfigItems)
  // so one fetch pulls all relevant events. Falls back to data-orb-id alone.
  const related = splitOrbIds(tab.dataset.relatedOrbIds, tab.dataset.orbId)
  if (related.length === 0) return
  const qs = related.map(id => `orbId=${encodeURIComponent(id)}`).join('&')
  fetch(BASE + `/api/v1/audit-log?${qs}&limit=${AUDIT_PANEL_LIMIT}`, {
    headers: { 'HX-Request': 'true' },
  })
    .then(r => r.text())
    .then(html => { panel.innerHTML = html; renderTimestamps(panel); onLoaded && onLoaded() })
    .catch(() => {})
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

  // The generic detail page's tab strip — owned children, relationship
  // tables, then the audit log. Wired here for a fragment opened as a tab on a
  // list page, and on DOMContentLoaded in orbital.js/orb.js for a direct
  // navigation.
  //
  // This replaced four branches keyed on dc-/cluster-/network-device-/srv-
  // detail-tabs ids, none of which existed in any template any more: those
  // pages became schema-derived and their markup went with them, leaving the
  // handlers looking for elements that could never appear.
  const genericDetailTabs = target.querySelector('[id^="generic-detail-tabs-"]')
  if (genericDetailTabs) {
    target.dataset.loaded = 'true'
    initDetailTabs(genericDetailTabs)
    return
  }
})

// ─── Todo toast ───────────────────────────────────────────────────────────────

// displayTodoToast is the placeholder for a feature that is deliberately not
// built yet (see the Ratel decision in CLAUDE.md — a sub-path proxy cannot work,
// so the link shows this instead of 404ing).
//
// It lived in an inline <script> in todo-toast.gohtml and was called from here
// as a BARE GLOBAL — a module reaching out to a function a template happened to
// define. It worked, and nothing said it had to keep working.
function displayTodoToast() {
  window.bulmaToast.toast({
    message: "🚧 Hang tight! We are currently building this feature.",
    closeOnClick: true,
    type: "is-warning is-light",
    position: "top-right",
    dismissible: false,
    duration: 4000,
    offsetTop: "4em",
    pauseOnHover: true,
    animate: { in: 'fadeInRight', out: 'fadeOutRight' },
  })
}

document.addEventListener('click', (e) => {
  if (e.target.closest('.todo')) displayTodoToast()
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
    // The generic detail page's tab container, not the deleted DC page's:
    // closing a tab must clear the active-panel memory for the markup that
    // actually renders, or the next open restores a panel id nothing matches.
    localStorage.removeItem(`tab-active:generic-detail-tabs-${domId}`)
    unloadTab(orbId)
    deleteTab(displayName, orbId)
    document.getElementById('tab-summary').click()
    replaceCurrentTab(`tab-${domId}`, 'tab-summary')
  })
}

export function deleteServerTab(displayName, id) {
  const item = JSON.stringify(new TabItem(displayName, id))
  const s = new Set(localStorage.serverTabs ? JSON.parse(localStorage.serverTabs) : [])
  s.delete(item)
  localStorage.serverTabs = JSON.stringify([...s])
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
  localStorage.removeItem('clusterTabs')    // was MISSING — see below
  localStorage.removeItem('networkTabs')    // was MISSING — see below
  localStorage.removeItem('genericTabs')    // every /{slug} page shares this one
  localStorage.removeItem('dcTabCurrent')
  localStorage.removeItem('crTabCurrent')   // change-request queue's active tab
  // Swept by PREFIX, not named one by one: the active-tab key is per slug, so
  // enumerating them would reintroduce exactly the omission this function was
  // already caught by once — a key added for a new page that nobody remembers
  // to list here.
  for (const k of Object.keys(localStorage)) {
    if (k.startsWith(GENERIC_TAB_CURRENT_PREFIX)) localStorage.removeItem(k)
  }

  // clusterTabs and networkTabs were absent until 2026-09-25, so a cluster or
  // network-device tab opened by one user survived login on a shared machine
  // and reappeared for the next — the exact thing this function exists to
  // prevent, missed for two of the four tab-bearing pages because each page
  // added its own key and nothing enumerated them.
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

// ─── Inventory / DataCenter / Server list tables ──────────────────────────────
//
// These three DataTables are shared between orbital and orb. The core
// DataTable construction, columns, ajax, and dataSrc are identical because
// both apps render the same DGraph schema. App-specific behaviors (row
// double-click action, currently: orbital opens a tab, orb navigates to a
// detail page) are injected via the `opts` callbacks.

export const INVENTORY_CACHE_KEY = 'inventoryCache'

// usableInventoryRows splits inventory rows into the ones a table can render
// and a count of the ones it cannot.
//
// A node keeps its `dgraph.type` when its type is removed from the GraphQL
// schema, so `queryConfigItem` still matches it through the ConfigItem
// interface — but every field on it now resolves to nothing, including
// `__typename`. DataTables reads a missing column as a FATAL error and blocks
// the whole page with an alert() naming only its own documentation, so one
// leftover node makes the entire inventory unreachable.
//
// Dropped, never silently: the caller renders the count and the reason. Same
// rule as the capped-fetch notice — a list that quietly omits rows looks
// exactly like a complete one.
export function usableInventoryRows(items) {
  const rows = [], unrenderable = []
  for (const it of items || []) {
    if (it && typeof it.type === 'string' && it.type !== '') rows.push(it)
    else unrenderable.push(it)
  }
  return { rows, unrenderable }
}

// The notice naming which types are hidden is rendered SERVER-SIDE (see
// page.Home.Orphans). It cannot be built here: for these rows every field
// resolves to nothing, `__typename` included, so the client has a count and no
// names — which is what made the first version of this banner useless. The JS
// keeps only the half it can do, which is not feeding DataTables a row it
// treats as fatal.

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
      const { rows } = usableInventoryRows(items)
      // The CACHE stores the usable rows only. Caching the raw response would
      // re-poison every later page load from sessionStorage, where no fetch
      // runs to filter it again.
      sessionStorage.setItem(INVENTORY_CACHE_KEY, JSON.stringify(rows))
      onData(rows)
    })
}

// initInventoryTable initializes the inventory DataTable on pages that have
// #inventory-table. Pure read-only display — no app-specific hooks needed.
export function initInventoryTable() {
  if (!document.getElementById('inventory-table')) return

  const savedType = localStorage.getItem('inventoryTypeFilter') || ''
  const savedNamespace = localStorage.getItem('inventoryNamespaceFilter') || ''
  const cachedRaw = sessionStorage.getItem(INVENTORY_CACHE_KEY)
  // Filtered on the way OUT of the cache as well as in. A cache written before
  // this guard existed still holds the bad row, and it survives a reload —
  // which is exactly the state someone hitting this bug is already in.
  const cachedSplit = usableInventoryRows(cachedRaw ? JSON.parse(cachedRaw) : [])
  const initialData = cachedSplit.rows
  // Written back, not just filtered in memory. A cache poisoned before this
  // guard existed would otherwise stay poisoned for the life of the session —
  // and the cached path performs no fetch, so nothing else would ever clean it.
  if (cachedSplit.unrenderable.length) {
    sessionStorage.setItem(INVENTORY_CACHE_KEY, JSON.stringify(cachedSplit.rows))
  }
  // An EMPTY cached array is not a cache hit. "[]" is a truthy string, so a page
  // loaded while the graph was empty — between `make up` and `make seed`, or
  // before a restore finishes — used to cache [], skip the fetch on every
  // subsequent load, and show "No data available in table" indefinitely while
  // /servers and /datacenters (which always fetch) showed data.
  const cached = initialData.length > 0

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
    // Use `this.api()`, never the outer `inventoryTable` const. With
    // stateSave:true DataTables restores saved state DURING the constructor
    // (_fnLoadState -> _fnImplementState -> _fnInitComplete), so this callback
    // can run before `const inventoryTable = new DataTable(...)` has bound —
    // touching it then throws a TDZ ReferenceError out of the constructor, and
    // everything after it in initInventoryTable (including the data fetch)
    // never runs. The table then sits empty forever. Only reproduces once a
    // saved state exists, which is why it hid on a fresh profile.
    initComplete: function () {
      const api = this.api()
      dtWrapLengthSelect(api)

      document.getElementById('inventory-type-select').addEventListener('change', function () {
        localStorage.setItem('inventoryTypeFilter', this.value)
        api.column(0).search(this.value, { exact: !!this.value }).draw()
      })

      const nsSelect = document.getElementById('inventory-namespace-select')
      if (nsSelect) {
        nsSelect.addEventListener('change', function () {
          localStorage.setItem('inventoryNamespaceFilter', this.value)
          applyNamespaceFilter(this.value, api)
        })
      }

      if (savedNamespace) applyNamespaceFilter(savedNamespace, api)
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

  // `table` is passed by callers that may run before the outer const binds.
  function applyNamespaceFilter(ns, table) {
    ;(table || inventoryTable).column(1).search(ns ? '^' + ns + ':' : '', { regex: true }).draw()
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
      topEnd: { search: { placeholder: 'Search data centers' } },
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
      const onError = () => reloadButton.removeClass('is-loading')
      datacenterTable.one('error.dt', onError)
      datacenterTable.ajax.reload(() => {
        datacenterTable.off('error.dt', onError)
        reloadButton.removeClass('is-loading')
      })
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

export function deleteClusterTab(displayName, orbId) {
  const item = JSON.stringify(new TabItem(displayName, orbId))
  const s = new Set(localStorage.clusterTabs ? JSON.parse(localStorage.clusterTabs) : [])
  s.delete(item)
  localStorage.clusterTabs = JSON.stringify([...s])
}

export function deleteNetworkDeviceTab(displayName, orbId) {
  const item = JSON.stringify(new TabItem(displayName, orbId))
  const s = new Set(localStorage.networkTabs ? JSON.parse(localStorage.networkTabs) : [])
  s.delete(item)
  localStorage.networkTabs = JSON.stringify([...s])
}

// ─── List-page wiring (shared by orbital and orb) ─────────────────────────────
//
// Every entity list page opens a detail tab the same way: look for an already
// open tab, focus it if present, otherwise load + persist + focus. That logic
// was copy-pasted once per entity in BOTH orbital.js and orb.js — three
// identical 17-line DOMContentLoaded blocks in each, ~60 duplicated lines, while
// shared.js already exported every function they called. A change to the
// open-tab sequence landed in one file and silently missed the other, which is
// the failure mode UI.md's "any new navigation pattern belongs in these
// functions so both apps get it automatically" rule exists to prevent.
//
// Each init*Table returns early when its table is absent, so wiring all of them
// in both apps is safe: a page only activates the descriptor that matches it.
const LIST_PAGES = [
  {
    init: initDatacenterTable,
    tabId: (domId) => `tab-${domId}`,
    load: loadDataCenterTab,
    save: saveTab,
    restore: initDatacenterTabRestoration,
    label: (d) => d.name,
  },
]

// openOrFocusTab is the shared row-open behaviour: focus an open tab, or load,
// persist and focus a new one.
function openOrFocusTab(pageDef, data) {
  const orbId = data.orbId
  const domId = safeDomId(orbId)
  const id = pageDef.tabId(domId)
  const existing = document.getElementById(id)
  if (existing) {
    existing.click()
    return
  }
  const displayName = pageDef.label(data)
  pageDef.load(displayName, orbId)
  pageDef.save(displayName, orbId)
  document.getElementById(id)?.click()
}

// initListPages wires the inventory table and every entity list page. Called
// once by orbital.js and once by orb.js; nothing else should duplicate it.
// ─── Modal close (all modals, both apps) ────────────────────────────────────
//
// One handler for every modal that opts in with data-modal-close — the edit
// modals in orbital and the OCI layers modal in both apps. There were four byte-identical copies, one
// per ConfigItem family, each reconstructing the modal's id from a per-type
// data attribute — `data-srv-modal-close`, `data-dc-modal-close` and so on.
//
// None of that was needed: the close button is INSIDE the modal it closes, so
// `closest('.modal')` finds it without an id. The per-type attribute names also
// could not survive template consolidation — html/template refuses to
// interpolate into an attribute NAME, so `data-{{.Prefix}}-modal-close` renders
// nothing at all (caught by TestEditModalPrefixIsConsistent).
document.addEventListener('click', (e) => {
  const closeBtn = e.target.closest('[data-modal-close]')
  if (!closeBtn) return
  const modal = closeBtn.closest('.modal')
  if (!modal) return
  modal.classList.remove('is-active')
  document.documentElement.style.overflow = ''
})

// ─── Reload, on any generic detail view (both apps) ──────────────────────────
//
// The detail route serves BOTH a full page and — under HX-Request — just its
// body, so one button has to mean two things. It resolves which at CLICK time
// by looking for the tab panel it is sitting in rather than being told by the
// template: a server-rendered flag would have to know whether this render was a
// page or a fragment, and the whole point of the shared block is that it does
// not. Re-fetching the SAME url the tab was opened with means there is no
// second endpoint to drift.
document.addEventListener('click', (e) => {
  const btn = e.target.closest('.js-generic-reload')
  if (!btn) return
  e.preventDefault()
  const panel = btn.closest('.tab-content[id^="tab-content-generic-"]')
  if (!panel) {
    window.location.reload()
    return
  }
  const url = panel.dataset.detailUrl
  if (!url) {
    window.location.reload()
    return
  }
  btn.classList.add('is-loading')
  // The swap replaces the modal any open editor is bound to, so drop the cached
  // instance first — otherwise the next Edit click reuses an editor pointing at
  // a detached node and the modal opens empty.
  const editId = panel.querySelector('[data-generic-edit-id]')?.dataset.genericEditId
  if (editId) window.genericEditors?.delete(editId)
  // Promise.resolve wraps it rather than chaining directly: htmx.ajax returns a
  // promise only on the public 3-arg form, and a spinner that never stops is a
  // worse failure than one that never starts.
  Promise.resolve(htmx.ajax('GET', url, { target: panel, swap: 'innerHTML' }))
    .finally(() => btn.classList.remove('is-loading'))
})

// initGenericTable wires the DataTable on the generic list page (/{slug}).
//
// The bespoke list pages each fetch their own rows and register per-type
// behaviour in LIST_PAGES. This page cannot: it serves every ConfigItem type
// from one template, and there is nothing per-type for JS to know. Rows are
// rendered server-side from the view's derived field list, and DataTables is
// initialised over the existing DOM purely for sort/search/colvis/export — the
// same toolbar the hand-written pages get, so a derived page does not read as a
// lesser one.
// ─── Generic tabs ─────────────────────────────────────────────────────────────
//
// ONE implementation for every ConfigItem type, replacing four per-type sets of
// loadXTab / saveXTab / deleteXTab / initXTabRestoration that differ only by a
// prefix and a localStorage key.
//
// The detail route serves the tab body itself, as a fragment, on HX-Request —
// the same URL you would navigate to. There is no separate fragment endpoint to
// keep in step.

// genericTabKey identifies an open tab. Slug-qualified because two types can
// hold entities with the same orbId shape, and a tab restored onto the wrong
// page would fetch a 404.
function genericTabKey(slug, orbId) {
  return `${slug}|${orbId}`
}

function genericTabDomId(slug, orbId) {
  return `${slug}-${safeDomId(orbId)}`
}

function readGenericTabs() {
  try {
    return JSON.parse(localStorage.genericTabs || '[]')
  } catch (_) {
    return []
  }
}

export function saveGenericTab(label, slug, orbId) {
  const tabs = readGenericTabs().filter(t => genericTabKey(t.slug, t.orbId) !== genericTabKey(slug, orbId))
  tabs.push({ label, slug, orbId, touchedAt: Date.now() })
  localStorage.genericTabs = JSON.stringify(tabs)
}

// touchGenericTab records that a tab was just looked at, which is what makes
// eviction least-recently-USED rather than least-recently-opened. Survives a
// reload with the rest of the tab list, so returning to a tab across page loads
// keeps protecting it.
function touchGenericTab(slug, orbId) {
  const key = genericTabKey(slug, orbId)
  const tabs = readGenericTabs()
  const hit = tabs.find(t => genericTabKey(t.slug, t.orbId) === key)
  if (!hit) return
  hit.touchedAt = Date.now()
  localStorage.genericTabs = JSON.stringify(tabs)
}

export function deleteGenericTab(slug, orbId) {
  const tabs = readGenericTabs().filter(t => genericTabKey(t.slug, t.orbId) !== genericTabKey(slug, orbId))
  localStorage.genericTabs = JSON.stringify(tabs)
}

// MAX_GENERIC_TABS caps how many detail tabs a list page holds at once.
//
// Not configurable, deliberately: per CLAUDE.md's bar a setting earns its place
// when the default is actively harmful to an adopter, and a tab cap is not that.
// One constant, changed here.
//
// Five is a judgement about the STRIP, not about cost — lazy restoration already
// removed the cost. Bulma's `.tabs` is `overflow-x: auto; white-space: nowrap`
// with no overflow menu and no tab search, so past a handful the strip stops
// being navigable. ~8 fit at 1440px and ~4 at 1024px; five is the number that
// still works on the narrow end.
export const MAX_GENERIC_TABS = 5

// evictLeastRecentlyUsedTab closes the oldest-touched tab to make room.
//
// Least recently USED, not least recently opened: a tab you keep coming back to
// is the one you want kept, and open-order would throw it away first. `touchedAt`
// is stamped on every activation.
//
// Eviction rather than refusal, and a toast rather than an error: the click that
// hit the cap is a reasonable thing to do, and an error dialog would refuse it
// while leaving the reader to work out which tab to close. Safe because a tab
// holds no unsaved state — the editor is a modal that opens over it, and closing
// a background tab cannot discard anything.
function evictLeastRecentlyUsedTab(slug) {
  const mine = readGenericTabs()
    .filter(t => t.slug === slug)
    .sort((a, b) => (a.touchedAt || 0) - (b.touchedAt || 0))
  const victim = mine[0]
  if (!victim) return
  const domId = genericTabDomId(victim.slug, victim.orbId)
  deleteGenericTab(victim.slug, victim.orbId)
  document.getElementById(`tab-generic-${domId}`)?.parentElement?.remove()
  document.getElementById(`tab-content-generic-${domId}`)?.remove()
  window.bulmaToast?.toast({
    message: `Closed “${victim.label}” — ${MAX_GENERIC_TABS} tabs is the maximum.`,
    closeOnClick: true,
    type: 'is-info is-light',
    position: 'top-right',
    dismissible: false,
    duration: 4000,
    offsetTop: '4em',
    pauseOnHover: true,
    animate: { in: 'fadeInRight', out: 'fadeOutRight' },
  })
}

// loadGenericTab opens (or focuses) a detail tab below the list.
//
// `activate` is false only during RESTORATION: the strip is rebuilt without
// selecting anything, so no panel fetches until someone actually looks at it.
// Restoring eagerly meant arriving at a list page fired one detail request per
// saved tab, all at once, for panels nobody had opened — and firing them
// concurrently dropped one, which the optimistic `loaded` flag then made
// permanent (the tab stayed blank until Reload). Lazy keeps "fetch once per tab
// per page load" and pays it on the click instead of on arrival.
export function loadGenericTab(label, slug, orbId, activate = true) {
  const domId = genericTabDomId(slug, orbId)
  const tabId = `tab-generic-${domId}`
  const contentId = `tab-content-generic-${domId}`

  const existing = document.getElementById(tabId)
  if (existing) {
    existing.click()
    return
  }

  // Counted off the DOM, not off storage: storage holds every slug's tabs and
  // only this page's are on screen.
  if (document.querySelectorAll('#tablist li.tab a[id^="tab-generic-"]').length >= MAX_GENERIC_TABS) {
    evictLeastRecentlyUsedTab(slug)
  }

  $('#tablist').append(`<li class="tab">
    <a id="${tabId}" data-target="${contentId}" role="tab" aria-selected="false" tabindex="-1">
      ${label}
      <span class="pl-2">
        <button id="tab-close-generic-${domId}">
          <i class="fa-solid fa-xmark" style="font-size: 0.8em;"></i>
        </button>
      </span>
    </a>
  </li>`)
  $('.app-main').append(`<div class="tab-content" id="${contentId}" role="tabpanel" style="display:none"></div>`)

  const tabLink = document.getElementById(tabId)
  const tabContent = document.getElementById(contentId)

  // The SAME url the row links to. HX-Request makes the handler return the body
  // alone; there is no second endpoint that could drift from the page. Stashed
  // on the panel so the Reload button inside the fragment can re-issue it
  // without reconstructing slug and orbId from the DOM.
  tabContent.dataset.detailUrl = `${BASE}/${slug}/${encodeURIComponent(orbId)}`

  tabLink.addEventListener('click', () => {
    activateTab(tabLink.parentElement)
    displayTabContent(contentId)
    setCurrentTab(tabId)
    touchGenericTab(slug, orbId)
    if (tabContent.dataset.loaded) return
    // `loading` guards a second click while the first request is in flight;
    // `loaded` is set only once the swap has actually happened. Setting it
    // optimistically — which is what this did — meant a request that never
    // arrived left the tab flagged loaded and permanently empty, with no way
    // back but the Reload button.
    if (tabContent.dataset.loading) return
    tabContent.dataset.loading = '1'
    Promise.resolve(htmx.ajax('GET', tabContent.dataset.detailUrl, { target: tabContent, swap: 'innerHTML' }))
      .then(() => { tabContent.dataset.loaded = '1' })
      .finally(() => { delete tabContent.dataset.loading })
  })

  document.getElementById(`tab-close-generic-${domId}`).addEventListener('click', (event) => {
    event.stopPropagation()

    // Where to land. Closing the ACTIVE tab moves one to the LEFT — which for
    // the leftmost detail tab is Summary, so the rule terminates naturally and
    // needs no special case. Closing a BACKGROUND tab moves nothing: you are
    // reading something else, and yanking you out of it to tidy up a tab you
    // were not looking at is the behaviour no tabbed interface has.
    //
    // It used to click Summary unconditionally, which got both wrong — closing
    // the last tab in a row of five sent you back to the list every time, and
    // closing a background one threw away where you were.
    const li = tabLink.parentElement
    const wasActive = li.classList.contains('is-active')
    const left = li.previousElementSibling
    const nextId = left ? left.querySelector('a')?.id : 'tab-summary'

    deleteGenericTab(slug, orbId)
    // The PERSISTED current tab follows the same answer, or the next arrival
    // restores a tab that no longer exists and silently falls back to Summary.
    replaceCurrentTab(tabId, nextId || 'tab-summary')
    li.remove()
    tabContent.remove()
    if (wasActive) document.getElementById(nextId || 'tab-summary')?.click()
  })

  saveGenericTab(label, slug, orbId)
  if (activate) tabLink.click()
}

// labelFromTable finds a row's display name in the list already rendered on the
// page. Returns '' when the row is not there — a capped list, or an orbId that
// does not exist — and the caller falls back to the orbId, which is ugly but
// honest and still opens.
function labelFromTable(slug, orbId) {
  for (const tr of document.querySelectorAll('#generic-table tbody tr[data-orb-id]')) {
    if (tr.dataset.orbId === orbId) return labelOfRow(tr)
  }
  return ''
}

// labelOfRow reads a row's display name — the first cell. Plain text, because
// the name is not a link: see the dblclick handler for why.
function labelOfRow(tr) {
  return (tr.querySelector('td')?.textContent || '').trim()
}


// initGenericTabRestoration reopens the tabs that were left open, but only the
// ones belonging to THIS page's slug — a rack tab restored onto /storage-devices
// would fetch a 404 and render an error where a tab should be.
export function initGenericTabRestoration() {
  const table = document.getElementById('generic-table')
  if (!table) return
  clearTabStateOnFresh()
  const slug = table.dataset.slug
  for (const t of readGenericTabs()) {
    if (t.slug === slug) loadGenericTab(t.label, t.slug, t.orbId, false)
  }

  // ?open=<orbId>&label=<name> deep-links, the same form every hand-written
  // list page accepts. It is how another page hands you one of these — a
  // cluster's node row opening that server — and how a bookmark reopens a tab.
  // Takes precedence over the restored tab: an explicit request beats what
  // happened to be open last time.
  const params = new URLSearchParams(window.location.search)
  const openId = params.get('open')
  if (openId) {
    // The label comes from the TABLE when the link did not carry one — which is
    // the common case now that /{slug}/{orbId} redirects here, because the
    // redirect happens before anything is read from DGraph and so has no name to
    // put in the URL. The row is already on the page; reading it there costs
    // nothing and cannot disagree with what the list shows.
    const label = params.get('label') || labelFromTable(slug, openId) || openId
    loadGenericTab(label, slug, openId)
    saveGenericTab(label, slug, openId)
    document.getElementById(`tab-generic-${genericTabDomId(slug, openId)}`)?.click()
    // Dropped from the URL once honoured, so a reload does not force the tab
    // back open after it has been closed.
    history.replaceState(null, '', BASE + '/' + slug)
    return
  }

  // The tab you were on comes back selected — which is what the hand-written
  // pages did, and what persisting the strip without the selection leaves half
  // done. Under lazy restoration this is the ONE panel that fetches on arrival;
  // the other four stay unloaded until opened.
  //
  // It read `localStorage.tabCurrent`, a key nothing has ever written: the
  // writer goes through getTabStorageKey() and the reader did not, so the
  // restore silently did nothing and every visit landed on Summary.
  const current = getCurrentTab()
  if (current && document.getElementById(current)) document.getElementById(current).click()
}

export function initGenericTable() {
  document.addEventListener('DOMContentLoaded', () => {
    const el = document.getElementById('generic-table')
    if (!el || typeof DataTable === 'undefined') return
    if ($.fn.dataTable.isDataTable(el)) return

    // Wire the tabs already in the markup — i.e. Summary. Dynamically opened
    // tabs get their own handler in loadGenericTab. Without this, opening a
    // detail tab switched away from Summary and nothing could switch back:
    // the list was still in the DOM, just permanently hidden.
    document.querySelectorAll('li.tab a[data-target]').forEach((a) => {
      a.addEventListener('click', () => {
        activateTab(a.parentElement)
        displayTabContent(a.dataset.target)
        setCurrentTab(a.id)
      })
    })

    const label = el.dataset.label || 'items'
    const file = el.dataset.slug || 'config-items'
    const btn = (icon, text, extra) => Object.assign({
      text: `<span style="display:inline-flex;align-items:center;gap:0.5em;font-size:0.65rem;"><i class="${icon}"></i><span>${text}</span></span>`,
      className: 'is-link is-outlined is-small', titleAttr: text,
    }, extra)

    // The filterBy select is rendered by the template and moved into the toolbar,
    // never built here — page structure belongs in the template (UI.md), and
    // the server already resolved which column it filters and what is in it.
    const filterByEl = el.parentElement.querySelector('.js-filterBy')
    // dataset.filterByColumn needs the attribute spelled data-filter-by-column.
    // HTML attribute names are CASE-INSENSITIVE — the parser lowercases
    // data-filterBy-column to data-filterby-column, which the dataset API then
    // exposes as `filterbyColumn`. Reading `filterByColumn` returned undefined,
    // Number(undefined) is NaN, and the column search silently matched nothing:
    // the dropdown rendered, changed, and filtered zero rows.
    const filterByCol = filterByEl ? Number(filterByEl.dataset.filterByColumn) : -1
    if (filterByEl) filterByEl.style.display = ''

    const topStart = [
      { buttons: [
        btn('fa-regular fa-file-excel', 'Excel', { extend: 'excel', title: '', filename: file }),
        btn('fa-regular fa-file-text', 'CSV', { extend: 'csv', title: '', filename: file }),
        btn('fa-regular fa-copy', 'Copy', { extend: 'copy' }),
        btn('fa fa-columns', 'Select', { extend: 'colvis', titleAttr: 'Select Columns' }),
      ] },
      { pageLength: { menu: [100, 250, 500] } },
    ]
    if (filterByEl) topStart.unshift($(filterByEl))

    new DataTable(el, {
      layout: {
        topStart,
        topEnd: { search: { placeholder: `Search ${label}` } },
      },
      autoWidth: true,
      scrollX: true,
      // Fill the viewport, as every hand-written list page did. A fixed 400px
      // strands a short table in an empty page on a tall screen; the constant
      // is this page's chrome (tabs bar + toolbar + header), not the old one's.
      scrollY: 'calc(100vh - 300px)',
      scrollCollapse: true,
      pageLength: 250,
      // Row selection, which the Copy/Excel/CSV buttons pick up: with rows
      // selected they export the selection, otherwise the whole table.
      select: { style: 'os' },
      // IPv4 sorts numerically, in EVERY column.
      //
      // "10.20.21.100" sorts before "10.20.21.41" as a string, because "1" <
      // "4" — wrong in a way that looks plausible, which is why it survived on
      // the hand-written pages until someone declared the column. Declaring it
      // is what a generic page cannot do: it does not know which field holds an
      // address. So the test is on the VALUE, and a cell that is not an address
      // passes through untouched. Link cells are unwrapped first — a reference
      // column renders an <a>, and sorting its markup orders by href.
      columnDefs: [{
        targets: '_all',
        render: (data, type) => {
          const raw = data == null ? '' : data
          if (type === 'display') return raw
          // Everything that is not display works on the VISIBLE TEXT.
          //
          // A reference cell is an <a href="/datacenters/colo:colo-galleon">,
          // and DataTables searches whatever it is given — so before this,
          // typing "datacenters" in the search box matched every row that had
          // any link, and an exact column search for "colo" matched nothing at
          // all because the cell's value was the whole anchor. The filterBy is
          // an exact column search, which is how this surfaced.
          const text = String(raw).replace(/<[^>]*>/g, '').trim()
          return (type === 'sort' || type === 'type') ? ipv4SortKey(text) : text
        },
      }],
      // stateSave keyed by SLUG, not by table id.
      //
      // DataTables keys its own storage on the id, and every generic page
      // shares the id `generic-table` — so a saved sort from /racks was
      // restored on /storage-devices, against entirely different columns.
      // Turning it off fixed that by losing the feature: the bespoke Servers
      // and Network Devices pages both persisted sort, page and search. The
      // callbacks below restore the behaviour with a key that cannot collide.
      //
      // The filterBy rides along for free — a filterBy IS a column search, and
      // column searches are part of the state DataTables saves. That is why
      // there is no separate filterBy-persistence code here.
      stateSave: true,
      stateSaveCallback: (_settings, data) => {
        try { localStorage.setItem('dt:' + file, JSON.stringify(data)) } catch { /* quota or private mode */ }
      },
      stateLoadCallback: () => {
        try { return JSON.parse(localStorage.getItem('dt:' + file)) } catch { return null }
      },
      language: {
        infoEmpty: `No ${label} to show`,
        info: '_START_ to _END_ of _TOTAL_ _ENTRIES-TOTAL_',
        entries: { _: 'rows', 1: 'row' },
        emptyTable: `No ${label} yet.`,
      },
      // The affordance for the dblclick handler below. Without it opening a
      // row as a tab is undiscoverable — every hand-written list page set
      // this, and the generic page silently dropped it.
      createdRow: (row) => { row.style.cursor = 'pointer'; row.title = 'Double-click to open' },
    })

    // Restore the filterBy's own <select> from whatever state put into the
    // column search — the select is ours, so DataTables cannot restore it.
    if (filterByEl) {
      const api = new DataTable.Api(el)
      const sel = filterByEl.querySelector('select')
      const saved = api.column(filterByCol).search()
      if (saved) sel.value = saved
      sel.addEventListener('change', () => {
        api.column(filterByCol).search(sel.value, { exact: !!sel.value }).draw()
      })
    }

    const slug = el.dataset.slug

    // DOUBLE click opens the row as a tab. Single click is already taken: this
    // table has DataTables row selection (`select: { style: 'os' }`), and the
    // Copy/Excel/CSV buttons export the selection when there is one. Binding
    // open to a single click fires both actions from one gesture, and made
    // selecting a row for export impossible.
    //
    // This is why the hand-written tables used double click — the same `select`
    // line was on them. It was forced, not a preference. A single-click opener
    // was added 2026-10-04 without checking what the gesture already meant, and
    // removed 2026-10-05.
    el.addEventListener('dblclick', (e) => {
      const row = e.target.closest('tbody tr[data-orb-id]')
      if (!row || !el.contains(row)) return
      const orbId = row.dataset.orbId
      if (!orbId) return
      e.preventDefault()
      loadGenericTab(labelOfRow(row) || orbId, slug, orbId)
    })

    initGenericTabRestoration()
  })
}

export function initListPages() {
  document.addEventListener('DOMContentLoaded', () => { initInventoryTable() })
  for (const pageDef of LIST_PAGES) {
    document.addEventListener('DOMContentLoaded', () => {
      pageDef.init({ onRowOpen: (data) => openOrFocusTab(pageDef, data) })
    })
    window.addEventListener('load', pageDef.restore)
  }
}


// ─── API error rendering ────────────────────────────────────────────────────
//
// Orbital answers every failure with one envelope — `{error, code, httpStatus,
// hint}` (docs/reference/ERROR-RESPONSES.md). `error` says what went wrong and
// `hint` says what to do about it, and the hint is the half most worth showing:
// it is where "Open a change request: POST /api/v1/change-requests" and
// "Rename the variable to `version`" live.
//
// These exist because the convention was documented server-side and enforced
// nowhere on the client. Seven call sites re-implemented it inline, four
// discarded it and printed a bare status code, and new code picked whichever
// neighbour it was copied from. A shared helper makes rendering the envelope
// the shortest thing to write, which is the only way a convention survives.
//
// `code` is deliberately NOT rendered. It is the machine identifier clients
// branch on — never prose for a reader.

// apiErrorFromBody renders an already-parsed envelope.
export function apiErrorFromBody(body, fallback = 'Request failed') {
  // `message` is the pre-envelope echo shape. A few endpoints were read that
  // way before the central ErrorHandler normalised everything, and falling
  // through costs nothing while guaranteeing this helper is never worse than
  // the inline code it replaces.
  const head = (body && (body.error || body.message)) || fallback
  return body && body.hint ? head + ' — ' + body.hint : head
}

// apiErrorText is apiErrorFromBody for a response you have not read yet.
// Accepts a fetch Response or a jQuery jqXHR (DataTables' `ajax.error`).
//
// Returns '' when the request was ABORTED (status 0) — a superseded fetch, or a
// page being navigated away from. That is not a failure anyone can act on, and
// showing it buries the ones they can. **Treat '' as "display nothing".**
export async function apiErrorText(source, fallback = 'Request failed') {
  const status = Number(source && source.status) || 0
  if (status === 0) return ''
  let body = null
  if (typeof source.json === 'function') {
    body = await source.json().catch(() => null)
  } else if (source.responseJSON) {
    body = source.responseJSON
  } else if (typeof source.responseText === 'string') {
    try { body = JSON.parse(source.responseText) } catch (_) { /* not the envelope */ }
  }
  return apiErrorFromBody(body, fallback + ' (HTTP ' + status + ')')
}
