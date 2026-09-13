// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { archToFlowchart, sanitizeMermaid } from './mermaidSanitize'

describe('sanitizeMermaid', () => {
  it('quotes square labels containing parentheses', () => {
    expect(sanitizeMermaid('flowchart LR\n  A[Engine.Append (pkg/storage)] --> B[ok]'))
      .toBe('flowchart LR\n  A["Engine.Append (pkg/storage)"] --> B[ok]')
  })

  it('leaves already-quoted labels and shaped nodes alone', () => {
    const src = 'flowchart LR\n  A["already (quoted)"] --> B[(SQLite)] --> C([stadium])'
    expect(sanitizeMermaid(src)).toBe(src)
  })

  it('quotes decision labels with parentheses', () => {
    expect(sanitizeMermaid('flowchart TD\n  C{ready (yet)?} --> D[go]'))
      .toBe('flowchart TD\n  C{"ready (yet)?"} --> D[go]')
  })

  it('does not touch plain labels', () => {
    const src = 'flowchart LR\n  Studio --> API --> Agent'
    expect(sanitizeMermaid(src)).toBe(src)
  })
})

// The real-world hallucination this guards against: groups but no services,
// flowchart arrows, `grp:Node` endpoints, parens in labels.
const HALLUCINATED_ARCH = `architecture-beta
    group consumer(service)[Consumer (statefs-node)]
    group statefs(database)[statefs engine (library)]
    group storage(database)[Storage]

    consumer:Client --> statefs:Engine
    statefs:Engine --> statefs:Manifest (SQLite)
    statefs:Engine --> statefs:ParquetWriter
    statefs:Engine --> storage:MemCache (LRU)
    statefs:Engine --> storage:WAL (optional)`

describe('archToFlowchart', () => {
  it('translates hallucinated architecture-beta into a grouped flowchart', () => {
    const flow = archToFlowchart(HALLUCINATED_ARCH)
    expect(flow.startsWith('flowchart LR')).toBe(true)
    expect(flow).toContain('subgraph statefs_grp["statefs engine (library)"]')
    expect(flow).toContain('statefs_Manifest_SQLite["Manifest (SQLite)"]')
    expect(flow).toContain('consumer_Client --> statefs_Engine')
    expect(flow).toContain('statefs_Engine --> storage_WAL_optional')
  })

  it('handles properly declared services and port-suffixed edges', () => {
    const flow = archToFlowchart(`architecture-beta
    group api(cloud)[API]
    service db(database)[Database] in api
    service web(server)[Web] in api
    db:L -- R:web`)
    expect(flow).toContain('db["Database"]')
    expect(flow).toContain('db --- web')
  })

  it('returns null for non-architecture sources', () => {
    expect(archToFlowchart('flowchart LR\n  A --> B')).toBeNull()
  })

  it('output parses as valid mermaid', async () => {
    const mermaid = (await import('mermaid')).default
    mermaid.initialize({ startOnLoad: false })
    expect(await mermaid.parse(HALLUCINATED_ARCH, { suppressErrors: true })).toBe(false)
    const flow = archToFlowchart(HALLUCINATED_ARCH)
    expect(await mermaid.parse(flow, { suppressErrors: true })).not.toBe(false)
  })
})
