let table = null;

// state for tabs opened
const openTabs= new Map();

$('#myTable tbody').on('dblclick', 'tr', function () {
  console.log('double click')

  // use object name as tab name 
  var displayName = this.cells[0].innerText

  // data() returns row object with hidden columns, e.g. id
  var id = table.row(this).data().id

  // var id = this.cells[0].innerText + "-" + table.row(this).index().toString()

  // look for tab id... if found, navigate to it, otherwise load new tab
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

// add loading progress indictor on button (instead of the default dt progress indicator) 
const reloadButton = table.button('reload:name').node()
table.button('reload:name').node().on('click', function (e) {
  table.clear().draw()
  reloadButton.addClass('is-loading')
  setTimeout(() => {
    table.ajax.url('/api/clusters').load(()=>{
        reloadButton.removeClass('is-loading')
    })
  }, 250);
});

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
      hx-get="/clusters/${itemId}" hx-trigger="click" hx-target="#tab-content-${itemId}" hx-swap="innerHTML">
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
  // for now we just go back to the main 'clusters' tab
  var tabClose = document.getElementById(`tab-close-${itemId}`)
  tabClose.addEventListener('click', (event) => {
    event.stopPropagation() // allows click on main tab below to work? Got idea from https://stackoverflow.com/questions/64687523/adding-event-listener-to-elements-on-click-of-another-in-loop
    unloadTab(itemId) 
    deleteTab(displayName, itemId)
    document.getElementById('tab-summary').click()  // go back to main tab
    // $('#tab-clusters')[0].click();  // this works too
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


// Feat: As a user, when I open a tab, that tab should remain open after I refresh a page
// On page load, check what tabs were last open (from local storage). For each tab found,
// add it to the nav bar
//
// Feat: As a user, when I logout, all my tabs are cleared.
function loadPreviousTabs() {
  console.log('checking for previous tabs')
  if (!localStorage.tabs) {
    console.log('none found')
    return
  }
  console.log('found', localStorage.tabs)

  let tabSet = new Set(JSON.parse(localStorage.tabs))
  tabSet.forEach(tabData => {
    let {displayName, id} = JSON.parse(tabData)

    // TODO check if entity still exists first before loading tab

    loadTab(displayName, id)
  })
}

function clickPreviousTab() {
  let currentTabId = getCurrentTab() 
  if (currentTabId) {
    document.getElementById(currentTabId).click()
  }  
}

// load saved tabs (logout clears local storage)
// wait for whole page to load, otherwise width of headers on main data table 
// won't adjust full size
window.addEventListener("load", () => {
  loadPreviousTabs()
  clickPreviousTab()
})
