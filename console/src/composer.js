// Composer syntax for the shared-conversation box.
//   /question …   or  :status …   — message type (post.comment, post.question, …)
//   @everyone / :everyone / /everyone  — every subscriber evaluates it
//   @alice        addresses; first @ becomes `to`
// Leading / or : is stripped before post. Work posts stay on `parley post`.

export const MESSAGE_TYPES = [
  { id: 'comment' },
  { id: 'question' },
  { id: 'answer' },
  { id: 'report' },
  { id: 'status' },
  { id: 'artifact' },
]

export const KINDS = MESSAGE_TYPES
export const KIND_IDS = MESSAGE_TYPES.map((k) => k.id)

export function parseComposer(text) {
  const raw = text ?? ''
  let rest = raw
  let to = ''
  const everyone = rest.match(/^([/:])everyone(?:\s+|$)([\s\S]*)$/i)
  if (everyone) {
    to = 'everyone'
    rest = everyone[2]
  }
  const m = rest.match(/^([/:])(\w+)(?:\s+|$)([\s\S]*)$/)
  if (m && KIND_IDS.includes(m[2])) {
    return { kind: m[2], text: m[3], sigil: m[1], to }
  }
  return { kind: 'comment', text: rest, sigil: '', to }
}

export function mentionsIn(text) {
  const out = []
  const re = /(^|\s)@([^\s]+)/g
  let m
  while ((m = re.exec(text || ''))) {
    const name = m[2].replace(/[.,;:!?]+$/g, '')
    if (!name) continue
    if (name === '*' || name.toLowerCase() === 'everyone') out.push('everyone')
    else out.push(name)
  }
  return out
}

export function addressOf(parsed, mentions) {
  if (parsed?.to) return parsed.to
  const m = mentions?.[0]
  if (!m) return ''
  if (m === '*' || m.toLowerCase() === 'everyone') return 'everyone'
  return m
}

// The @ / : token being typed at the caret, if any.
export function tokenAt(text, caret) {
  const left = (text || '').slice(0, caret ?? 0)
  const m = left.match(/(^|[\s])([/:@])([^\s]*)$/)
  if (!m) return null
  const sigil = m[2]
  const query = m[3]
  const start = left.length - query.length - 1
  if ((sigil === '/' || sigil === ':') && start !== 0) return null
  return { sigil, query, start, end: caret ?? left.length }
}

export function filterKinds(query) {
  const q = (query || '').toLowerCase()
  if ('everyone'.startsWith(q) && q) {
    return [{ id: 'everyone' }, ...MESSAGE_TYPES.filter((k) => k.id.startsWith(q))]
  }
  return MESSAGE_TYPES.filter((k) => !q || k.id.startsWith(q))
}

export function filterPeople(people, query) {
  const q = (query || '').toLowerCase()
  const roster = ['everyone', ...people.filter((p) => p !== 'everyone' && p !== '*')]
  return roster.filter((p) => p.toLowerCase().includes(q))
}

export function replaceToken(text, token, insert) {
  const after = (text || '').slice(token.end)
  return (text || '').slice(0, token.start) + insert + (after.startsWith(' ') ? after : ' ' + after.replace(/^\S*/, ''))
}
