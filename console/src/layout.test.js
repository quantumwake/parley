import { describe, expect, it } from 'vitest'
import { loadLayout, saveLayout } from './layout'

const memory = (init = {}) => {
  const data = { ...init }
  return { getItem: (k) => (k in data ? data[k] : null), setItem: (k, v) => { data[k] = String(v) }, data }
}

describe('layout preferences', () => {
  it('starts with the sidebar open and normal borders', () => {
    expect(loadLayout(memory())).toEqual({ sidebarOpen: true, dense: false })
  })

  it('reads what was saved', () => {
    const s = memory()
    saveLayout(s, { sidebarOpen: false, dense: true })
    expect(loadLayout(s)).toEqual({ sidebarOpen: false, dense: true })
  })

  it('ignores junk values rather than trusting them', () => {
    expect(loadLayout(memory({ 'parley.sidebarOpen': 'maybe', 'parley.dense': 'yes' }))).toEqual({ sidebarOpen: true, dense: false })
  })

  it('works when storage throws (private mode)', () => {
    const broken = { getItem() { throw new Error('denied') }, setItem() { throw new Error('denied') } }
    expect(loadLayout(broken)).toEqual({ sidebarOpen: true, dense: false })
    expect(() => saveLayout(broken, { sidebarOpen: false, dense: true })).not.toThrow()
  })
})
