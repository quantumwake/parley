import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { MessageSquare, Radio, Wrench, Brain, Send, RefreshCw, Sun, Moon, ChevronRight, ChevronDown, PanelRight } from 'lucide-react'
import { api } from './api'
import Markdown from './Markdown'

// parley: a conversation stream read like a chat.
//   left    conversations, shared first, then this machine's recorded sessions
//   center  the stream: the position rail on the left is statefs's own row
//           sequence; a prompt followed by everything the agent did to answer it
//           (thinking, tool calls, the answer) is ONE turn card
//   right   the selected row verbatim (toggle)
//   bottom  a composer on shared conversations

const short = (s) => (s ? String(s).slice(0, 8) : '')
const when = (ms) => (ms ? new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '')
const dateOf = (ms) => (ms ? new Date(ms).toLocaleDateString([], { month: 'short', day: 'numeric' }) : '')

function textOf(e) {
  const c = e.content
  if (!c) return ''
  if (typeof c === 'string') return c
  if ('text' in c) return c.text || ''
  if (c.input !== undefined) return toolInput(e.tool_name, c.input)
  if (c.output !== undefined) return toolOutput(c.output)
  return JSON.stringify(c, null, 2)
}

// A shell call reads as its command; a file edit as its path; anything else as JSON.
function toolInput(tool, input) {
  if (typeof input === 'string') return input
  if (!input || typeof input !== 'object') return JSON.stringify(input)
  if (tool === 'Bash' && input.command) return input.command + (input.description ? `\n# ${input.description}` : '')
  if ((tool === 'Read' || tool === 'Write' || tool === 'Edit') && input.file_path) {
    const rest = { ...input }; delete rest.file_path
    if (tool === 'Write' && typeof rest.content === 'string') return `${input.file_path}\n\n${rest.content}`
    return input.file_path + (Object.keys(rest).length ? '\n' + JSON.stringify(rest, null, 2) : '')
  }
  return JSON.stringify(input, null, 2)
}

// A tool result reads as what the agent saw: stdout and stderr for shells,
// text content for MCP-style results, JSON otherwise.
function toolOutput(out) {
  if (typeof out === 'string') return out
  if (!out || typeof out !== 'object') return JSON.stringify(out)
  if ('stdout' in out || 'stderr' in out) {
    const parts = []
    if (out.stdout) parts.push(out.stdout)
    if (out.stderr) parts.push('stderr: ' + out.stderr)
    if (out.interrupted) parts.push('(interrupted)')
    return parts.join('\n') || '(no output)'
  }
  if (typeof out.content === 'string') return out.content
  if (Array.isArray(out.content)) return out.content.map((b) => (typeof b === 'string' ? b : b.text || JSON.stringify(b))).join('\n')
  if (typeof out.text === 'string') return out.text
  return JSON.stringify(out, null, 2)
}

// Group rows into turns: a user message opens a turn; assistant rows until the
// next user message belong to it. Posts and session rows stand alone.
function groupTurns(events) {
  const turns = []
  let cur = null
  for (const e of events) {
    const k = e.kind || ''
    if (k === 'user.message') { cur = { kind: 'turn', prompt: e, steps: [], answer: [], pos: e.position }; turns.push(cur); continue }
    if (k.startsWith('assistant.') || k.startsWith('tool.') || k.startsWith('subagent.')) {
      if (!cur) { cur = { kind: 'turn', prompt: null, steps: [], answer: [], pos: e.position }; turns.push(cur) }
      if (k === 'assistant.text') cur.answer.push(e); else cur.steps.push(e)
      continue
    }
    turns.push({ kind: 'row', e, pos: e.position })
    cur = null
  }
  return turns
}

function Rail({ pos }) { return <div className="rail w-10 shrink-0 pt-1 text-right pr-3 select-none">{pos}</div> }

