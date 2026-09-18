import { useCallback, useEffect, useState } from 'react'
import { RefreshCw, Sun, Moon, Brain } from 'lucide-react'
import { api } from './api'
import List from './List'
import Pane from './Pane'

// parley: a conversation stream read like a chat.
//   left    conversations, shared first, then this machine's recorded sessions
//   right   one Pane per open conversation, side by side (a click opens one;
//           several can be open at once, each with its own stream, composer
//           and inspector)

export default function App() {
  const [theme, setTheme] = useState(() => { try { return localStorage.getItem('parley.theme') || 'chalkboard' } catch { return 'chalkboard' } })
  const [me, setMe] = useState(null)
  const [identities, setIdentities] = useState([])
  const [items, setItems] = useState([])
  const [subs, setSubs] = useState([])
  const [filter, setFilter] = useState('')
  const [showThinking, setShowThinking] = useState(false)
  const [error, setError] = useState('')
  // Open conversations, left to right. A click in the sidebar opens one if
  // it isn't already; each carries its own copy of the conversation record
  // so a pane keeps showing what it had even if the list refreshes.
  const [panes, setPanes] = useState([])

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
      setPanes([])
      const [m, ids] = await Promise.all([api.me(), api.identities()])
      setMe(m); setIdentities(ids.identities || []); setError('')
      await loadList()
    } catch (e) { setError(e.message) }
  }

  const openPane = (c) => setPanes((prev) => (prev.some((p) => p.id === c.id) ? prev : [...prev, c]))
  const closePane = (id) => setPanes((prev) => prev.filter((p) => p.id !== id))
  const updatePaneMeta = (id, patch) => setPanes((prev) => prev.map((p) => (p.id === id ? { ...p, ...patch } : p)))

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
          <List items={items} subs={subs} openIds={new Set(panes.map((p) => p.id))} onSelect={openPane} filter={filter} setFilter={setFilter} live={{}}
            onCreate={createChannel} onRename={renameChannel} onDelete={deleteChannel} />
        </aside>
        {panes.length === 0 ? (
          <div className="flex min-w-0 flex-1 items-center justify-center">
            <div className="mx-auto max-w-md text-center text-ink-subdued"><div className="serif text-[20px] text-ink-2">Every session, kept.</div><div className="mt-2 text-[12px]">Pick a recorded session on the left to read it as a chat, or a shared conversation to follow and post. Open more than one to read them side by side.</div></div>
          </div>
        ) : (
          <div className="flex min-h-0 min-w-0 flex-1">
            {panes.map((c) => (
              <Pane key={c.id} conversation={c} theme={theme} showThinking={showThinking} me={me} single={panes.length === 1}
                onClose={() => closePane(c.id)} onSubscribedChange={(id, subscribed) => updatePaneMeta(id, { subscribed })} />
            ))}
          </div>
        )}
      </div>
      <footer className="flex items-center justify-between border-t border-border bg-surface px-4 py-1 text-[11px] text-ink-subdued">
        <span>{error ? <span className="text-danger">{error}</span> : `${items.length} conversations · ${me?.directory || ''}`}</span>
        <span>{panes.length ? `${panes.length} open` : ''} · {theme}</span>
      </footer>
    </div>
  )
}
