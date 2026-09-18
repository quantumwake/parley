import { useCallback, useEffect, useState } from 'react'
import { RefreshCw, Sun, Moon, Brain, X, Radio, MessageSquare, Square, Columns2, Rows2 } from 'lucide-react'
import { api } from './api'
import List from './List'
import Pane from './Pane'

// parley: a conversation stream read like a chat.
//   left    conversations, shared first, then this machine's recorded sessions
//   right   a tab strip of open conversations over one pane, or two panes
//           split left/right or top/bottom; each pane has its own stream,
//           composer and inspector

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
  // it keeps showing what it had even if the list refreshes. Up to two panes
  // (side by side or stacked) each show one tab; paneTabs holds their ids.
  const [tabs, setTabs] = useState([])
  const [paneTabs, setPaneTabs] = useState([null, null])
  const [layout, setLayoutState] = useState(() => { try { return localStorage.getItem('parley.layout') || 'single' } catch { return 'single' } })
  const [focused, setFocused] = useState(0)

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
      setTabs([]); setPaneTabs([null, null]); setFocused(0)
      const [m, ids] = await Promise.all([api.me(), api.identities()])
      setMe(m); setIdentities(ids.identities || []); setError('')
      await loadList()
    } catch (e) { setError(e.message) }
  }

  // Every tab lives in the strip; a pane shows one tab. A click (sidebar or
  // strip) puts the conversation into the focused pane.
  const show = (id) => setPaneTabs((p) => { const n = [...p]; n[focused] = id; return n })
  const openPane = (c) => {
    setTabs((prev) => (prev.some((t) => t.id === c.id) ? prev : [...prev, c]))
    show(c.id)
  }
  const closePane = (id) => setTabs((prev) => {
    const next = prev.filter((t) => t.id !== id)
    const fallback = next.length ? next[next.length - 1].id : null
    setPaneTabs((p) => p.map((pt) => (pt === id ? fallback : pt)))
    return next
  })
  const updatePaneMeta = (id, patch) => setTabs((prev) => prev.map((t) => (t.id === id ? { ...t, ...patch } : t)))
  const setLayout = (l) => {
    if (l !== 'single') {
      // The second pane gets another open tab, or starts empty and takes focus
      // so the next click lands in it.
      setPaneTabs(([a, b]) => {
        const second = b && b !== a ? b : tabs.find((t) => t.id !== a)?.id ?? null
        if (!second) setFocused(1)
        return [a, second]
      })
    } else setFocused(0)
    setLayoutState(l)
    try { localStorage.setItem('parley.layout', l) } catch { /* private mode */ }
  }

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
  const split = layout !== 'single'

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
      <div className="flex min-h-0 flex-1">
        <aside className="w-[320px] shrink-0 border-r border-border bg-surface">
          <List items={items} subs={subs} openIds={new Set(tabs.map((t) => t.id))} onSelect={openPane} filter={filter} setFilter={setFilter} live={{}}
            onCreate={createChannel} onRename={renameChannel} onDelete={deleteChannel} />
        </aside>
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="flex items-stretch gap-1 border-b border-border bg-base px-2 pt-1.5">
            <div className="flex min-w-0 flex-1 items-stretch gap-1 overflow-x-auto [scrollbar-width:none]">
              {tabs.map((t) => (
                <div key={t.id} onClick={() => show(t.id)} onAuxClick={(e) => { if (e.button === 1) closePane(t.id) }} title={t.title || t.name}
                  className={`group flex shrink-0 cursor-pointer items-center gap-2 border border-b-0 px-3 py-1 text-[12px] ${paneTabs[focused] === t.id
                    ? 'border-accent/70 bg-elevated text-ink'
                    : paneTabs.slice(0, split ? 2 : 1).includes(t.id) ? 'border-border bg-surface text-ink-2' : 'border-border bg-surface text-ink-subdued hover:text-ink-2'}`}>
                  {t.mode === 'shared' ? <Radio size={11} className="shrink-0 text-ink-subdued" /> : <MessageSquare size={11} className="shrink-0 text-ink-subdued" />}
                  <span className="max-w-[22ch] truncate">{t.title || t.name}</span>
                  <span title="close tab" onClick={(e) => { e.stopPropagation(); closePane(t.id) }}
                    className="text-ink-hint opacity-0 hover:text-ink group-hover:opacity-100"><X size={11} /></span>
                </div>
              ))}
              {!tabs.length && <span className="self-center px-1 pb-1 text-[11px] italic text-ink-hint">no conversations open</span>}
            </div>
            <div className="flex shrink-0 items-center gap-1 self-center pb-1">
              {[['single', 'single pane', Square], ['lr', 'split left / right', Columns2], ['tb', 'split top / bottom', Rows2]].map(([l, title, Icon]) => (
                <button key={l} title={title} onClick={() => setLayout(l)} className={layout === l ? btnOn : btn}><Icon size={12} /></button>
              ))}
            </div>
          </div>
          <div className={`flex min-h-0 min-w-0 flex-1 ${layout === 'tb' ? 'flex-col' : ''}`}>
            {(split ? [0, 1] : [0]).map((i) => {
              const c = tabs.find((t) => t.id === paneTabs[i])
              return (
                <div key={i} onMouseDown={() => setFocused(i)}
                  className={`flex min-h-0 min-w-0 flex-1 ${i === 0 && split ? (layout === 'tb' ? 'border-b border-border' : 'border-r border-border') : ''} ${split && focused === i ? 'ring-1 ring-inset ring-accent/40' : ''}`}>
                  {c ? (
                    <Pane key={c.id} conversation={c} theme={theme} showThinking={showThinking} me={me}
                      onSubscribedChange={(id, subscribed) => updatePaneMeta(id, { subscribed })} />
                  ) : (
                    <div className="flex min-w-0 flex-1 items-center justify-center">
                      <div className="mx-auto max-w-md text-center text-ink-subdued">
                        <div className="serif text-[20px] text-ink-2">{split ? 'Empty pane' : 'Every session, kept.'}</div>
                        <div className="mt-2 text-[12px]">{split
                          ? 'Click this pane, then pick a conversation on the left or a tab above to show it here.'
                          : 'Pick a recorded session on the left to read it as a chat, or a shared conversation to follow and post. Each opens as a tab; split the view to read two side by side.'}</div>
                      </div>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      </div>
      <footer className="flex items-center justify-between border-t border-border bg-surface px-4 py-1 text-[11px] text-ink-subdued">
        <span>{error ? <span className="text-danger">{error}</span> : `${items.length} conversations · ${me?.directory || ''}`}</span>
        <span>{tabs.length ? `${tabs.length} open` : ''} · {theme}</span>
      </footer>
    </div>
  )
}
