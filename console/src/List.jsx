import { useMemo } from 'react'
import { MessageSquare, Radio } from 'lucide-react'

// The conversation index. Shared conversations first with what is unread;
// recorded sessions grouped by day of last activity, most recently active
// first, titled by their first prompt, with the agent and the time. One
// search box over everything.

const short = (s) => (s ? String(s).slice(0, 8) : '')
// Last activity when this machine recorded the session, else its start.
const lastOf = (c) => c.active_ms || c.started_ms
const timeOf = (ms) => (ms ? new Date(Number(ms)).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '')
const dayOf = (ms) => {
  if (!ms) return 'undated'
  const d = new Date(Number(ms)); const today = new Date(); const y = new Date(); y.setDate(today.getDate() - 1)
  if (d.toDateString() === today.toDateString()) return 'today'
  if (d.toDateString() === y.toDateString()) return 'yesterday'
  return d.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' })
}

// A stable color per identity so speakers are told apart at a glance.
// Colour keys on the identity, never the handle: handles are self-declared
// and two participants may choose the same one.
export function identityColor(name) {
  let h = 0
  for (const ch of String(name || '')) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return `hsl(${h % 360} 45% 62%)`
}

function nameOf(c) {
  // <agent>/<timestamp>/<dir>#<tag> or <agent>/<dir>#<tag>: show the dir
  const parts = (c.name || '').split('/')
  const last = parts[parts.length - 1] || c.name
  return last.replace(/#.*$/, '')
}

export default function List({ items, subs, selected, onSelect, filter, setFilter, live }) {
  const q = filter.trim().toLowerCase()
  const match = (c) => !q || [c.title, c.name, c.description, c.agent, ...(Array.isArray(c.tags) ? c.tags : [])].join(' ').toLowerCase().includes(q)
  const unread = useMemo(() => Object.fromEntries((subs || []).map((s) => [s.id, s])), [subs])
  const shared = items.filter((c) => c.mode === 'shared' && match(c))
  const sessions = items.filter((c) => c.mode !== 'shared' && match(c)).sort((a, b) => (Number(lastOf(b)) || 0) - (Number(lastOf(a)) || 0))
  const days = useMemo(() => {
    const m = new Map()
    for (const c of sessions) { const k = dayOf(lastOf(c)); if (!m.has(k)) m.set(k, []); m.get(k).push(c) }
    return [...m.entries()]
  }, [sessions])

  const sharedRow = (c) => {
    const u = unread[c.id]
    return (
      <button key={c.id} onClick={() => onSelect(c)} className={`block w-full px-3 py-2 text-left border-b border-border hover:bg-elevated ${selected?.id === c.id ? 'bg-elevated' : ''}`}>
        <div className="flex items-center gap-2 text-[12.5px]">
          <Radio size={12} style={{ color: identityColor(c.name) }} />
          <span className="truncate serif text-ink-2">{c.name}</span>
          {u && u.unread > 0 && <span className="ml-auto shrink-0 border border-accent/60 px-1 text-[10px] text-accent-bright">{u.unread} new</span>}
          {u && u.unread === 0 && <span className="ml-auto shrink-0 text-[10px] text-ink-hint">following</span>}
        </div>
        <div className="truncate text-[11px] text-ink-subdued">{c.description || 'no description'}{c.access !== 'owner' ? ` · ${c.access}` : ''}</div>
      </button>
    )
  }

  const sessionRow = (c) => (
    <button key={c.id} onClick={() => onSelect(c)} className={`block w-full px-3 py-1.5 text-left border-b border-border hover:bg-elevated ${selected?.id === c.id ? 'bg-elevated' : ''}`}>
      <div className="flex items-center gap-2 text-[12.5px]">
        <MessageSquare size={12} className="shrink-0 text-ink-subdued" />
        <span className="truncate text-ink-2">{c.title || nameOf(c)}</span>
        {live[c.session] && <span className="ml-auto shrink-0 text-[10px] text-success">recording</span>}
      </div>
      <div className="flex gap-2 text-[11px] text-ink-subdued"><span className="mono">{timeOf(lastOf(c))}</span><span style={{ color: identityColor(c.agent) }}>{c.agent}</span><span className="truncate">{nameOf(c)}</span></div>
    </button>
  )

  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-border p-2">
        <input className="w-full border border-border bg-elevated px-2 py-1 text-[12px] text-ink-2 outline-none placeholder:text-ink-hint focus:border-accent" placeholder="search titles, agents, tags" value={filter} onChange={(e) => setFilter(e.target.value)} />
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="flex items-baseline justify-between px-3 pt-3 pb-1"><span className="text-[10px] uppercase tracking-wider text-ink-subdued">shared</span><span className="text-[10px] text-ink-hint">{shared.length}</span></div>
        {shared.length ? shared.map(sharedRow) : <div className="px-3 py-2 text-[11px] italic text-ink-hint">none · parley create &lt;name&gt;</div>}
        <div className="flex items-baseline justify-between px-3 pt-4 pb-1"><span className="text-[10px] uppercase tracking-wider text-ink-subdued">recorded sessions</span><span className="text-[10px] text-ink-hint">{sessions.length}</span></div>
        {days.map(([day, rows]) => (
          <div key={day}>
            <div className="sticky top-0 bg-surface px-3 py-1 text-[10px] text-ink-muted border-b border-border">{day}</div>
            {rows.map(sessionRow)}
          </div>
        ))}
        {!sessions.length && <div className="px-3 py-2 text-[11px] italic text-ink-hint">nothing recorded yet</div>}
      </div>
    </div>
  )
}
