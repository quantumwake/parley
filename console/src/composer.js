// Composer syntax for the shared-conversation box.
//   /question …   or  :status …
//   @alice        addresses; first @ becomes `to` (or @* for everyone)
// Leading / or : is stripped before post. Work kinds stay on `parley post`.

export const KINDS = [
  { id: 'comment', hint: 'talk' },
  { id: 'question', hint: 'ask — claimable' },
  { id: 'answer', hint: 'reply to a question' },
  { id: 'report', hint: 'findings' },
  { id: 'status', hint: 'an update' },
  { id: 'artifact', hint: 'a thing' },
]

export const KIND_IDS = KINDS.map((k) => k.id)

export function parseComposer(text) {
  const raw = text ?? ''
  const m = raw.match(/^([/:])(\w+)(?:\s+|$)([\s\S]*)$/)
  if (m && KIND_IDS.includes(m[2])) {
    return { kind: m[2], text: m[3], sigil: m[1] }
  }
  return { kind: 'comment', text: raw, sigil: '' }
}

export function mentionsIn(text) {
  const out = []
  const re = /(^|\s)@([^\s]+)/g
  let m
  while ((m = re.exec(text || ''))) out.push(m[2])
  return out
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
  return KINDS.filter((k) => !q || k.id.startsWith(q))
}

export function filterPeople(people, query) {
  const q = (query || '').toLowerCase()
  return people.filter((p) => p.toLowerCase().includes(q))
}

export function replaceToken(text, token, insert) {
  const after = (text || '').slice(token.end)
  return (text || '').slice(0, token.start) + insert + (after.startsWith(' ') ? after : ' ' + after.replace(/^\S*/, ''))
}