function Step({ e, theme, onSelect, selected, showThinking }) {
  const k = e.kind
  const [open, setOpen] = useState(false)
  if (k === 'assistant.thinking') {
    if (!showThinking) return null
    const t = textOf(e)
    return (
      <div onClick={() => onSelect(e)} className={`cursor-pointer border-l-2 border-accent/50 pl-3 py-1 ${selected ? 'bg-elevated' : ''}`}>
        <div className="flex items-center gap-1 text-[11px] text-ink-subdued"><Brain size={11} /> thinking <span className="mono">{when(e.ts_ms)}</span></div>
        {t ? <div className="prose italic text-ink-muted">{open || t.length < 300 ? t : t.slice(0, 300) + '…'}{t.length >= 300 && <button className="ml-1 underline" onClick={(ev) => { ev.stopPropagation(); setOpen(!open) }}>{open ? 'less' : 'more'}</button>}</div>
          : <div className="text-[11px] italic text-ink-hint">not shown by the model</div>}
      </div>
    )
  }
  if (k === 'tool.use' || k === 'tool.result') {
    const isUse = k === 'tool.use'
    const body = textOf(e)
    const long = body.length > 600
    return (
      <div onClick={() => onSelect(e)} className={`cursor-pointer border border-border bg-elevated/60 my-1 ${selected ? 'border-accent' : ''}`}>
        <div className="flex items-center gap-2 px-2 py-1 text-[11px] text-ink-subdued">
          <Wrench size={11} /><span className="text-ink-2">{e.tool_name}</span><span>{isUse ? 'call' : 'result'}</span><span className="mono">{short(e.tool_use_id)}</span><span className="mono">{when(e.ts_ms)}</span>
          {e.content?.is_error && <span className="text-danger">error</span>}
          {long && <button className="ml-auto underline" onClick={(ev) => { ev.stopPropagation(); setOpen(!open) }}>{open ? 'collapse' : 'expand'}</button>}
        </div>
        <pre className={`mono px-2 pb-2 text-[11px] leading-relaxed text-ink-body whitespace-pre-wrap ${open ? '' : 'max-h-40 overflow-hidden'}`}>{long && !open ? body.slice(0, 600) + '…' : body}</pre>
      </div>
    )
  }
  return (
    <div onClick={() => onSelect(e)} className="text-[11px] text-ink-hint py-0.5">{k} {e.agent_type ? `· ${e.agent_type}` : ''} <span className="mono">{when(e.ts_ms)}</span></div>
  )
}

function Turn({ t, theme, onSelect, selected, showThinking }) {
  const [stepsOpen, setStepsOpen] = useState(true)
  const hasSteps = t.steps.length > 0
  return (
    <div className="flex">
      <Rail pos={t.pos} />
      <div className="min-w-0 flex-1 card my-2">
        {t.prompt && (
          <div onClick={() => onSelect(t.prompt)} className={`cursor-pointer border-b border-border bg-raised/60 px-4 py-2 ${selected?.event_id === t.prompt.event_id ? 'ring-1 ring-accent' : ''}`}>
            <div className="flex items-center gap-2 text-[11px] text-ink-subdued"><span className="text-ink-2 font-medium">{t.prompt.author || 'user'}</span><span className="mono">{when(t.prompt.ts_ms)}</span></div>
            <Markdown text={textOf(t.prompt)} theme={theme} />
          </div>
        )}
        {hasSteps && (
          <div className="px-4 py-1">
            <button className="flex items-center gap-1 text-[11px] text-ink-subdued hover:text-ink-2" onClick={() => setStepsOpen(!stepsOpen)}>
              {stepsOpen ? <ChevronDown size={11} /> : <ChevronRight size={11} />} {t.steps.length} step{t.steps.length > 1 ? 's' : ''}
            </button>
            {stepsOpen && t.steps.map((e) => <Step key={e.event_id} e={e} theme={theme} onSelect={onSelect} selected={selected?.event_id === e.event_id} showThinking={showThinking} />)}
          </div>
        )}
        {t.answer.map((e) => (
          <div key={e.event_id} onClick={() => onSelect(e)} className={`cursor-pointer px-4 py-3 ${selected?.event_id === e.event_id ? 'ring-1 ring-accent' : ''}`}>
            <div className="flex items-center gap-2 text-[11px] text-ink-subdued"><span className="text-accent-bright">{e.model || 'assistant'}</span><span className="mono">{when(e.ts_ms)}</span>{(e.tokens_out || 0) > 0 && <span>{e.tokens_in}→{e.tokens_out} tok</span>}</div>
            <Markdown text={textOf(e)} theme={theme} />
          </div>
        ))}
      </div>
    </div>
  )
}

