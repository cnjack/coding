/**
 * Markdown rendering — marked + highlight.js + DOMPurify.
 *
 * Ported from the Vue composable and extended with:
 *   - a code-block "chrome" renderer (filename bar + copy button), and
 *   - a zero-cost plugin hook table (mermaid / katex register into it; the core
 *     never imports the plugin files, so not importing them costs nothing).
 *
 * Framework-agnostic; returns an HTML string the consumer injects via
 * dangerouslySetInnerHTML. Sanitization runs only when a DOM is present
 * (browser); in SSR/Node it is a no-op so the pipeline stays testable — the
 * browser, which is the only place the HTML is ever injected, always sanitizes.
 */

import { Marked } from 'marked'
import type { TokenizerAndRendererExtension, Tokens } from 'marked'
import hljs from 'highlight.js'
import DOMPurify from 'dompurify'
import { copyText } from './clipboard.js'

// ─── Plugin hook table ───────────────────────────────────────────────────
// Optional plugins register renderers here. `markdown.ts` never imports the
// plugin files (mermaid.ts / katex.ts), only the reverse — so a consumer that
// never calls registerMermaid()/registerKatex() ships zero plugin code.

/** Arguments passed to a fenced-code-block hook. */
export interface CodeBlockHookArgs {
  /** Raw (un-highlighted) code text. */
  code: string
  /** First token of the info string, e.g. `ts` for ```` ```ts title=a.ts ````. */
  lang: string
  /** Parsed filename (from `title=` or `lang:file` conventions), or ''. */
  filename: string
}

/** Return HTML to fully replace the code block, or `null` to fall through. */
export type CodeBlockHook = (args: CodeBlockHookArgs) => string | null

/** Render TeX to an HTML string. `displayMode` = block (`$$…$$`) vs inline (`$…$`). */
export type MathRenderer = (tex: string, displayMode: boolean) => string

const codeBlockHooks: CodeBlockHook[] = []
let mathRenderer: MathRenderer | null = null
let mathExtInstalled = false

/**
 * Register a fenced-code-block renderer. Hooks run in registration order; the
 * first to return non-null wins (used by the mermaid plugin for ```` ```mermaid ````).
 */
export function registerCodeBlockRenderer(hook: CodeBlockHook): void {
  codeBlockHooks.push(hook)
}

/**
 * Register a math renderer and (once) install the `$…$` / `$$…$$` tokenizers.
 * Until this is called, math delimiters are left as literal text — so a doc
 * that never registers katex renders `$x^2$` verbatim (and pays no math cost).
 */
export function registerMathRenderer(render: MathRenderer): void {
  mathRenderer = render
  if (!mathExtInstalled) {
    marked.use({ extensions: [blockMathExtension, inlineMathExtension] })
    mathExtInstalled = true
  }
}

// ─── Escaping helpers ──────────────────────────────────────────────────────

const HTML_ESCAPES: Record<string, string> = {
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&#39;',
}

/** Escape text for use as HTML text content or a double-quoted attribute. */
function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => HTML_ESCAPES[c])
}

// ─── Info-string parsing ───────────────────────────────────────────────────

/**
 * Parse a fenced-code info string into `{ lang, filename }`.
 * Supports two filename conventions:
 *   ```` ```ts title=a.ts ````  (also title="a b.ts")
 *   ```` ```ts:a.ts ````
 */
export function parseCodeInfo(info: string): { lang: string; filename: string } {
  const trimmed = (info || '').trim()
  if (!trimmed) return { lang: '', filename: '' }

  const head = trimmed.split(/\s+/)[0]
  let lang = head
  let filename = ''

  // `lang:file` convention.
  const colon = head.indexOf(':')
  if (colon >= 0) {
    lang = head.slice(0, colon)
    filename = head.slice(colon + 1)
  }

  // `title=…` convention (wins if present). Supports quoted values.
  const rest = trimmed.slice(head.length)
  const m = rest.match(/\btitle=(?:"([^"]*)"|'([^']*)'|(\S+))/)
  if (m) filename = m[1] ?? m[2] ?? m[3] ?? filename

  return { lang, filename }
}

// ─── Syntax highlighting ────────────────────────────────────────────────────

function highlight(code: string, lang: string): string {
  if (lang && hljs.getLanguage(lang)) {
    try {
      return hljs.highlight(code, { language: lang }).value
    } catch {
      /* fall through to auto */
    }
  }
  try {
    return hljs.highlightAuto(code).value
  } catch {
    return escapeHtml(code)
  }
}

// ─── Code-block chrome renderer ─────────────────────────────────────────────

function renderCodeBlock(token: Tokens.Code): string {
  const { lang, filename } = parseCodeInfo(token.lang || '')

  // Plugin hooks first (e.g. mermaid intercepts lang === 'mermaid').
  for (const hook of codeBlockHooks) {
    const out = hook({ code: token.text, lang, filename })
    if (out != null) return out
  }

  const highlighted = highlight(token.text, lang)
  const langClass = lang ? ` language-${escapeHtml(lang)}` : ''
  // encodeURIComponent output is safe inside a double-quoted attribute
  // (it percent-encodes " < > & and leaves ' — which we never quote with).
  const dataCode = encodeURIComponent(token.text)

  const bar =
    `<div class="jcode-codeblock__bar">` +
    `<span class="jcode-codeblock__lang">${escapeHtml(lang)}</span>` +
    (filename ? `<span class="jcode-codeblock__name">${escapeHtml(filename)}</span>` : '') +
    `<button type="button" class="jcode-codeblock__copy" data-code="${dataCode}" aria-label="Copy code">Copy</button>` +
    `</div>`

  return (
    `<div class="jcode-codeblock" data-lang="${escapeHtml(lang)}"` +
    (filename ? ` data-filename="${escapeHtml(filename)}"` : '') +
    `>` +
    bar +
    `<pre><code class="hljs${langClass}">${highlighted}\n</code></pre>` +
    `</div>`
  )
}

