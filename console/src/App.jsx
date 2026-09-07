import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ThemeProvider, defaultTheme, TerminalContainer, TerminalHeader, TerminalFooter, TerminalSidebar, TerminalButton, TerminalInput,
} from '@quantumwake/terminal-ux-components'
import { MessageSquare, Radio, Bot, User, Wrench, Brain, Send, RefreshCw, Eye, EyeOff } from 'lucide-react'
import { api } from './api'

// parley console: a chat-like view of conversation streams.
//   left    the conversations: your recorded sessions and the tenant's shared ones
//   center  the stream as chat: user and assistant bubbles, thinking folded,
//           tool call + result paired, posts with author and kind; live follow
//   right   the selected row, verbatim
//   bottom  a composer for shared conversations

const muted = 'text-[11px] text-midnight-text-subdued'
const label = 'text-[10px] uppercase tracking-wider text-midnight-text-subdued'
const short = (s) => (s ? String(s).slice(0, 8) : '')
const when = (ms) => (ms ? new Date(ms).toLocaleTimeString() : '')

function textOf(e) {
  const c = e.content
  if (!c) return ''
  if (typeof c === 'string') return c
  if ('text' in c) return c.text || ''
  if (c.input) return typeof c.input === 'string' ? c.input : JSON.stringify(c.input, null, 1)
  if (c.output !== undefined) return typeof c.output === 'string' ? c.output : JSON.stringify(c.output, null, 1)
  return JSON.stringify(c)
}

function Bubble({ e, selected, onSelect, showThinking }) {
  const kind = e.kind || ''
  const mine = kind === 'user.message'
  const isThinking = kind === 'assistant.thinking'
  const isTool = kind === 'tool.use' || kind === 'tool.result'
  const isPost = kind.startsWith('post.')
  const isMeta = kind.startsWith('session.') || kind.startsWith('subagent.') || kind.startsWith('meta.')
  const [open, setOpen] = useState(false)
  if (isThinking && !showThinking) return null

  if (isMeta) {
    return (
      <div onClick={() => onSelect(e)} className={`mx-auto my-1 max-w-[70%] cursor-pointer text-center text-[10px] text-midnight-text-hint ${selected ? 'text-midnight-accent-bright' : ''}`}>
        {kind} {textOf(e) && `· ${textOf(e).slice(0, 80)}`} · {when(e.ts_ms)}
      </div>
    )
  }

  if (isThinking) {
    const t = textOf(e)
    return (
      <div onClick={() => onSelect(e)} className={`my-1 mr-auto max-w-[80%] cursor-pointer border-l-2 border-midnight-accent/40 pl-3 ${selected ? 'bg-midnight-elevated' : ''}`}>
        <div className={`${muted} flex items-center gap-1`}><Brain size={11} /> thinking · {when(e.ts_ms)}</div>
        <div className="whitespace-pre-wrap text-[11px] italic text-midnight-text-muted">{t ? (open || t.length < 240 ? t : t.slice(0, 240) + '…') : '(redacted by the model)'}</div>
        {t.length >= 240 && <button className={`${muted} underline`} onClick={(ev) => { ev.stopPropagation(); setOpen(!open) }}>{open ? 'less' : 'more'}</button>}
      </div>
    )
  }

  if (isTool) {
    const isUse = kind === 'tool.use'
    return (
      <div onClick={() => onSelect(e)} className={`my-1 mr-auto max-w-[85%] cursor-pointer border border-midnight-border bg-midnight-surface p-2 ${selected ? 'border-midnight-accent' : ''}`}>
        <div className={`${muted} flex items-center gap-1`}><Wrench size={11} /> {isUse ? 'call' : 'result'} · {e.tool_name} · {short(e.tool_use_id)} · {when(e.ts_ms)}{e.content?.is_error && <span className="text-midnight-danger-bright"> error</span>}</div>
        <pre className="max-h-48 overflow-auto whitespace-pre-wrap text-[11px] text-midnight-text-body">{textOf(e).slice(0, 2000)}</pre>
      </div>
    )
  }

  if (isPost) {
    return (
      <div onClick={() => onSelect(e)} className={`my-2 mr-auto max-w-[80%] cursor-pointer border border-midnight-border-subtle bg-midnight-elevated p-2 ${selected ? 'border-midnight-accent' : ''}`}>
        <div className={`${muted} flex items-center gap-2`}>
          <span className="text-midnight-electric">{e.author || '?'}</span>
          <span className="rounded border border-midnight-border px-1">{kind.replace('post.', '')}</span>
          {e.to && e.to !== '*' && <span>to {e.to}</span>}
          {e.reply_to && <span>reply to {short(e.reply_to)}</span>}
          <span>{when(e.ts_ms)}</span>
        </div>
        <div className="whitespace-pre-wrap text-[12px] text-midnight-text-secondary">{textOf(e)}</div>
      </div>
    )
  }

  return (
    <div onClick={() => onSelect(e)} className={`my-2 flex ${mine ? 'justify-end' : 'justify-start'}`}>
      <div className={`max-w-[80%] cursor-pointer p-2 ${mine ? 'bg-midnight-accent/20 border border-midnight-accent/40' : 'bg-midnight-surface border border-midnight-border'} ${selected ? 'ring-1 ring-midnight-accent-bright' : ''}`}>
        <div className={`${muted} flex items-center gap-1`}>{mine ? <User size={11} /> : <Bot size={11} />} {mine ? e.author || 'user' : e.model || 'assistant'} · {when(e.ts_ms)}</div>
        <div className="whitespace-pre-wrap text-[12px] text-midnight-text-secondary">{textOf(e)}</div>
      </div>
    </div>
  )
}

