import { useCallback, useEffect, useState } from 'react'
import { X } from 'lucide-react'
import { api } from './api'

// Who may read or write a shared conversation: the grants statefs holds on
// its namespace, a form to add one by exact username, and revoke per member.
// statefs decides who may change them; a refusal shows as the error line.
export default function Share({ conversation }) {
  const [grants, setGrants] = useState(null)
  const [username, setUsername] = useState('')
  const [access, setAccess] = useState('write')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

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
      setUsername('')
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
        <input className="min-w-0 flex-1 border border-border bg-elevated px-2 py-1 text-[12px] text-ink outline-none placeholder:text-ink-hint focus:border-accent"
          placeholder="exact username (identity) to grant" value={username} onChange={(e) => setUsername(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') add() }} />
        <select value={access} onChange={(e) => setAccess(e.target.value)} className="border border-border bg-elevated px-1 py-1 text-[11px] text-ink-2">
          <option value="write">write (read + post)</option>
          <option value="read">read</option>
        </select>
        <button disabled={busy || !username.trim()} onClick={add} className="border border-accent bg-accent/15 px-2 py-1 text-[11px] text-ink disabled:opacity-50">grant</button>
      </div>
      {error && <div className="mt-1 text-[11px] text-danger">{error}</div>}
    </div>
  )
}
