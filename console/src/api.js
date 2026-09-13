// The local API served by `parley console` (the S13/S14 contract, local first).
const j = async (path, init) => {
  const res = await fetch(path, { ...init, headers: { 'Content-Type': 'application/json', ...(init?.headers || {}) } })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`)
  return body
}

export const api = {
  me: () => j('/v1/me'),
  conversations: (params = {}) => j('/v1/conversations?' + new URLSearchParams(params)),
  events: (id, from = 0, to = 0, limit = 500) => j(`/v1/conversations/${id}/events?from=${from}&to=${to}&limit=${limit}`),
  // The last n rows: how a conversation opens, at its end.
  tail: (id, n = 300) => j(`/v1/conversations/${id}/events?tail=${n}`),
  head: (id) => j(`/v1/conversations/${id}/head`),
  post: (id, body) => j(`/v1/conversations/${id}/posts`, { method: 'POST', body: JSON.stringify(body) }),
  subscriptions: () => j('/v1/subscriptions'),
  subscribe: (name, mode = 'full') => j('/v1/subscriptions', { method: 'POST', body: JSON.stringify({ name, mode }) }),
  unsubscribe: (name) => j(`/v1/subscriptions/${encodeURIComponent(name)}`, { method: 'DELETE' }),
}
