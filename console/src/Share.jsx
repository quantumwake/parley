import { useCallback, useEffect, useRef, useState } from 'react'
import { X } from 'lucide-react'
import { TerminalAutocomplete } from '@quantumwake/terminal-ux-components'
import { api } from './api'

// Who may read or write a shared conversation: the grants statefs holds on
// its namespace, a form to add one (people found in statefs.ai, or an exact
// username), and revoke per member. statefs decides who may change them; a
// refusal shows as the error line.
export default function Share({ conversation }) {
  const [grants, setGrants] = useState(null)
  const [username, setUsername] = useState('')
  const [access, setAccess] = useState('write')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  // People from statefs.ai, one row per agent (an agent's identity is what a
  // grant names). Typing always counts as an exact username too; a search
  // statefs.ai refuses leaves a note and the typed name still grants.
  const [matches, setMatches] = useState([])
  const [picked, setPicked] = useState('')
  const [searching, setSearching] = useState(false)
  const [note, setNote] = useState('')
  const [formKey, setFormKey] = useState(0)
  const seq = useRef(0)
  const timer = useRef(null)
  useEffect(() => () => clearTimeout(timer.current), [])

  const search = (term) => {
    setUsername(term.trim()); setPicked('')
    clearTimeout(timer.current)
    if (term.trim().length < 2) { setMatches([]); setNote(''); return }
    const n = ++seq.current
    timer.current = setTimeout(async () => {
      setSearching(true)
      try {
        const r = await api.people(term.trim())
        if (n !== seq.current) return
        setMatches((r.people || []).flatMap((p) => (p.agents || []).map((a) => ({ name: p.name, label: a.label, identity: a.identity }))))
        setNote(r.note || '')
      } catch (e) { if (n === seq.current) { setMatches([]); setNote(e.message) } }
      if (n === seq.current) setSearching(false)
    }, 250)
  }

  const load = useCallback(async () => {
    try {
      const r = await api.grants(conversation.id)
      setGrants(r.grants || []); setError('')
    } catch (e) { setGrants([]); setError(e.message) }
  }, [conversation.id])
  useEffect(() => { load() }, [load])

  const add = async () => {
    const u = username.trim()
    if (!u || busy) return
    setBusy(true)
    try {
      await api.grant(conversation.id, u, access)
      setUsername(''); setPicked(''); setMatches([]); setFormKey((k) => k + 1)
      await load()
    } catch (e) { setError(e.message) }
    setBusy(false)
  }

  const revoke = async (u) => {
    setBusy(true)
    try {
      await api.revoke(conversation.id, u)
      await load()
    } catch (e) { setError(e.message) }
    setBusy(false)
  }

  return (
    <div className="border-b border-border bg-surface px-3 py-2 text-[12px]">
      <div className="mb-1.5 text-[10px] uppercase tracking-wider text-ink-subdued">access</div>
      {grants === null && <div className="italic text-ink-hint">loading…</div>}
      {grants && grants.length === 0 && !error && <div className="italic text-ink-hint">no one else has been granted access</div>}
      {grants && grants.map((g) => (
        <div key={g.username} className="group flex items-center gap-2 py-0.5">
          <span className="mono min-w-0 truncate text-ink-2">{g.username}</span>
          <span className="shrink-0 text-[11px] text-ink-subdued">{g.access}</span>
          <button title={`revoke ${g.username}`} disabled={busy} onClick={() => revoke(g.username)}
            className="ml-auto shrink-0 p-0.5 text-ink-hint opacity-0 hover:text-danger group-hover:opacity-100"><X size={12} /></button>
        </div>
      ))}
      <div className="mt-2 flex items-center gap-1.5">
        <TerminalAutocomplete key={formKey} className="min-w-0 flex-1" placeholder="name or exact username to grant"
          items={matches} valueField="identity" value={picked} formatDisplay={(m) => `${m.identity} · ${m.name} (${m.label})`}
          filterFn={(items) => items} openOnFocus={false} loading={searching} emptyText={note || 'no one in statefs.ai matches; type an exact username'}
          onSearchChange={search} onSelect={(m) => { setPicked(m.identity); setUsername(m.identity) }} />
        <select value={access} onChange={(e) => setAccess(e.target.value)} className="border border-border bg-elevated px-1 py-1 text-[11px] text-ink-2">
          <option value="write">write (read + post)</option>
          <option value="read">read</option>
        </select>
        <button disabled={busy || !username.trim()} onClick={add} className="border border-accent bg-accent/15 px-2 py-1 text-[11px] text-ink disabled:opacity-50">grant</button>
      </div>
      {note && !matches.length && <div className="mt-1 text-[11px] text-ink-subdued">people search: {note}; an exact username still works</div>}
      {error && <div className="mt-1 text-[11px] text-danger">{error}</div>}
    </div>
  )
}
