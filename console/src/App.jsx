import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Wrench, Brain, Send, RefreshCw, Sun, Moon, ChevronRight, ChevronDown, PanelRight, ArrowDown, Bot } from 'lucide-react'
import { api } from './api'
import Markdown from './Markdown'
import List, { identityColor } from './List'

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
// next user message belong to it. Posts and session rows stand alone. A
// subagent's rows (they carry agent_id) never open a turn: they gather into
// one collapsed group, placed where the agent first appears, in the turn
// that started it.
function groupTurns(events) {
  const turns = []
  let cur = null
  const groups = new Map()
  for (const e of events) {
    const k = e.kind || ''
    if (e.agent_id) {
      let g = groups.get(e.agent_id)
      if (!g) {
        if (!cur) { cur = { kind: 'turn', prompt: null, steps: [], answer: [], pos: e.position }; turns.push(cur) }
        g = { kind: 'subagent.group', event_id: 'group:' + e.agent_id, agent_id: e.agent_id, start: null, rows: [] }
        groups.set(e.agent_id, g)
        cur.steps.push(g)
      }
      if (k === 'subagent.start' && !g.start) g.start = e
      else g.rows.push(e)
      continue
    }
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

// A subagent's work, collapsed to one line until opened: its label, then its
// prompt, text, thinking and tool calls in order.
function SubagentGroup({ g, theme, onSelect, selected, showThinking }) {
  const [open, setOpen] = useState(false)
  const label = subagentLabel(g)
  const rows = g.rows.filter((e) => showThinking || e.kind !== 'assistant.thinking')
  return (
    <div className="border-l-2 border-border pl-3 my-1">
      <button className="flex w-full items-center gap-1 text-left text-[11px] text-ink-subdued hover:text-ink-2" onClick={() => setOpen(!open)}>
        {open ? <ChevronDown size={11} /> : <ChevronRight size={11} />}<Bot size={11} /><span className="text-ink-2">{label}</span><span>{rows.length} row{rows.length === 1 ? '' : 's'}</span>
        {g.start && <span className="mono">{when(g.start.ts_ms)}</span>}
      </button>
      {open && rows.map((e) => {
        if (e.kind === 'user.message' || e.kind === 'assistant.text') {
          return (
            <div key={e.event_id} onClick={() => onSelect(e)} className={`cursor-pointer py-1 ${selected?.event_id === e.event_id ? 'ring-1 ring-accent' : ''}`}>
              <div className="text-[11px] text-ink-subdued">{e.kind === 'user.message' ? 'prompt' : 'reply'} <span className="mono">{when(e.ts_ms)}</span></div>
              <Markdown text={textOf(e)} theme={theme} />
            </div>
          )
        }
        return <Step key={e.event_id} e={e} theme={theme} onSelect={onSelect} selected={selected?.event_id === e.event_id} showThinking={showThinking} />
      })}
    </div>
  )
}

function subagentLabel(g) {
  const s = g.start || g.rows.find((e) => e.agent_type) || {}
  const desc = s.content && typeof s.content === 'object' ? s.content.description : ''
  return ['subagent', s.agent_type, desc].filter(Boolean).join(' · ')
}

function stepSummary(e) {
  const k = e.kind
  if (k === 'subagent.group') return subagentLabel(e)
  if (k === 'assistant.thinking') return 'thinking'
  if (k === 'tool.use') return `${e.tool_name} · ${textOf(e).split('\n')[0].slice(0, 70)}`
  if (k === 'tool.result') return `${e.tool_name} result${e.content?.is_error ? ' · error' : ''}`
  return k
}

function Turn({ t, theme, onSelect, selected, showThinking }) {
  const [stepsOpen, setStepsOpen] = useState(false)
  const hasSteps = t.steps.length > 0
  const visibleSteps = t.steps.filter((e) => showThinking || e.kind !== 'assistant.thinking')
  return (
    <div className="flex">
      <Rail pos={t.pos} />
      <div className="min-w-0 flex-1 card my-2">
        {t.prompt && (
          <div onClick={() => onSelect(t.prompt)} className={`cursor-pointer border-b border-border bg-raised/60 px-4 py-2 ${selected?.event_id === t.prompt.event_id ? 'ring-1 ring-accent' : ''}`}>
            <div className="flex items-center gap-2 text-[11px] text-ink-subdued"><span className="text-ink-2 font-medium">{t.prompt.identity || 'user'}</span><span className="mono">{when(t.prompt.ts_ms)}</span></div>
            <Markdown text={textOf(t.prompt)} theme={theme} />
          </div>
        )}
        {hasSteps && (
          <div className="px-4 py-1">
            <button className="flex w-full items-center gap-1 text-left text-[11px] text-ink-subdued hover:text-ink-2" onClick={() => setStepsOpen(!stepsOpen)}>
              {stepsOpen ? <ChevronDown size={11} /> : <ChevronRight size={11} />} {visibleSteps.length} step{visibleSteps.length === 1 ? '' : 's'}
              {!stepsOpen && <span className="ml-2 truncate text-ink-hint">{visibleSteps.map(stepSummary).join(' → ').slice(0, 160)}</span>}
            </button>
            {stepsOpen && visibleSteps.map((e) => e.kind === 'subagent.group'
              ? <SubagentGroup key={e.event_id} g={e} theme={theme} onSelect={onSelect} selected={selected} showThinking={showThinking} />
              : <Step key={e.event_id} e={e} theme={theme} onSelect={onSelect} selected={selected?.event_id === e.event_id} showThinking={showThinking} />)}
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
    const reply = !!e.reply_to
    return (
      <div className="flex">
        <Rail pos={e.position} />
        <div onClick={() => onSelect(e)} className={`min-w-0 flex-1 card my-1.5 cursor-pointer px-4 py-2.5 ${reply ? 'ml-8 border-l-2' : ''} ${selected?.event_id === e.event_id ? 'ring-1 ring-accent' : ''}`} style={reply ? { borderLeftColor: identityColor(e.identity) } : undefined}>
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-ink-subdued">
            <span className="font-medium" style={{ color: identityColor(e.identity) }}>{e.participant || e.identity || '?'}</span>
            {e.participant && e.identity && <span className="text-[10px] text-ink-subdued" title="the handle is self-declared; this is the identity that holds the write grant">{e.identity}</span>}
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

export default function App() {
  const [theme, setTheme] = useState(() => { try { return localStorage.getItem('parley.theme') || 'chalkboard' } catch { return 'chalkboard' } })
  const [me, setMe] = useState(null)
  const [items, setItems] = useState([])
  const [subs, setSubs] = useState([])
  const [atBottom, setAtBottom] = useState(true)
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState(null)
  const [events, setEvents] = useState([])
  const [head, setHead] = useState(0)
  const [follow, setFollow] = useState(true)
  const [showThinking, setShowThinking] = useState(false)
  const [inspect, setInspect] = useState(false)
  const [row, setRow] = useState(null)
  const [draft, setDraft] = useState('')
  const [kind, setKind] = useState('comment')
  const [error, setError] = useState('')
  const bottom = useRef(null)
  const scroller = useRef(null)
  const nextRef = useRef(0)
  const fromRef = useRef(0) // the first position loaded; older rows load on demand
  const [older, setOlder] = useState(false) // rows exist before fromRef
  const loadingOlder = useRef(false)
  const jump = useRef(false) // land at the bottom instantly after opening
  const anchor = useRef(null) // scroll height before prepending older rows

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
  useEffect(() => { api.me().then(setMe).catch((e) => setError(e.message)); loadList(); const t = setInterval(loadList, 15000); return () => clearInterval(t) }, [loadList])

  // Opening a conversation loads its tail and lands at the bottom. Nothing
  // older is fetched until the reader scrolls up for it.
  const [opened, setOpened] = useState(0)
  useEffect(() => {
    if (!selected) return
    let stop = false
    setEvents([]); setRow(null); nextRef.current = 0; fromRef.current = 0; setOlder(false)
    api.tail(selected.id, 300).then((r) => {
      if (stop) return
      fromRef.current = r.from; nextRef.current = r.next
      setOlder(r.from > 0); setHead(r.head)
      jump.current = true
      setEvents(r.events)
      setOpened((n) => n + 1)
    }).catch((e) => { if (!stop) setError(e.message) })
    return () => { stop = true }
  }, [selected])

  // Live: only rows after the last one loaded.
  useEffect(() => {
    if (!selected || !follow || !opened) return
    let stop = false
    let busy = false
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
    const t = setInterval(pull, 1500)
    return () => { stop = true; clearInterval(t) }
  }, [selected, follow, opened])

  const loadOlder = useCallback(async () => {
    if (!selected || loadingOlder.current || fromRef.current <= 0) return
    loadingOlder.current = true
    try {
      const from = Math.max(0, fromRef.current - 300)
      const r = await api.events(selected.id, from, fromRef.current, 300)
      anchor.current = scroller.current ? scroller.current.scrollHeight - scroller.current.scrollTop : null
      setEvents((prev) => { const seen = new Set(prev.map((e) => e.position)); return [...r.events.filter((e) => !seen.has(e.position)), ...prev] })
      fromRef.current = from
      setOlder(from > 0)
    } catch (e) { setError(e.message) } finally { loadingOlder.current = false }
  }, [selected])

  // Keep the reader's place when older rows are prepended; land at the
  // bottom right after opening; follow new rows only when already there.
  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    if (anchor.current != null) {
      el.scrollTop = el.scrollHeight - anchor.current
      anchor.current = null
      return
    }
    if (jump.current) {
      jump.current = false
      bottom.current?.scrollIntoView({ behavior: 'auto' })
      return
    }
    if (follow && atBottom) bottom.current?.scrollIntoView({ behavior: 'smooth' })
  }, [events]) // eslint-disable-line react-hooks/exhaustive-deps

  const onScroll = (e) => {
    const el = e.currentTarget
    setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80)
    if (el.scrollTop < 80 && older) loadOlder()
  }

  const live = useMemo(() => ({}), [])
  const turns = useMemo(() => groupTurns(events), [events])
  const purpose = useMemo(() => { const p = [...events].reverse().find((e) => e.kind === 'meta.purpose'); return p ? p.content : null }, [events])

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
        <aside className="w-[320px] shrink-0 border-r border-border bg-surface"><List items={items} subs={subs} selected={selected} onSelect={setSelected} filter={filter} setFilter={setFilter} live={live} /></aside>
        <main className="flex min-w-0 flex-1 flex-col">
          <div className="flex min-w-0 items-center justify-between gap-2 border-b border-border bg-surface px-4 py-1.5">
            <div className="truncate"><span className="serif text-[14px] text-ink">{selected ? (purpose?.name || selected.title || selected.name) : 'pick a conversation'}</span>{selected && <span className="ml-2 text-[11px] text-ink-subdued">{selected.title ? selected.name + ' · ' : ''}{events.length} of {head} rows{(purpose?.purpose || selected.description) ? ' · ' + (purpose?.purpose || selected.description) : ''}</span>}</div>
            <div className="flex shrink-0 items-center gap-1.5">
              <button className={showThinking ? btnOn : btn} onClick={() => setShowThinking(!showThinking)}><Brain size={11} className="inline mr-1" />thinking</button>
              <button className={follow ? btnOn : btn} onClick={() => setFollow(!follow)}>{follow ? 'live' : 'paused'}</button>
              {selected?.mode === 'shared' && <button className={btn} onClick={toggleFollowShared}>{selected.subscribed ? 'unfollow' : 'follow'}</button>}
              <button className={inspect ? btnOn : btn} title="show the selected row" onClick={() => setInspect(!inspect)}><PanelRight size={12} /></button>
            </div>
          </div>
          <div ref={scroller} className="relative min-h-0 flex-1 overflow-auto px-4 py-3" onScroll={onScroll}>
            {selected && older && <button className="mx-auto mb-3 block border border-border px-2 py-1 text-[11px] text-ink-2 hover:bg-elevated" onClick={loadOlder}>earlier rows</button>}
            {!selected && <div className="mx-auto mt-24 max-w-md text-center text-ink-subdued"><div className="serif text-[20px] text-ink-2">Every session, kept.</div><div className="mt-2 text-[12px]">Pick a recorded session on the left to read it as a chat, or a shared conversation to follow and post.</div></div>}
            {selected && events.length === 0 && <div className="text-[12px] italic text-ink-subdued">no rows yet</div>}
            {turns.map((t, i) => t.kind === 'turn'
              ? <Turn key={t.prompt?.event_id || 'turn' + i} t={t} theme={theme} onSelect={setRow} selected={row} showThinking={showThinking} />
              : <Row key={t.e.event_id || 'row' + i} e={t.e} theme={theme} onSelect={setRow} selected={row} />)}
            <div ref={bottom} />
            {!atBottom && <button className="sticky bottom-2 left-full mr-2 border border-border bg-elevated px-2 py-1 text-[11px] text-ink-2" onClick={() => { setAtBottom(true); bottom.current?.scrollIntoView({ behavior: 'smooth' }) }}><ArrowDown size={11} className="inline mr-1" />latest</button>}
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