// ─── Math tokenizers (installed on demand by registerMathRenderer) ──────────

const blockMathExtension: TokenizerAndRendererExtension = {
  name: 'blockMath',
  level: 'block',
  start(src) {
    const i = src.indexOf('$$')
    return i < 0 ? undefined : i
  },
  tokenizer(src) {
    const m = /^\$\$([\s\S]+?)\$\$/.exec(src)
    if (!m) return undefined
    return { type: 'blockMath', raw: m[0], text: m[1].trim() }
  },
  renderer(token) {
    if (!mathRenderer) return escapeHtml(token.raw)
    try {
      return `<div class="jcode-math jcode-math--block">${mathRenderer(token.text as string, true)}</div>`
    } catch {
      return escapeHtml(token.raw)
    }
  },
}

const inlineMathExtension: TokenizerAndRendererExtension = {
  name: 'inlineMath',
  level: 'inline',
  start(src) {
    const i = src.indexOf('$')
    return i < 0 ? undefined : i
  },
  tokenizer(src) {
    // Single `$…$`, not `$$` (that is the block rule). Allow escaped `\$`.
    const m = /^\$(?!\$)((?:\\\$|[^$\n])+?)\$/.exec(src)
    if (!m) return undefined
    return { type: 'inlineMath', raw: m[0], text: m[1].trim() }
  },
  renderer(token) {
    if (!mathRenderer) return escapeHtml(token.raw)
    try {
      return `<span class="jcode-math jcode-math--inline">${mathRenderer(token.text as string, false)}</span>`
    } catch {
      return escapeHtml(token.raw)
    }
  },
}

// ─── marked instance ────────────────────────────────────────────────────────

const marked = new Marked()
marked.setOptions({ breaks: true, gfm: true })
marked.use({ renderer: { code: (token) => renderCodeBlock(token) } })

/** Wrap each <table> so wide GFM tables scroll inside a framed container. */
function wrapTables(html: string): string {
  return html
    .replace(/<table(\s[^>]*)?>/gi, '<div class="jcode-md-table-wrap"><table$1>')
    .replace(/<\/table>/gi, '</table></div>')
}

// ─── Sanitization (DOM-only) ────────────────────────────────────────────────

// DOMPurify's default export auto-binds to `window` at import time when a DOM
// exists; in Node it stays an uninitialized factory with no `.sanitize`. Guard
// on capability so SSR/tests get a pass-through (the HTML is only ever injected
// in the browser, which always has a real sanitizer).
const canSanitize = typeof DOMPurify.sanitize === 'function'

const SANITIZE_CONFIG = {
  // class → code-block chrome + table wrap; data-* → copy button payload + meta;
  // style → katex inline layout; target → external links; mark → highlights.
  ADD_ATTR: ['target', 'class', 'style', 'data-code', 'data-lang', 'data-filename', 'data-mermaid-src'],
  ADD_TAGS: ['mark'],
}

function sanitize(html: string): string {
  return canSanitize ? DOMPurify.sanitize(html, SANITIZE_CONFIG) : html
}

/** Render markdown → sanitized HTML string. */
export function renderMarkdown(text: string): string {
  const raw = marked.parse(text) as string
  const withTables = wrapTables(typeof raw === 'string' ? raw : String(raw))
  return sanitize(withTables)
}

// ─── Copy-button wiring ─────────────────────────────────────────────────────

/**
 * Attach one delegated click handler that powers every `.jcode-codeblock__copy`
 * button under `root`. Call once on the container that holds rendered markdown
 * (idempotent per element). On click, copies the decoded `data-code` payload via
 * the shared clipboard helper (with execCommand fallback) and flips the label to
 * "Copied" for 1.5s — or to "Copy failed" when both write paths are rejected,
 * so a denied clipboard is never reported as success.
 *
 * @returns a cleanup function that removes the listener.
 */
export function bindCodeBlockCopy(root: HTMLElement): () => void {
  const KEY = '__jcodeCopyBound'
  const el = root as HTMLElement & Record<string, unknown>
  if (el[KEY]) return () => {}
  el[KEY] = true

  const onClick = (ev: Event) => {
    const target = ev.target as HTMLElement | null
    const btn = target?.closest<HTMLElement>('.jcode-codeblock__copy')
    if (!btn || !root.contains(btn)) return
    const raw = btn.getAttribute('data-code')
    if (raw == null) return
    let code = raw
    try {
      code = decodeURIComponent(raw)
    } catch {
      /* keep raw if it was not encoded */
    }
    void copyText(code).then(
      (ok) => (ok ? flashCopied(btn) : flashCopyFailed(btn)),
      () => flashCopyFailed(btn),
    )
  }

  root.addEventListener('click', onClick)
  return () => {
    root.removeEventListener('click', onClick)
    el[KEY] = false
  }
}

function flashCopied(btn: HTMLElement): void {
  if (btn.getAttribute('data-copied') === '1') return
  const prev = btn.textContent
  btn.setAttribute('data-copied', '1')
  btn.textContent = 'Copied'
  window.setTimeout(() => {
    btn.textContent = prev
    btn.removeAttribute('data-copied')
  }, 1500)
}

function flashCopyFailed(btn: HTMLElement): void {
  if (btn.getAttribute('data-copy-failed') === '1') return
  const prev = btn.textContent
  btn.setAttribute('data-copy-failed', '1')
  btn.textContent = 'Copy failed'
  window.setTimeout(() => {
    btn.textContent = prev
    btn.removeAttribute('data-copy-failed')
  }, 1500)
}
