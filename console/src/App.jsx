import { useCallback, useEffect, useState } from 'react'
import { RefreshCw, Sun, Moon, Brain, X, Radio, MessageSquare, Plus, Columns2, Rows2 } from 'lucide-react'
import { TerminalSplit } from '@quantumwake/terminal-ux-components'
import { api } from './api'
import List from './List'
import Pane from './Pane'

// parley: a conversation stream read like a chat.
//   left    conversations, shared first, then this machine's recorded sessions
//   right   a tab strip of open conversations over any number of panes, side
//           by side or stacked, each with its own stream, composer and
//           inspector; every divider (sidebar included) drags to resize

let paneSeq = 0
const newKey = () => `pane-${++paneSeq}`
const stored = (k, fallback) => { try { return localStorage.getItem(k) || fallback } catch { return fallback } }
const keep = (k, v) => { try { localStorage.setItem(k, v) } catch { /* private mode */ } }

export default function App() {
  const [theme, setTheme] = useState(() => { try { return localStorage.getItem('parley.theme') || 'chalkboard' } catch { return 'chalkboard' } })
  const [me, setMe] = useState(null)
  const [identities, setIdentities] = useState([])
  const [items, setItems] = useState([])
  const [subs, setSubs] = useState([])
  const [filter, setFilter] = useState('')
  const [showThinking, setShowThinking] = useState(false)
  const [error, setError] = useState('')
  // Open conversations are tabs; each carries its own copy of the record so
  // it keeps showing what it had even if the list refreshes. Any number of
  // panes, side by side or stacked, each show one tab (or none yet).
  const [tabs, setTabs] = useState([])
  const [panes, setPanes] = useState(() => [{ key: newKey(), tab: null }])
  const [focused, setFocused] = useState(0)
  const [direction, setDirectionState] = useState(() => stored('parley.direction', 'horizontal'))
  const [sidebar, setSidebar] = useState(() => { const s = Number(stored('parley.sidebar', '0.22')); return s > 0 && s < 1 ? s : 0.22 })

  useEffect(() => {
    document.documentElement.classList.toggle('theme-paper', theme === 'paper')
    try { localStorage.setItem('parley.theme', theme) } catch { /* private mode */ }
  }, [theme])

  const loadList = useCallback(async () => {
    try {
      const r = await api.conversations({ limit: 300 }); setItems(r.conversations || []); setError('')
      const su = await api.subscriptions(); setSubs(su.subscriptions || [])
    } catch (e) { setError(e.message) }
  }, [])
  useEffect(() => { api.me().then(setMe).catch((e) => setError(e.message)); api.identities().then((r) => setIdentities(r.identities || [])).catch(() => {}); loadList(); const t = setInterval(loadList, 15000); return () => clearInterval(t) }, [loadList])

  // Switching identity reopens the console as that identity: what it can see
  // and who its posts are from both change, so every open pane closes.
  const switchIdentity = async (name) => {
    try {
      await api.useIdentity(name)
      setTabs([]); setPanes([{ key: newKey(), tab: null }]); setFocused(0)
      const [m, ids] = await Promise.all([api.me(), api.identities()])
      setMe(m); setIdentities(ids.identities || []); setError('')
      await loadList()
    } catch (e) { setError(e.message) }
  }

  // Every tab lives in the strip; a pane shows one tab. A click (sidebar or
  // strip) on a tab already showing in a pane focuses that pane; otherwise
  // the conversation goes into the focused pane.
  const show = (id) => {
    const at = panes.findIndex((p) => p.tab === id)
    if (at >= 0) { setFocused(at); return }
    setPanes((ps) => ps.map((p, i) => (i === focused ? { ...p, tab: id } : p)))
  }
  const openPane = (c) => {
    setTabs((prev) => (prev.some((t) => t.id === c.id) ? prev : [...prev, c]))
    show(c.id)
  }
  // Closing a tab empties the panes that showed it; each takes an open tab
  // no other pane shows, if there is one.
  const closePane = (id) => setTabs((prev) => {
    const next = prev.filter((t) => t.id !== id)
    setPanes((ps) => {
      const shown = new Set(ps.map((p) => p.tab).filter((t) => t && t !== id))
      return ps.map((p) => {
        if (p.tab !== id) return p
        const spare = next.find((t) => !shown.has(t.id))
        if (spare) shown.add(spare.id)
        return { ...p, tab: spare ? spare.id : null }
      })
    })
    return next
  })
  const updatePaneMeta = (id, patch) => setTabs((prev) => prev.map((t) => (t.id === id ? { ...t, ...patch } : t)))
  // A new pane opens beside the focused one with the next open tab no pane
  // shows (or empty), and takes focus so the next click lands in it.
  const addPane = () => {
    const shown = new Set(panes.map((p) => p.tab))
    const spare = tabs.find((t) => !shown.has(t.id))
    const at = focused + 1
    setPanes((ps) => [...ps.slice(0, at), { key: newKey(), tab: spare ? spare.id : null }, ...ps.slice(at)])
    setFocused(at)
  }
  const removePane = (i) => {
    if (panes.length === 1) return
    setPanes((ps) => ps.filter((_, j) => j !== i))
    setFocused((f) => (f > i || f === panes.length - 1 ? Math.max(f - 1, 0) : f))
  }
  const setDirection = (d) => { setDirectionState(d); keep('parley.direction', d) }

  // Create, rename and delete a shared conversation from the sidebar.
  const createChannel = async (name, description) => {
    const r = await api.createConversation(name, description)
    await loadList()
    return r
  }
  const renameChannel = async (id, title) => {
    await api.renameConversation(id, { title })
    await loadList()
    updatePaneMeta(id, { title })
  }
  const deleteChannel = async (id) => {
    await api.deleteConversation(id)
    closePane(id)
    await loadList()
  }

  const btn = 'px-2 py-1 text-[11px] border border-border text-ink-2 hover:bg-elevated'
  const btnOn = 'px-2 py-1 text-[11px] border border-accent bg-accent/15 text-ink'
  const many = panes.length > 1
  const shownTabs = new Set(panes.map((p) => p.tab))
  const divider = 'bg-border outline-none transition-colors hover:bg-accent/50 focus:bg-accent/50 data-[dragging]:bg-accent'

  return (
    <div className="flex h-screen w-screen max-w-full flex-col overflow-hidden bg-base text-ink-body">
      <header className="flex min-w-0 items-center justify-between gap-3 border-b border-border bg-surface px-4 py-2" style={{ boxShadow: 'var(--shadow)' }}>
        <div className="flex items-baseline gap-3"><span className="serif text-[17px] font-semibold text-ink">parley</span><span className="text-[11px] text-ink-subdued">statefs.ai · conversations</span></div>
        <div className="flex shrink-0 items-center gap-2 text-[11px] text-ink-subdued">
          {me && identities.length > 1 ? (
            <select aria-label="identity" title="act as another identity on this machine" className="max-w-[220px] truncate border border-border bg-surface px-1.5 py-1 text-[11px] text-ink-2 outline-none focus:border-accent"
              value={identities.find((i) => i.current)?.name || ''} onChange={(e) => switchIdentity(e.target.value)}>
              {!identities.some((i) => i.current) && <option value="">{me.username}</option>}
              {identities.map((i) => <option key={i.path} value={i.name}>{i.username}{i.name !== i.username ? ` (${i.name})` : ''}</option>)}
            </select>
          ) : me && <span className="truncate">{me.username}</span>}
          {me && <span className="truncate"><span className="text-ink-hint">@</span> {me.tenant}</span>}
          <button className={showThinking ? btnOn : btn} onClick={() => setShowThinking(!showThinking)}><Brain size={11} className="inline mr-1" />thinking</button>
          <button className={btn} title="switch theme" onClick={() => setTheme(theme === 'paper' ? 'chalkboard' : 'paper')}>{theme === 'paper' ? <Moon size={12} /> : <Sun size={12} />}</button>
          <button className={btn} title="reload the list" onClick={loadList}><RefreshCw size={12} /></button>
        </div>
      </header>
      {(me?.notices || []).map((n) => (
        <div key={n} className="border-b border-border bg-elevated px-4 py-1.5 text-[12px] text-accent-bright">{n}</div>
      ))}
      <TerminalSplit className="min-h-0 flex-1" sizes={[sidebar, 1 - sidebar]} minSize={220} dividerSize={4} dividerClassName={divider}
        onSizesChange={([s]) => { setSidebar(s); keep('parley.sidebar', String(s)) }}>
        <aside className="min-w-0 flex-1 bg-surface">
          <List items={items} subs={subs} openIds={new Set(tabs.map((t) => t.id))} onSelect={openPane} filter={filter} setFilter={setFilter} live={{}}
            onCreate={createChannel} onRename={renameChannel} onDelete={deleteChannel} />
        </aside>
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="flex items-stretch gap-1 border-b border-border bg-base px-2 pt-1.5">
            <div className="flex min-w-0 flex-1 items-stretch gap-1 overflow-x-auto [scrollbar-width:none]">
              {tabs.map((t) => (
                <div key={t.id} onClick={() => show(t.id)} onAuxClick={(e) => { if (e.button === 1) closePane(t.id) }} title={t.title || t.name}
                  className={`group flex shrink-0 cursor-pointer items-center gap-2 border border-b-0 px-3 py-1 text-[12px] ${panes[focused]?.tab === t.id
                    ? 'border-accent/70 bg-elevated text-ink'
                    : shownTabs.has(t.id) ? 'border-border bg-surface text-ink-2' : 'border-border bg-surface text-ink-subdued hover:text-ink-2'}`}>
                  {t.mode === 'shared' ? <Radio size={11} className="shrink-0 text-ink-subdued" /> : <MessageSquare size={11} className="shrink-0 text-ink-subdued" />}
                  <span className="max-w-[22ch] truncate">{t.title || t.name}</span>
                  <span title="close tab" onClick={(e) => { e.stopPropagation(); closePane(t.id) }}
                    className="text-ink-hint opacity-0 hover:text-ink group-hover:opacity-100"><X size={11} /></span>
                </div>
              ))}
              {!tabs.length && <span className="self-center px-1 pb-1 text-[11px] italic text-ink-hint">no conversations open</span>}
            </div>
            <div className="flex shrink-0 items-center gap-1 self-center pb-1">
              <button title="add a pane beside the focused one" onClick={addPane} className={btn}><Plus size={12} className="inline" /> pane</button>
              <button title="panes side by side" onClick={() => setDirection('horizontal')} className={direction === 'horizontal' ? btnOn : btn}><Columns2 size={12} /></button>
              <button title="panes stacked" onClick={() => setDirection('vertical')} className={direction === 'vertical' ? btnOn : btn}><Rows2 size={12} /></button>
            </div>
          </div>
          <TerminalSplit className="min-h-0 min-w-0 flex-1" direction={direction} minSize={200} dividerSize={4} dividerClassName={divider}>
            {panes.map((p, i) => {
              const c = tabs.find((t) => t.id === p.tab)
              return (
                <div key={p.key} onMouseDown={() => setFocused(i)}
                  className={`relative flex min-h-0 min-w-0 flex-1 ${many && focused === i ? 'ring-1 ring-inset ring-accent/50' : ''}`}>
                  {c ? (
                    <Pane key={c.id} conversation={c} theme={theme} showThinking={showThinking} me={me}
                      onClosePane={many ? () => removePane(i) : undefined}
                      onSubscribedChange={(id, subscribed) => updatePaneMeta(id, { subscribed })} />
                  ) : (
                    <div className="flex min-w-0 flex-1 items-center justify-center">
                      {many && <button title="close this pane" onClick={() => removePane(i)} className={`absolute right-2 top-2 ${btn}`}><X size={12} /></button>}
                      <div className="mx-auto max-w-md px-4 text-center text-ink-subdued">
                        <div className="serif text-[20px] text-ink-2">{many ? 'Empty pane' : 'Every session, kept.'}</div>
                        <div className="mt-2 text-[12px]">{many
                          ? 'Click this pane, then pick a conversation on the left or a tab above to show it here.'
                          : 'Pick a recorded session on the left to read it as a chat, or a shared conversation to follow and post. Each opens as a tab; add panes to read several at once, and drag the dividers to resize.'}</div>
                      </div>
                    </div>
                  )}
                </div>
              )
            })}
          </TerminalSplit>
        </div>
      </TerminalSplit>
      <footer className="flex items-center justify-between border-t border-border bg-surface px-4 py-1 text-[11px] text-ink-subdued">
        <span>{error ? <span className="text-danger">{error}</span> : `${items.length} conversations · ${me?.directory || ''}${me?.version ? ` · parley ${me.version}` : ''}`}</span>
        <span>{tabs.length ? `${tabs.length} open · ` : ''}{panes.length} pane{many ? 's' : ''} · {theme}</span>
      </footer>
    </div>
  )
}
