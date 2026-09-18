import { useMemo, useState } from 'react'
import { MessageSquare, Radio, Plus, Pencil, Trash2, X, Check } from 'lucide-react'

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

export default function List({ items, subs, openIds, onSelect, filter, setFilter, live, onCreate, onRename, onDelete }) {
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

  // Create, rename and delete each own their own inline state: a new-
  // channel form above the list, an editable title in place of a row's
  // name, and a click-again-to-confirm delete (never a native confirm(),
  // which blocks the whole page).
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [createErr, setCreateErr] = useState('')
  const [renaming, setRenaming] = useState(null) // conversation id
  const [renameVal, setRenameVal] = useState('')
  const [renameErr, setRenameErr] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(null) // conversation id, armed
  const [deleteErr, setDeleteErr] = useState('')

  const submitCreate = async () => {
    const name = newName.trim()
    if (!name) return
    try {
      await onCreate(name, newDesc.trim())
      setCreating(false); setNewName(''); setNewDesc(''); setCreateErr('')
    } catch (e) { setCreateErr(e.message) }
  }

  const startRename = (c) => { setRenaming(c.id); setRenameVal(c.title || nameOf(c)); setRenameErr('') }
  const submitRename = async (id) => {
    const title = renameVal.trim()
    if (!title) { setRenameErr('needs a title'); return }
    try {
      await onRename(id, title)
      setRenaming(null); setRenameErr('')
    } catch (e) { setRenameErr(e.message) }
  }

  const clickDelete = async (id) => {
    if (confirmDelete !== id) { setConfirmDelete(id); setDeleteErr(''); return }
    try {
      await onDelete(id)
      setConfirmDelete(null); setDeleteErr('')
    } catch (e) { setDeleteErr(e.message) }
  }

  const sharedRow = (c) => {
    const u = unread[c.id]
    if (renaming === c.id) {
      return (
        <div key={c.id} className="flex items-center gap-1 border-b border-border bg-elevated px-3 py-2">
          <input autoFocus className="min-w-0 flex-1 border border-accent bg-raised px-1.5 py-1 text-[12.5px] text-ink outline-none" value={renameVal}
            onChange={(e) => setRenameVal(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') submitRename(c.id); if (e.key === 'Escape') setRenaming(null) }} />
          <button title="save" onClick={() => submitRename(c.id)} className="shrink-0 p-1 text-success hover:bg-raised"><Check size={13} /></button>
          <button title="cancel" onClick={() => setRenaming(null)} className="shrink-0 p-1 text-ink-hint hover:bg-raised"><X size={13} /></button>
          {renameErr && <div className="basis-full text-[10px] text-danger">{renameErr}</div>}
        </div>
      )
    }

    return (
      <div key={c.id} className={`group flex items-start gap-1 border-b border-border px-3 py-2 hover:bg-elevated ${openIds.has(c.id) ? 'bg-elevated' : ''}`}>
        <button onClick={() => onSelect(c)} className="min-w-0 flex-1 text-left">
          <div className="flex items-center gap-2 text-[12.5px]">
            <Radio size={12} style={{ color: identityColor(c.name) }} />
            <span className="truncate serif text-ink-2">{c.title || c.name}</span>
            {u && u.unread > 0 && <span className="ml-auto shrink-0 border border-accent/60 px-1 text-[10px] text-accent-bright">{u.unread} new</span>}
            {u && u.unread === 0 && <span className="ml-auto shrink-0 text-[10px] text-ink-hint">following</span>}
          </div>
          <div className="truncate text-[11px] text-ink-subdued">{c.description || 'no description'}{c.access !== 'owner' ? ` · ${c.access}` : ''}</div>
        </button>
        <div className="hidden shrink-0 items-center gap-0.5 group-hover:flex">
          <button title="rename" onClick={() => startRename(c)} className="p-1 text-ink-hint hover:text-ink-2 hover:bg-raised"><Pencil size={12} /></button>
          <button title={confirmDelete === c.id ? 'click again to delete' : 'delete'} onClick={() => clickDelete(c.id)}
            className={`p-1 hover:bg-raised ${confirmDelete === c.id ? 'text-danger' : 'text-ink-hint hover:text-danger'}`}><Trash2 size={12} /></button>
        </div>
        {confirmDelete === c.id && <div className="basis-full text-[10px] text-danger">click the trash icon again to delete — this removes it everywhere, for everyone</div>}
        {deleteErr && confirmDelete === c.id && <div className="basis-full text-[10px] text-danger">{deleteErr}</div>}
      </div>
    )
  }

  const sessionRow = (c) => (
    <button key={c.id} onClick={() => onSelect(c)} className={`block w-full px-3 py-1.5 text-left border-b border-border hover:bg-elevated ${openIds.has(c.id) ? 'bg-elevated' : ''}`}>
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
        <div className="flex items-center justify-between px-3 pt-3 pb-1">
          <span className="text-[10px] uppercase tracking-wider text-ink-subdued">shared</span>
          <div className="flex items-center gap-2">
            <span className="text-[10px] text-ink-hint">{shared.length}</span>
            <button title="new channel" onClick={() => setCreating((v) => !v)} className={`p-0.5 hover:bg-elevated ${creating ? 'text-accent' : 'text-ink-hint hover:text-ink-2'}`}><Plus size={13} /></button>
          </div>
        </div>
        {creating && (
          <div className="border-b border-border bg-elevated px-3 py-2">
            <input autoFocus className="mb-1 w-full border border-accent bg-raised px-1.5 py-1 text-[12px] text-ink outline-none" placeholder="channel name"
              value={newName} onChange={(e) => setNewName(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') submitCreate(); if (e.key === 'Escape') setCreating(false) }} />
            <input className="mb-1 w-full border border-border bg-raised px-1.5 py-1 text-[12px] text-ink outline-none placeholder:text-ink-hint" placeholder="description (optional)"
              value={newDesc} onChange={(e) => setNewDesc(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') submitCreate(); if (e.key === 'Escape') setCreating(false) }} />
            <div className="flex items-center gap-2">
              <button onClick={submitCreate} className="border border-border px-2 py-1 text-[11px] text-ink-2 hover:bg-raised">create</button>
              <button onClick={() => { setCreating(false); setCreateErr('') }} className="px-2 py-1 text-[11px] text-ink-hint hover:text-ink-2">cancel</button>
              {createErr && <span className="text-[10px] text-danger">{createErr}</span>}
            </div>
          </div>
        )}
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
