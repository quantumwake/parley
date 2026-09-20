import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Wrench, Brain, Send, ChevronRight, ChevronDown, PanelRight, ArrowDown, Bot, Users, X, Reply } from 'lucide-react'
import Share from './Share'
import { api } from './api'
import Markdown from './Markdown'
import { identityColor } from './List'
import { addressOf, filterKinds, filterPeople, mentionsIn, parseComposer, replaceToken, tokenAt } from './composer'

// One open conversation: its own stream, composer and inspector. App
// renders one Pane per open conversation, side by side, so several
// conversations can be read and posted to at once.

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
    if (k === 'subagent.stop' && !e.agent_type) continue // Claude Code's own side agents, recorded by older versions
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

// Delivery verdicts, coloured like the status line and the list's badges.
const verdictCls = { react: 'text-danger', context: 'text-ink-subdued', display: 'text-accent-bright', ignore: 'text-ink-hint' }
const verdictTitle = {
  react: 'recorded here: reacted to — woke an agent',
  context: 'recorded here: shown to the agent on its next turn',
  display: "recorded here: shown to you only, kept out of the agent's context",
  ignore: 'recorded here: counted, shown to nobody',
}

// A question or request's state, from /work: open, claimed by X, or
// answered by X (a question); claimed by X or closed (<outcome>) otherwise.
function workLabel(m) {
  if (!m) return ''
  if (m.kind === 'question') {
    if (m.state === 'claimed') return `claimed by ${m.holder}`
    if (m.outcome === 'answered') return `answered by ${m.holder}`
    if (m.state === 'closed') return `closed (${m.outcome})`
    return 'open'
  }
  if (m.state === 'claimed') return `claimed by ${m.holder}`
  if (m.state === 'closed') return `closed (${m.outcome})`
  return 'open'
}

function Row({ e, theme, onSelect, selected, verdict, work, refCb, highlighted, onReply }) {
  const k = e.kind || ''
  if (k.startsWith('post.')) {
    const reply = !!e.reply_to
    const showWork = k === 'post.question' || k === 'post.request'
    return (
      <div className="flex" ref={refCb}>
        <Rail pos={e.position} />
        <div onClick={() => onSelect(e)} className={`min-w-0 flex-1 card my-1.5 cursor-pointer px-4 py-2.5 ${reply ? 'ml-8 border-l-2' : ''} ${selected?.event_id === e.event_id ? 'ring-1 ring-accent' : ''} ${highlighted ? 'ring-2 ring-accent-bright' : ''}`} style={reply ? { borderLeftColor: identityColor(e.identity) } : undefined}>
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-ink-subdued">
            <span className="font-medium" style={{ color: identityColor(e.identity) }}>{e.participant || e.identity || '?'}</span>
            {e.participant && e.identity && <span className="text-[10px] text-ink-subdued" title="the handle is self-declared; this is the identity that holds the write grant">{e.identity}</span>}
            <span className="border border-border px-1">{k.replace('post.', '')}</span>
            {e.to && e.to !== '*' && <span>to {e.to}</span>}
            {e.reply_to && <span>reply to <span className="mono">{short(e.reply_to)}</span></span>}
            {showWork && work && <span className="text-ink-hint">· {workLabel(work)}</span>}
            {verdict && <span className={`mono ${verdictCls[verdict.verdict] || 'text-ink-hint'}`} title={verdictTitle[verdict.verdict] || ''}>{verdict.verdict}</span>}
            <span className="mono">{dateOf(e.ts_ms)} {when(e.ts_ms)}</span>
            {onReply && <button type="button" title="reply to this post" className="ml-auto text-ink-hint hover:text-ink-2" onClick={(ev) => { ev.stopPropagation(); onReply(e) }}><Reply size={11} className="inline mr-0.5" />reply</button>}
          </div>
          <Markdown text={textOf(e)} theme={theme} />
        </div>
      </div>
    )
  }
  return (
    <div className="flex" ref={refCb}>
      <Rail pos={e.position} />
      <div onClick={() => onSelect(e)} className={`min-w-0 flex-1 cursor-pointer py-1 text-[11px] text-ink-hint ${selected?.event_id === e.event_id ? 'text-accent' : ''} ${highlighted ? 'ring-2 ring-accent-bright' : ''}`}>
        {k} {textOf(e) && <span className="mono">· {textOf(e).slice(0, 90)}</span>} <span className="mono">· {dateOf(e.ts_ms)} {when(e.ts_ms)}</span>
      </div>
    </div>
  )
}