function ConversationList({ items, selected, onSelect, filter, setFilter }) {
  const groups = useMemo(() => ({
    shared: items.filter((c) => c.mode === 'shared'),
    agent: items.filter((c) => c.mode !== 'shared'),
  }), [items])
  const row = (c) => (
    <button key={c.id} onClick={() => onSelect(c)} className={`block w-full border-b border-midnight-border px-3 py-2 text-left hover:bg-midnight-elevated ${selected?.id === c.id ? 'bg-midnight-elevated' : ''}`}>
      <div className="flex items-center gap-2 text-[12px] text-midnight-text-secondary">{c.mode === 'shared' ? <Radio size={12} className="text-midnight-electric" /> : <MessageSquare size={12} />}<span className="truncate">{c.name}</span></div>
      <div className={muted}>{c.mode === 'shared' ? `${c.access}${c.subscribed ? ' · following ' + c.subscribed : ''}` : `${c.agent || ''} · ${short(c.session)}`}{c.description ? ` · ${c.description}` : ''}</div>
    </button>
  )
  return (
    <div className="flex h-full flex-col">
      <div className="p-2"><TerminalInput placeholder="filter" value={filter} onChange={(e) => setFilter(e.target.value)} /></div>
      <div className="overflow-auto">
        <div className={`${label} px-3 pt-2`}>shared conversations</div>
        {groups.shared.length ? groups.shared.map(row) : <div className={`${muted} px-3 py-2 italic`}>none yet</div>}
        <div className={`${label} px-3 pt-3`}>recorded sessions</div>
        {groups.agent.map(row)}
      </div>
    </div>
  )
}

function Inspector({ e }) {
  if (!e) return <div className={`${muted} p-3 italic`}>select a row to inspect it</div>
  return <pre className="overflow-auto p-3 text-[10px] leading-relaxed text-midnight-text-body">{JSON.stringify(e, null, 2)}</pre>
}

