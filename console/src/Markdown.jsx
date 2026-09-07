import { useEffect, useMemo, useRef } from 'react'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

// Markdown as a real chat client renders it: headings, lists, tables, code,
// links, and mermaid diagrams (rendered lazily, theme-aware, falling back to
// the source when the diagram does not parse). Sanitized before insertion.
marked.setOptions({ gfm: true, breaks: true })

const renderer = new marked.Renderer()
const baseCode = renderer.code.bind(renderer)
renderer.code = function (token) {
  const lang = (token.lang || '').trim()
  if (lang === 'mermaid') return `<pre class="mermaid" data-src="${encodeURIComponent(token.text)}">${escapeHtml(token.text)}</pre>`
  return baseCode(token)
}

function escapeHtml(s) { return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])) }

let mermaidPromise = null
const loadMermaid = () => (mermaidPromise ||= import('mermaid').then((m) => m.default))
const isPaper = () => document.documentElement.classList.contains('theme-paper')
let seq = 0

async function renderDiagrams(root) {
  const blocks = root.querySelectorAll('pre.mermaid[data-src]')
  if (!blocks.length) return
  const mermaid = await loadMermaid()
  mermaid.initialize({ startOnLoad: false, theme: isPaper() ? 'default' : 'dark', securityLevel: 'strict' })
  for (const pre of blocks) {
    const src = decodeURIComponent(pre.dataset.src)
    try {
      if (!(await mermaid.parse(src, { suppressErrors: true }))) continue
      const { svg } = await mermaid.render(`parley-mmd-${++seq}`, src)
      const div = document.createElement('div')
      div.className = 'mermaid'
      div.innerHTML = svg
      pre.replaceWith(div)
    } catch { /* keep the source block */ }
  }
}

export default function Markdown({ text, theme }) {
  const ref = useRef(null)
  const html = useMemo(() => DOMPurify.sanitize(marked.parse(text || '', { renderer })), [text])
  useEffect(() => { if (ref.current) renderDiagrams(ref.current) }, [html, theme])
  return <div ref={ref} className="prose" dangerouslySetInnerHTML={{ __html: html }} />
}