// "Shown to you only": the posts this machine gated to `display` — kept out
// of the agent's context, counted for the person. Collapsed by default (the
// user's ruling); a preview is one line, never the whole post — the post
// itself is the row already in the stream, a click away.
function DisplayDigest({ rows, onJump }) {
  const [open, setOpen] = useState(false)
  if (!rows.length) return null
  return (
    <div className="border-b border-border bg-elevated px-4 py-1.5 text-[11px] text-ink-subdued">
      <button className="flex items-center gap-1 hover:text-ink-2" onClick={() => setOpen((v) => !v)}>
        {open ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
        {rows.length} shown to you only
      </button>
      {open && (
        <div className="mt-1 flex flex-col gap-0.5 pl-4">
          {rows.map((v) => (
            <button key={v.id} onClick={() => onJump(v.id)} className="truncate text-left text-[11px] text-ink-hint hover:text-ink-2">
              <span style={{ color: identityColor(v.author) }}>{v.author}</span> · {v.text} · <span className="mono">{when(v.at_ms)}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

export default function Pane({ conversation, theme, showThinking, me, dense, onSubscribedChange, onClosePane }) {
  const [events, setEvents] = useState([])
  const [head, setHead] = useState(0)
  const [follow, setFollow] = useState(true)
  const [atBottom, setAtBottom] = useState(true)
  const [inspect, setInspect] = useState(false)
  const [sharing, setSharing] = useState(false)
  const [row, setRow] = useState(null)
  const [draft, setDraft] = useState('')
  const [replyTo, setReplyTo] = useState(null)
  const [menuIx, setMenuIx] = useState(0)
  const [caret, setCaret] = useState(0)
  const [error, setError] = useState('')
  const inputRef = useRef(null)
  const [older, setOlder] = useState(false)
  const [verdictByID, setVerdictByID] = useState({})
  const [displayRows, setDisplayRows] = useState([])
  const [workByID, setWorkByID] = useState({})
  const [highlightID, setHighlightID] = useState(null)
  const bottom = useRef(null)
  const scroller = useRef(null)
  const rowRefs = useRef(new Map())
  const nextRef = useRef(0)
  const fromRef = useRef(0) // the first position loaded; older rows load on demand
  const loadingOlder = useRef(false)
  const jump = useRef(false) // land at the bottom instantly after opening
  const anchor = useRef(null) // scroll height before prepending older rows
  const restoredTop = useRef(null) // scrollTop we just set restoring the anchor, so the scroll event it fires isn't mistaken for the reader scrolling back up

  // Opening a conversation loads its tail and lands at the bottom. Nothing
  // older is fetched until the reader scrolls up for it.
  const [opened, setOpened] = useState(0)
  useEffect(() => {
    let stop = false
    setEvents([]); setRow(null); setReplyTo(null); nextRef.current = 0; fromRef.current = 0; setOlder(false)
    api.tail(conversation.id, 300).then((r) => {
      if (stop) return
      fromRef.current = r.from; nextRef.current = r.next
      setOlder(r.from > 0); setHead(r.head)
      jump.current = true
      setEvents(r.events)
      setOpened((n) => n + 1)
    }).catch((e) => { if (!stop) setError(e.message) })
    return () => { stop = true }
  }, [conversation.id])

  // Live: only rows after the last one loaded.
  useEffect(() => {
    if (!follow || !opened) return
    let stop = false
    let busy = false
    const pull = async () => {
      if (busy) return
      busy = true
      try {
        const r = await api.events(conversation.id, nextRef.current, 0, 500)
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
  }, [conversation.id, follow, opened])

  // Verdicts and work state change far less often than the stream itself;
  // poll them on their own slower cadence rather than on every live pull.
  useEffect(() => {
    if (conversation.mode !== 'shared') return
    let stop = false
    let busy = false
    // One round at a time: a conversation this machine has never folded
    // can take seconds to answer, and stacked rounds would use up the
    // browser's connections and stall the stream's own poll.
    const load = async () => {
      if (busy) return
      busy = true
      try {
        const [v, w] = await Promise.all([
          api.conversationVerdicts(conversation.id).catch(() => null),
          api.work(conversation.id).catch(() => null),
        ])
        if (stop) return
        if (v) {
          const rows = v.verdicts || []
          setVerdictByID(Object.fromEntries(rows.map((r) => [r.id, r])))
          setDisplayRows(rows.filter((r) => r.verdict === 'display'))
        }
        if (w) setWorkByID(w.work || {})
      } finally { busy = false }
    }
    load()
    const t = setInterval(load, 5000)
    return () => { stop = true; clearInterval(t) }
  }, [conversation.id, conversation.mode])

  // Scrolls to a post already loaded in the stream and briefly highlights
  // it; a post not loaded (older than what has been fetched) is left alone.
  const jumpTo = useCallback((id) => {
    const el = rowRefs.current.get(id)
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block: 'center' })
    setHighlightID(id)
    setTimeout(() => setHighlightID((h) => (h === id ? null : h)), 1600)
  }, [])

  const loadOlder = useCallback(async () => {
    if (loadingOlder.current || fromRef.current <= 0) return
    loadingOlder.current = true
    try {
      const from = Math.max(0, fromRef.current - 300)
      const r = await api.events(conversation.id, from, fromRef.current, 300)
      anchor.current = scroller.current ? scroller.current.scrollHeight - scroller.current.scrollTop : null
      setEvents((prev) => { const seen = new Set(prev.map((e) => e.position)); return [...r.events.filter((e) => !seen.has(e.position)), ...prev] })
      fromRef.current = from
      setOlder(from > 0)
    } catch (e) { setError(e.message) } finally { loadingOlder.current = false }
  }, [conversation.id])

  // Keep the reader's place when older rows are prepended; land at the
  // bottom right after opening; follow new rows only when already there.
  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    if (anchor.current != null) {
      el.scrollTop = el.scrollHeight - anchor.current
      restoredTop.current = el.scrollTop // read back what the browser actually set (clamped/rounded on HiDPI or zoom)
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
    // Restoring the anchor after a load moves scrollTop back near the top
    // whenever the prepended rows render short (e.g. a collapsed subagent
    // group), which would otherwise fire this same near-top check again and
    // cascade through the whole history one "page" at a time. The restore is
    // always the first scroll event after it runs, so consume the marker on
    // that event regardless of outcome (it won't fire at all if the restore
    // didn't move scrollTop, and a rounded/clamped value on HiDPI or a
    // zoomed page can land within a pixel of, not exactly on, what we set).
    if (restoredTop.current != null) {
      const wasRestore = Math.abs(el.scrollTop - restoredTop.current) <= 1
      restoredTop.current = null
      if (wasRestore) return
    }
    if (el.scrollTop < 80 && older) loadOlder()
  }

  const turns = useMemo(() => groupTurns(events), [events])
  const purpose = useMemo(() => { const p = [...events].reverse().find((e) => e.kind === 'meta.purpose'); return p ? p.content : null }, [events])
  const people = useMemo(() => {
    const s = new Set()
    if (me?.username) s.add(me.username)
    for (const e of events) if (e.identity) s.add(e.identity)
    return [...s]
  }, [events, me])
  const token = tokenAt(draft, caret)
  const kindMenu = token && (token.sigil === '/' || token.sigil === ':') ? filterKinds(token.query) : null
  const atMenu = token && token.sigil === '@' ? filterPeople(people, token.query) : null
  const menu = kindMenu ? { type: 'kind', items: kindMenu } : atMenu ? { type: 'at', items: atMenu } : null

  useEffect(() => { setMenuIx(0) }, [token?.sigil, token?.query])

  const pick = (item) => {
    if (!token) return
    const insert = menu.type === 'kind' ? token.sigil + item.id : '@' + item
    const next = replaceToken(draft, token, insert)
    setDraft(next)
    setCaret(token.start + insert.length + 1)
    inputRef.current?.focus()
  }

  const startReply = (e) => {
    setReplyTo(e)
    setRow(e)
    if (e.kind === 'post.question' && !/^[/:]/.test(draft)) setDraft((d) => '/answer ' + d)
    inputRef.current?.focus()
  }

  const send = async () => {
    const parsed = parseComposer(draft)
    const text = parsed.text.trim()
    if (!text) return
    if (parsed.kind === 'answer' && !replyTo) {
      setError('answer needs a reply — click reply on a post')
      return
    }
    const to = addressOf(parsed, mentionsIn(parsed.text))
    try {
      await api.post(conversation.id, { kind: parsed.kind, text, to, reply_to: replyTo?.event_id || '' })
      setDraft(''); setReplyTo(null); setError('')
    } catch (e) { setError(e.message) }
  }

  const onComposerKey = (e) => {
    if (menu && menu.items.length) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setMenuIx((i) => (i + 1) % menu.items.length); return }
      if (e.key === 'ArrowUp') { e.preventDefault(); setMenuIx((i) => (i - 1 + menu.items.length) % menu.items.length); return }
      if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); pick(menu.items[menuIx] || menu.items[0]); return }
      if (e.key === 'Escape') { e.preventDefault(); setDraft(draft.slice(0, token.start) + draft.slice(token.end)); return }
    }
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
  }
  const toggleFollowShared = async () => {
    try {
      if (conversation.subscribed) await api.unsubscribe(conversation.name); else await api.subscribe(conversation.name, 'full')
      onSubscribedChange(conversation.id, conversation.subscribed ? '' : 'full')
      setError('')
    } catch (e) { setError(e.message) }
  }

  const btn = 'px-2 py-1 text-[11px] border border-border text-ink-2 hover:bg-elevated'
  const btnOn = 'px-2 py-1 text-[11px] border border-accent bg-accent/15 text-ink'

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className={`flex min-w-0 items-center justify-between gap-2 border-b border-border bg-surface ${dense ? 'px-2 py-0.5' : 'px-3 py-1.5'}`}>
        <div className="min-w-0 truncate"><span className="serif text-[14px] text-ink">{purpose?.name || conversation.title || conversation.name}</span><span className="ml-2 text-[11px] text-ink-subdued">{events.length} of {head} rows{(purpose?.purpose || conversation.description) ? ' · ' + (purpose?.purpose || conversation.description) : ''}</span></div>
        <div className="flex shrink-0 items-center gap-1.5">
          <button className={follow ? btnOn : btn} onClick={() => setFollow(!follow)}>{follow ? 'live' : 'paused'}</button>
          {conversation.mode === 'shared' && <button className={btn} onClick={toggleFollowShared}>{conversation.subscribed ? 'unfollow' : 'follow'}</button>}
          {conversation.mode === 'shared' && <button className={sharing ? btnOn : btn} title="who has access" onClick={() => setSharing(!sharing)}><Users size={12} className="inline mr-1" />share</button>}
          <button className={inspect ? btnOn : btn} title="show the selected row" onClick={() => setInspect(!inspect)}><PanelRight size={12} /></button>
          {onClosePane && <button className={btn} title="close this pane (the tab stays open)" onClick={onClosePane}><X size={12} /></button>}
        </div>
      </div>
      {sharing && conversation.mode === 'shared' && <Share conversation={conversation} me={me} />}
      {conversation.mode === 'shared' && <DisplayDigest rows={displayRows} onJump={jumpTo} />}
      <div className="flex min-h-0 flex-1">
        <div ref={scroller} className={`relative min-h-0 flex-1 overflow-auto ${dense ? 'px-2 py-1' : 'px-4 py-3'}`} onScroll={onScroll}>
          {older && <button className="mx-auto mb-3 block border border-border px-2 py-1 text-[11px] text-ink-2 hover:bg-elevated" onClick={loadOlder}>earlier rows</button>}
          {events.length === 0 && <div className="text-[12px] italic text-ink-subdued">no rows yet</div>}
          {turns.map((t, i) => t.kind === 'turn'
            ? <Turn key={t.prompt?.event_id || 'turn' + i} t={t} theme={theme} onSelect={setRow} selected={row} showThinking={showThinking} />
            : <Row key={t.e.event_id || 'row' + i} e={t.e} theme={theme} onSelect={setRow} selected={row}
                verdict={verdictByID[t.e.event_id]} work={workByID[t.e.event_id]} highlighted={highlightID === t.e.event_id}
                onReply={startReply}
                refCb={(node) => { if (node) rowRefs.current.set(t.e.event_id, node); else rowRefs.current.delete(t.e.event_id) }} />)}
          <div ref={bottom} />
          {!atBottom && <button className="sticky bottom-2 left-full mr-2 border border-border bg-elevated px-2 py-1 text-[11px] text-ink-2" onClick={() => { setAtBottom(true); bottom.current?.scrollIntoView({ behavior: 'smooth' }) }}><ArrowDown size={11} className="inline mr-1" />latest</button>}
        </div>
        {inspect && <aside className="w-[300px] shrink-0 border-l border-border bg-surface overflow-auto">{row ? <pre className="mono p-3 text-[10.5px] leading-relaxed text-ink-body whitespace-pre-wrap">{JSON.stringify(row, null, 2)}</pre> : <div className="p-3 text-[11px] italic text-ink-subdued">select a row to see it verbatim</div>}</aside>}
      </div>
      {conversation.mode === 'shared' && (
        <div className={`border-t border-border bg-surface ${dense ? 'p-1' : 'p-2'}`}>
          {replyTo && (
            <div className="mb-1.5 flex items-center gap-2 text-[11px] text-ink-subdued">
              <Reply size={11} />
              reply to <span className="font-medium" style={{ color: identityColor(replyTo.identity) }}>{replyTo.participant || replyTo.identity}</span>
              <span className="border border-border px-1">{(replyTo.kind || '').replace('post.', '')}</span>
              <span className="mono">{short(replyTo.event_id)}</span>
              <button type="button" className="ml-auto text-ink-hint hover:text-ink-2" onClick={() => setReplyTo(null)}><X size={11} /></button>
            </div>
          )}
          <div className="relative flex items-center gap-2">
            {menu && (
              <div className="absolute bottom-full left-0 z-10 mb-1 min-w-[220px] border border-border bg-elevated py-1 text-[12px] shadow-sm">
                <div className="px-2 pb-1 text-[10px] text-ink-hint">{menu.type === 'kind' ? 'message type' : 'address'}</div>
                {menu.items.length === 0 && <div className="px-2 py-1 text-ink-hint">no match</div>}
                {menu.items.map((item, i) => (
                  <button key={menu.type === 'kind' ? item.id : item} type="button"
                    className={`flex w-full items-baseline gap-2 px-2 py-1 text-left ${i === menuIx ? 'bg-accent/15 text-ink' : 'text-ink-2 hover:bg-elevated'}`}
                    onMouseDown={(ev) => { ev.preventDefault(); pick(item) }}>
                    {menu.type === 'kind'
                      ? <span className="mono">{token.sigil}{item.id}</span>
                      : <span className="mono">@{item}</span>}
                  </button>
                ))}
              </div>
            )}
            <span className="mono shrink-0 text-[11px] text-ink-hint" title="message type">{parseComposer(draft).kind}</span>
            <input ref={inputRef}
              className="flex-1 bg-elevated border border-border px-2 py-1.5 text-[12.5px] text-ink outline-none focus:border-accent placeholder:text-ink-hint"
              placeholder="/question  :everyone  @everyone"
              value={draft}
              onChange={(e) => { setDraft(e.target.value); setCaret(e.target.selectionStart || 0) }}
              onKeyUp={(e) => setCaret(e.target.selectionStart || 0)}
              onClick={(e) => setCaret(e.target.selectionStart || 0)}
              onKeyDown={onComposerKey} />
            <button className={btnOn} onClick={send}><Send size={12} className="inline mr-1" />post</button>
          </div>
        </div>
      )}
      {error && <div className="border-t border-border bg-surface px-3 py-1 text-[11px] text-danger">{error}</div>}
    </div>
  )
}