function Row({ e, theme, onSelect, selected }) {
  const k = e.kind || ''
  if (k.startsWith('post.')) {
    return (
      <div className="flex">
        <Rail pos={e.position} />
        <div onClick={() => onSelect(e)} className={`min-w-0 flex-1 card my-2 cursor-pointer px-4 py-3 ${selected?.event_id === e.event_id ? 'ring-1 ring-accent' : ''}`}>
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-ink-subdued">
            <span className="text-info font-medium">{e.author || '?'}</span>
            <span className="border border-border px-1">{k.replace('post.', '')}</span>
            {e.to && e.to !== '*' && <span>to {e.to}</span>}
            {e.reply_to && <span>reply to <span className="mono">{short(e.reply_to)}</span></span>}
            <span className="mono">{dateOf(e.ts_ms)} {when(e.ts_ms)}</span>
          </div>
          <Markdown text={textOf(e)} theme={theme} />
        </div>
      </div>
    )
  }
  return (
    <div className="flex">
      <Rail pos={e.position} />
      <div onClick={() => onSelect(e)} className={`min-w-0 flex-1 cursor-pointer py-1 text-[11px] text-ink-hint ${selected?.event_id === e.event_id ? 'text-accent' : ''}`}>
        {k} {textOf(e) && <span className="mono">· {textOf(e).slice(0, 90)}</span>} <span className="mono">· {dateOf(e.ts_ms)} {when(e.ts_ms)}</span>
      </div>
    </div>
  )
}

function ConversationList({ items, selected, onSelect, filter, setFilter }) {
  const shared = items.filter((c) => c.mode === 'shared')
  const agent = items.filter((c) => c.mode !== 'shared').sort((a, b) => (Number(b.started_ms) || 0) - (Number(a.started_ms) || 0) || b.name.localeCompare(a.name))
  const row = (c) => (
    <button key={c.id} onClick={() => onSelect(c)} className={`block w-full border-b border-border px-3 py-2 text-left hover:bg-elevated ${selected?.id === c.id ? 'bg-elevated' : ''}`}>
      <div className="flex items-center gap-2 text-[12.5px] text-ink-2">{c.mode === 'shared' ? <Radio size={12} className="text-info" /> : <MessageSquare size={12} className="text-ink-subdued" />}<span className="truncate serif">{c.title || c.name}</span></div>
      <div className="truncate text-[11px] text-ink-subdued">{c.mode === 'shared' ? `${c.access}${c.subscribed ? ' · following ' + c.subscribed : ''}${c.description ? ' · ' + c.description : ''}` : `${c.description || c.name}`}</div>
    </button>
  )
  return (
    <div className="flex h-full flex-col">
      <div className="p-2 border-b border-border"><input className="w-full bg-elevated border border-border px-2 py-1 text-[12px] text-ink-2 placeholder:text-ink-hint outline-none focus:border-accent" placeholder="filter conversations" value={filter} onChange={(e) => setFilter(e.target.value)} /></div>
      <div className="overflow-auto">
        <div className="px-3 pt-3 pb-1 text-[10px] uppercase tracking-wider text-ink-subdued">shared</div>
        {shared.length ? shared.map(row) : <div className="px-3 py-2 text-[11px] italic text-ink-hint">none yet · parley create &lt;name&gt;</div>}
        <div className="px-3 pt-4 pb-1 text-[10px] uppercase tracking-wider text-ink-subdued">recorded sessions</div>
        {agent.map(row)}
      </div>
    </div>
  )
}

