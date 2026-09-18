// The local API served by `parley console` (the S13/S14 contract, local first).

// Each launch of `parley console` opens this page with a token in the URL
// fragment (never sent to a server or logged). Keep it for this tab only and
// clear it from the address bar, so it is not bookmarked, shared or kept in
// history; a reload finds it in sessionStorage.
const TOKEN_KEY = 'parley.console.token'
const token = (() => {
  const m = window.location.hash.match(/(?:^#|&)token=([0-9a-f]+)/)
  if (m) {
    try { sessionStorage.setItem(TOKEN_KEY, m[1]) } catch {}
    window.history.replaceState(null, '', window.location.pathname + window.location.search)
    return m[1]
  }
  try { return sessionStorage.getItem(TOKEN_KEY) || '' } catch { return '' }
})()

const j = async (path, init) => {
  const res = await fetch(path, { ...init, headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, ...(init?.headers || {}) } })
  if (res.status === 401) throw new Error('This console link has expired: open the console again with `parley console`.')
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`)
  return body
}

export const api = {
  me: () => j('/v1/me'),
  identities: () => j('/v1/identities'),
  // Act as another identity on this machine: this console only, not the machine's default.
  useIdentity: (name) => j('/v1/identity', { method: 'POST', body: JSON.stringify({ name }) }),
  conversations: (params = {}) => j('/v1/conversations?' + new URLSearchParams(params)),
  createConversation: (name, description = '', tags = []) => j('/v1/conversations', { method: 'POST', body: JSON.stringify({ name, description, tags }) }),
  renameConversation: (id, fields) => j(`/v1/conversations/${id}`, { method: 'PATCH', body: JSON.stringify(fields) }),
  deleteConversation: (id) => j(`/v1/conversations/${id}`, { method: 'DELETE' }),
  events: (id, from = 0, to = 0, limit = 500) => j(`/v1/conversations/${id}/events?from=${from}&to=${to}&limit=${limit}`),
  // The last n rows: how a conversation opens, at its end.
  tail: (id, n = 300) => j(`/v1/conversations/${id}/events?tail=${n}`),
  head: (id) => j(`/v1/conversations/${id}/head`),
  post: (id, body) => j(`/v1/conversations/${id}/posts`, { method: 'POST', body: JSON.stringify(body) }),
  subscriptions: () => j('/v1/subscriptions'),
  subscribe: (name, mode = 'full') => j('/v1/subscriptions', { method: 'POST', body: JSON.stringify({ name, mode }) }),
  unsubscribe: (name) => j(`/v1/subscriptions/${encodeURIComponent(name)}`, { method: 'DELETE' }),
}
