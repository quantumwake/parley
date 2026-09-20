// Layout preferences that survive a reload: whether the conversation sidebar
// is open and whether the console is in minimal-border (dense) mode. Full
// screen is not stored: browsers only allow it after a click.

const SIDEBAR = 'parley.sidebarOpen'
const DENSE = 'parley.dense'

export function loadLayout(storage) {
  const get = (k) => { try { return storage.getItem(k) } catch { return null } }
  return { sidebarOpen: get(SIDEBAR) !== '0', dense: get(DENSE) === '1' }
}

export function saveLayout(storage, layout) {
  try {
    storage.setItem(SIDEBAR, layout.sidebarOpen ? '1' : '0')
    storage.setItem(DENSE, layout.dense ? '1' : '0')
  } catch { /* private mode: the layout just does not persist */ }
}
