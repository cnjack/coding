/**
 * Copy-target model — the shared "what can I copy from this response" analysis.
 *
 * Mirrors the TUI analyzer (internal/tui/copy_targets.go) so `/copy` in the
 * terminal and the copy menu in Web/Desktop offer byte-identical targets and
 * byte-identical text for the same response:
 *
 *   - Full response — the whole message markdown.
 *   - Code block N — one fenced code block per target. Fence markers and the
 *     info string (language / filename chrome) never enter the copied text.
 *   - Blockquote N — one consecutive `>` group per target (a blank line ends a
 *     group); the `>` marker and one following space are stripped per line.
 *
 * Fence rules: 3+ backticks or 3+ tildes (never mixed), optional indentation;
 * the opener's indentation is stripped from content lines; an unterminated
 * fence (still-streaming response) still yields its partial content.
 * Blockquotes inside a code fence are code, not quotes.
 *
 * Analysis always runs on the message markdown — never on rendered HTML or
 * hidden metadata — so the copied bytes match across local, remote/SSH and
 * cloud-relayed sessions.
 */

import { parseCodeInfo } from './markdown.js'

export type CopyTargetKind = 'full' | 'code' | 'quote'

export interface CopyTarget {
  kind: CopyTargetKind
  /** Menu title, e.g. "Full response" / "Code block 2". */
  label: string
  /** Secondary line, e.g. "go · main.go · 512 B". */
  detail: string
  /** Exact bytes written to the clipboard. */
  text: string
  /** 1-based index within the kind. */
  index: number
  /** Code blocks: fence language, '' if none. */
  lang: string
  /** Code blocks: filename parsed from the info string, '' if none. */
  filename: string
}

export function analyzeCopyTargets(markdown: string): CopyTarget[] {
  const normalized = markdown.replaceAll('\r\n', '\n')
  const full = normalized.trim()
  if (!full) return []

  const targets: CopyTarget[] = [
    {
      kind: 'full',
      label: 'Full response',
      detail: `${humanSize(full.length)} · ${countLines(full)} lines`,
      text: full,
      index: 1,
      lang: '',
      filename: '',
    },
  ]

  const lines = normalized.split('\n')

  let inFence = false
  let fenceChar = ''
  let fenceLen = 0
  let fenceIndent = 0
  let fenceInfo = ''
  let codeLines: string[] = []
  let quoteLines: string[] = []
  let codeIdx = 0
  let quoteIdx = 0

  const flushQuote = () => {
    const body = quoteLines
    quoteLines = []
    while (body.length && !body[0].trim()) body.shift()
    while (body.length && !body[body.length - 1].trim()) body.pop()
    if (!body.length) return
    const text = body.map((l) => l.replace(/[ \t]+$/, '')).join('\n')
    if (!text.trim()) return
    quoteIdx++
    targets.push({
      kind: 'quote',
      label: `Blockquote ${quoteIdx}`,
      detail: quotePreview(text),
      text,
      index: quoteIdx,
      lang: '',
      filename: '',
    })
  }

  const flushCode = () => {
    codeIdx++
    const text = codeLines.join('\n')
    codeLines = []
    const { lang, filename } = parseCodeInfo(fenceInfo)
    const chrome = [lang, filename].filter(Boolean).join(' ')
    targets.push({
      kind: 'code',
      label: `Code block ${codeIdx}`,
      detail: chrome ? `${chrome} · ${humanSize(text.length)}` : humanSize(text.length),
      text,
      index: codeIdx,
      lang,
      filename,
    })
  }

  for (const line of lines) {
    const indent = line.length - line.replace(/^ +/, '').length
    const content = line.slice(indent)

    if (inFence) {
      const marker = fencePrefix(content)
      if (
        marker &&
        marker.char === fenceChar &&
        marker.len >= fenceLen &&
        indent <= fenceIndent &&
        marker.rest.trim() === ''
      ) {
        // Closing fence: only the fence chars (and whitespace) remain.
        inFence = false
        flushCode()
        continue
      }
      codeLines.push(dedent(line, fenceIndent))
      continue
    }

    const open = fencePrefix(content)
    if (open && open.len >= 3 && (open.char === '~' || !open.rest.includes('`'))) {
      // Opening fence (backtick fences cannot have backticks in the info).
      flushQuote()
      inFence = true
      fenceChar = open.char
      fenceLen = open.len
      fenceIndent = indent
      fenceInfo = open.rest.trim()
      continue
    }

    if (content.startsWith('>')) {
      quoteLines.push(content.slice(1).replace(/^ /, ''))
      continue
    }

    // Any non-quote line (blank included) ends the current blockquote group.
    flushQuote()
  }
  if (inFence) flushCode() // unterminated fence while streaming
  flushQuote()

  return targets
}

/** Fence prefix at the start of a line: the fence char, its repeat count and
 * the remainder of the line (the info string for opening fences, '' for
 * closing fences). */
function fencePrefix(content: string): { char: string; len: number; rest: string } | null {
  if (!content) return null
  const char = content[0]
  if (char !== '`' && char !== '~') return null
  let len = 0
  for (const c of content) {
    if (c !== char) break
    len++
  }
  return { char, len, rest: content.slice(len) }
}

/** Strip up to n leading spaces (the opener's indentation is not code). */
function dedent(line: string, n: number): string {
  let i = 0
  while (i < n && i < line.length && line[i] === ' ') i++
  return line.slice(i)
}

function quotePreview(text: string): string {
  const first = text.split('\n')[0].trim()
  return first.length > 48 ? first.slice(0, 48) + '…' : first
}

function humanSize(n: number): string {
  if (n < 1024) return `${n} B`
  return `${(n / 1024).toFixed(1)} KB`
}

function countLines(s: string): number {
  return s.split('\n').length
}
