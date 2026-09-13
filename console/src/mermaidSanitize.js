// Salvage common LLM mermaid mistakes before falling back to raw source. Pure —
// applied only as a RETRY after a failed parse, so valid diagrams are untouched.
// Ported from quantumwake/specbuilder studio/src/editor/adapters/codemirror/mermaidSanitize.ts.
//
// The #1 failure: unquoted node labels containing parentheses/punctuation, e.g.
//   A[Engine.Append (pkg/storage)] --> B
// which mermaid parses as a nested shape. Quoting the label fixes it:
//   A["Engine.Append (pkg/storage)"]

const OFFENDING = /[()]/

// ---- architecture-beta salvage ------------------------------------------------
// LLMs constantly hallucinate architecture-beta: groups with no `service` lines,
// flowchart arrows, `group:Node` edge endpoints, parens in labels — none of which
// the arch parser accepts, and none of which is locally repairable into VALID
// architecture syntax (the services simply aren't declared). Instead, when an
// architecture-beta block fails to parse, translate its intent into a flowchart:
// groups → subgraphs, declared services and invented `grp:Node` endpoints → nodes.
// A rendered flowchart beats a broken specialty diagram.

const ARCH_DECL = /^\s*(group|service)\s+([A-Za-z0-9_]+)(?:\(([^)]*)\))?\s*\[([^\]]*)\]\s*(?:in\s+([A-Za-z0-9_]+))?\s*$/
const ARCH_EDGE = /^\s*(.+?)\s*(<--?>?|--?>|--)\s*(.+?)\s*$/
const PORT = /^[LRTB]$/

const slug = (s) => s.replace(/[^A-Za-z0-9_]+/g, '_').replace(/^_+|_+$/g, '') || 'n'

export function archToFlowchart(code) {
  const lines = code.split('\n')
  if (!/^\s*architecture-beta\s*$/.test(lines[0] ?? '')) return null

  const groups = new Map() // id -> { label, nodes }
  const nodes = new Map() // id -> declaration line
  const edges = []

  const addNode = (id, label, group) => {
    if (nodes.has(id)) return
    nodes.set(id, `${id}["${label.replace(/"/g, "'")}"]`)
    if (group && groups.has(group)) groups.get(group).nodes.push(id)
  }

  // An edge endpoint: `svc`, `svc:R` (port — strip), or the hallucinated
  // `grp:Node Label (extra)` → a synthesized node inside that group.
  const endpoint = (raw) => {
    const m = /^([A-Za-z0-9_]+)(?::(.+))?$/.exec(raw.trim())
    if (!m) return null
    let [, base, rest] = m
    // Valid arch syntax puts the port BEFORE the id on the right side of an
    // edge (`db:L -- R:web`) — `R:web` means port R of service web.
    if (PORT.test(base) && rest && /^[A-Za-z0-9_]+$/.test(rest.trim())) {
      base = rest.trim()
      rest = undefined
    }
    if (!rest || PORT.test(rest.trim())) {
      if (groups.has(base) && !nodes.has(base)) addNode(base, groups.get(base).label)
      return base
    }
    const label = rest.trim()
    const id = `${base}_${slug(label)}`
    addNode(id, label, base)
    return id
  }

  for (const line of lines.slice(1)) {
    if (!line.trim() || /^\s*(%%|junction\b)/.test(line)) continue
    const decl = ARCH_DECL.exec(line)
    if (decl) {
      const [, kind, id, , label, parent] = decl
      if (kind === 'group') groups.set(id, { label: label || id, nodes: [] })
      else addNode(id, label || id, parent)
      continue
    }
    const edge = ARCH_EDGE.exec(line)
    if (edge) {
      const [, lhs, arrow, rhs] = edge
      const a = endpoint(lhs)
      const b = endpoint(rhs)
      if (a && b) edges.push(`${a} ${arrow === '--' ? '---' : arrow} ${b}`)
    }
  }
  if (!edges.length && !nodes.size) return null

  const grouped = new Set([...groups.values()].flatMap((g) => g.nodes))
  const out = ['flowchart LR']
  for (const [gid, g] of groups) {
    if (!g.nodes.length) continue
    out.push(`    subgraph ${gid}_grp["${g.label.replace(/"/g, "'")}"]`)
    for (const n of g.nodes) out.push(`        ${nodes.get(n)}`)
    out.push('    end')
  }
  for (const [id, decl] of nodes) if (!grouped.has(id)) out.push(`    ${decl}`)
  for (const e of edges) out.push(`    ${e}`)
  return out.join('\n')
}

export function sanitizeMermaid(code) {
  // Quote plain [...] labels that contain parens and aren't already quoted.
  // Leave shaped nodes alone: [( )] cylinders, ([ ]) stadiums, [[ ]] subroutines.
  let out = code.replace(
    /(^|[^\w"\]])([A-Za-z0-9_]+)\[(?!\[|\()([^[\]\n"]*)\]/gm,
    (whole, pre, id, label) => (OFFENDING.test(label) ? `${pre}${id}["${label}"]` : whole),
  )
  // Curly (decision) nodes with parens: C{is it (really)?} → C{"is it (really)?"}
  out = out.replace(
    /(^|[^\w"])([A-Za-z0-9_]+)\{([^{}\n"]*)\}/gm,
    (whole, pre, id, label) => (OFFENDING.test(label) ? `${pre}${id}{"${label}"}` : whole),
  )
  return out
}