export default function App() {
  const [me, setMe] = useState(null)
  const [items, setItems] = useState([])
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState(null)
  const [events, setEvents] = useState([])
  const [head, setHead] = useState(0)
  const [follow, setFollow] = useState(true)
  const [showThinking, setShowThinking] = useState(true)
  const [row, setRow] = useState(null)
  const [draft, setDraft] = useState('')
  const [kind, setKind] = useState('comment')
  const [error, setError] = useState('')
  const bottom = useRef(null)
  const nextRef = useRef(0)

  const loadList = useCallback(async () => {
    try {
      const r = await api.conversations({ limit: 200 })
      setItems(r.conversations || [])
    } catch (e) { setError(e.message) }
  }, [])

  useEffect(() => { api.me().then(setMe).catch((e) => setError(e.message)); loadList() }, [loadList])

  // Open a conversation: load everything, then follow the head.
  useEffect(() => {
    if (!selected) return
    let stop = false
    setEvents([]); setRow(null); nextRef.current = 0
    const pull = async () => {
      try {
        const r = await api.events(selected.id, nextRef.current, 0, 500)
        if (stop) return
        if (r.events.length) { setEvents((prev) => [...prev, ...r.events]); nextRef.current = r.next }
        setHead(r.head)
      } catch (e) { if (!stop) setError(e.message) }
    }
    pull()
    const t = setInterval(() => { if (follow) pull() }, 1500)
    return () => { stop = true; clearInterval(t) }
  }, [selected, follow])

  useEffect(() => { if (follow) bottom.current?.scrollIntoView({ behavior: 'smooth' }) }, [events, follow])

  const visible = useMemo(() => items.filter((c) => !filter || (c.name + ' ' + (c.description || '') + ' ' + (c.agent || '')).toLowerCase().includes(filter.toLowerCase())), [items, filter])

  const send = async () => {
    if (!selected || !draft.trim()) return
    try {
      await api.post(selected.id, { kind, text: draft })
      setDraft('')
    } catch (e) { setError(e.message) }
  }

  const toggleFollowShared = async () => {
    if (!selected) return
    try {
      if (selected.subscribed) await api.unsubscribe(selected.name); else await api.subscribe(selected.name, 'full')
      await loadList()
      setSelected((s) => ({ ...s, subscribed: s.subscribed ? '' : 'full' }))
    } catch (e) { setError(e.message) }
  }

  return (
    <ThemeProvider theme={defaultTheme}>
      <TerminalContainer className="flex h-screen flex-col">
        <TerminalHeader
          leftContent={<div className="flex items-center gap-3 text-[12px]"><span className="font-semibold text-midnight-text-primary">parley</span><span className={muted}>statefs.ai · conversations</span></div>}
          rightContent={<div className={`${muted} flex items-center gap-3`}>{me && <span>{me.username} @ {me.tenant}</span>}<TerminalButton size="small" variant="ghost" onClick={loadList}><RefreshCw size={12} /></TerminalButton></div>} />
        <div className="flex min-h-0 flex-1">
          <TerminalSidebar isOpen position="left" defaultWidth={300} mainContent={<ConversationList items={visible} selected={selected} onSelect={setSelected} filter={filter} setFilter={setFilter} />} />
          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex items-center justify-between border-b border-midnight-border px-3 py-1">
              <div className="truncate text-[12px] text-midnight-text-secondary">{selected ? selected.name : 'pick a conversation'}{selected && <span className={muted}> · {events.length}/{head} rows</span>}</div>
              <div className="flex items-center gap-2">
                <TerminalButton size="small" variant="ghost" onClick={() => setShowThinking(!showThinking)}>{showThinking ? <Eye size={12} /> : <EyeOff size={12} />} thinking</TerminalButton>
                <TerminalButton size="small" variant={follow ? 'primary' : 'ghost'} onClick={() => setFollow(!follow)}>{follow ? 'live' : 'paused'}</TerminalButton>
                {selected?.mode === 'shared' && <TerminalButton size="small" variant="ghost" onClick={toggleFollowShared}>{selected.subscribed ? 'unfollow' : 'follow'}</TerminalButton>}
              </div>
            </div>
            <div className="min-h-0 flex-1 overflow-auto px-4 py-2">
              {selected && events.length === 0 && <div className={`${muted} italic`}>no rows yet</div>}
              {events.map((e) => <Bubble key={e.event_id || e.position} e={e} selected={row?.event_id === e.event_id} onSelect={setRow} showThinking={showThinking} />)}
              <div ref={bottom} />
            </div>
            {selected?.mode === 'shared' && (
              <div className="flex items-center gap-2 border-t border-midnight-border p-2">
                <select value={kind} onChange={(e) => setKind(e.target.value)} className="bg-midnight-surface text-[11px] text-midnight-text-body border border-midnight-border px-1 py-1">
                  {['comment', 'question', 'answer', 'report', 'status'].map((k) => <option key={k}>{k}</option>)}
                </select>
                <div className="flex-1"><TerminalInput placeholder={`post to ${selected.name} as ${me?.username || 'me'}`} value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') send() }} /></div>
                <TerminalButton size="small" onClick={send}><Send size={12} /> post</TerminalButton>
              </div>
            )}
          </div>
          <TerminalSidebar isOpen position="right" defaultWidth={340} mainContent={<Inspector e={row} />} />
        </div>
        <TerminalFooter leftContent={<span className={muted}>{error ? <span className="text-midnight-danger-bright">{error}</span> : `${items.length} conversations · ${me?.directory || ''}`}</span>} rightContent={<span className={muted}>{follow ? 'polling 1.5s' : 'paused'}</span>} />
      </TerminalContainer>
    </ThemeProvider>
  )
}