export default function App() {
  const [theme, setTheme] = useState(() => { try { return localStorage.getItem('parley.theme') || 'chalkboard' } catch { return 'chalkboard' } })
  const [me, setMe] = useState(null)
  const [items, setItems] = useState([])
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState(null)
  const [events, setEvents] = useState([])
  const [head, setHead] = useState(0)
  const [follow, setFollow] = useState(true)
  const [showThinking, setShowThinking] = useState(true)
  const [inspect, setInspect] = useState(false)
  const [row, setRow] = useState(null)
  const [draft, setDraft] = useState('')
  const [kind, setKind] = useState('comment')
  const [error, setError] = useState('')
  const bottom = useRef(null)
  const nextRef = useRef(0)

  useEffect(() => {
    document.documentElement.classList.toggle('theme-paper', theme === 'paper')
    try { localStorage.setItem('parley.theme', theme) } catch { /* private mode */ }
  }, [theme])

  const loadList = useCallback(async () => {
    try { const r = await api.conversations({ limit: 200 }); setItems(r.conversations || []); setError('') } catch (e) { setError(e.message) }
  }, [])
  useEffect(() => { api.me().then(setMe).catch((e) => setError(e.message)); loadList() }, [loadList])

  useEffect(() => {
    if (!selected) return
    let stop = false
    let busy = false
    setEvents([]); setRow(null); nextRef.current = 0
    const pull = async () => {
      if (busy) return
      busy = true
      try {
        const r = await api.events(selected.id, nextRef.current, 0, 500)
        if (stop) return
        if (r.events.length) {
          setEvents((prev) => { const seen = new Set(prev.map((e) => e.position)); return [...prev, ...r.events.filter((e) => !seen.has(e.position))] })
          nextRef.current = Math.max(nextRef.current, r.next)
        }
        setHead(r.head)
      } catch (e) { if (!stop) setError(e.message) } finally { busy = false }
    }
    pull()
    const t = setInterval(() => { if (follow) pull() }, 1500)
    return () => { stop = true; clearInterval(t) }
  }, [selected, follow])

  useEffect(() => { if (follow) bottom.current?.scrollIntoView({ behavior: 'smooth' }) }, [events, follow])

  const visible = useMemo(() => items.filter((c) => !filter || (c.name + ' ' + (c.title || '') + ' ' + (c.description || '') + ' ' + (c.agent || '')).toLowerCase().includes(filter.toLowerCase())), [items, filter])
  const turns = useMemo(() => groupTurns(events), [events])

  const send = async () => {
    if (!selected || !draft.trim()) return
    try { await api.post(selected.id, { kind, text: draft }); setDraft(''); setError('') } catch (e) { setError(e.message) }
  }
  const toggleFollowShared = async () => {
    if (!selected) return
    try {
      if (selected.subscribed) await api.unsubscribe(selected.name); else await api.subscribe(selected.name, 'full')
      await loadList(); setSelected((s) => ({ ...s, subscribed: s.subscribed ? '' : 'full' }))
    } catch (e) { setError(e.message) }
  }

  const btn = 'px-2 py-1 text-[11px] border border-border text-ink-2 hover:bg-elevated'
  const btnOn = 'px-2 py-1 text-[11px] border border-accent bg-accent/15 text-ink'

  return (
    <div className="flex h-screen w-screen max-w-full flex-col overflow-hidden bg-base text-ink-body">
      <header className="flex min-w-0 items-center justify-between gap-3 border-b border-border bg-surface px-4 py-2" style={{ boxShadow: 'var(--shadow)' }}>
        <div className="flex items-baseline gap-3"><span className="serif text-[17px] font-semibold text-ink">parley</span><span className="text-[11px] text-ink-subdued">statefs.ai · conversations</span></div>
        <div className="flex shrink-0 items-center gap-2 text-[11px] text-ink-subdued">
          {me && <span className="truncate">{me.username} <span className="text-ink-hint">@</span> {me.tenant}</span>}
          <button className={btn} title="switch theme" onClick={() => setTheme(theme === 'paper' ? 'chalkboard' : 'paper')}>{theme === 'paper' ? <Moon size={12} /> : <Sun size={12} />}</button>
          <button className={btn} title="reload the list" onClick={loadList}><RefreshCw size={12} /></button>
        </div>
      </header>
      <div className="flex min-h-0 flex-1">
        <aside className="w-[300px] shrink-0 border-r border-border bg-surface"><ConversationList items={visible} selected={selected} onSelect={setSelected} filter={filter} setFilter={setFilter} /></aside>
        <main className="flex min-w-0 flex-1 flex-col">
          <div className="flex min-w-0 items-center justify-between gap-2 border-b border-border bg-surface px-4 py-1.5">
            <div className="truncate"><span className="serif text-[14px] text-ink">{selected ? (selected.title || selected.name) : 'pick a conversation'}</span>{selected && <span className="ml-2 text-[11px] text-ink-subdued">{selected.title ? selected.name + ' · ' : ''}{events.length} of {head} rows{selected.description ? ' · ' + selected.description : ''}</span>}</div>
            <div className="flex shrink-0 items-center gap-1.5">
              <button className={showThinking ? btnOn : btn} onClick={() => setShowThinking(!showThinking)}><Brain size={11} className="inline mr-1" />thinking</button>
              <button className={follow ? btnOn : btn} onClick={() => setFollow(!follow)}>{follow ? 'live' : 'paused'}</button>
              {selected?.mode === 'shared' && <button className={btn} onClick={toggleFollowShared}>{selected.subscribed ? 'unfollow' : 'follow'}</button>}
              <button className={inspect ? btnOn : btn} title="show the selected row" onClick={() => setInspect(!inspect)}><PanelRight size={12} /></button>
            </div>
          </div>
          <div className="min-h-0 flex-1 overflow-auto px-4 py-3">
            {!selected && <div className="mx-auto mt-24 max-w-md text-center text-ink-subdued"><div className="serif text-[20px] text-ink-2">Every session, kept.</div><div className="mt-2 text-[12px]">Pick a recorded session on the left to read it as a chat, or a shared conversation to follow and post.</div></div>}
            {selected && events.length === 0 && <div className="text-[12px] italic text-ink-subdued">no rows yet</div>}
            {turns.map((t, i) => t.kind === 'turn'
              ? <Turn key={t.prompt?.event_id || 'turn' + i} t={t} theme={theme} onSelect={setRow} selected={row} showThinking={showThinking} />
              : <Row key={t.e.event_id || 'row' + i} e={t.e} theme={theme} onSelect={setRow} selected={row} />)}
            <div ref={bottom} />
          </div>
          {selected?.mode === 'shared' && (
            <div className="flex items-center gap-2 border-t border-border bg-surface p-2">
              <select value={kind} onChange={(e) => setKind(e.target.value)} className="bg-elevated border border-border px-1 py-1 text-[11px] text-ink-2">
                {['comment', 'question', 'answer', 'report', 'status'].map((k) => <option key={k}>{k}</option>)}
              </select>
              <input className="flex-1 bg-elevated border border-border px-2 py-1.5 text-[12.5px] text-ink outline-none focus:border-accent placeholder:text-ink-hint" placeholder={`post to ${selected.name} as ${me?.username || 'me'} (markdown ok)`} value={draft} onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) send() }} />
              <button className={btnOn} onClick={send}><Send size={12} className="inline mr-1" />post</button>
            </div>
          )}
        </main>
        {inspect && <aside className="w-[360px] shrink-0 border-l border-border bg-surface overflow-auto">{row ? <pre className="mono p-3 text-[10.5px] leading-relaxed text-ink-body whitespace-pre-wrap">{JSON.stringify(row, null, 2)}</pre> : <div className="p-3 text-[11px] italic text-ink-subdued">select a row to see it verbatim</div>}</aside>}
      </div>
      <footer className="flex items-center justify-between border-t border-border bg-surface px-4 py-1 text-[11px] text-ink-subdued">
        <span>{error ? <span className="text-danger">{error}</span> : `${items.length} conversations · ${me?.directory || ''}`}</span>
        <span>{follow ? 'live · 1.5 s' : 'paused'} · {theme}</span>
      </footer>
    </div>
  )
}
