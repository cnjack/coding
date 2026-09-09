/**
 * QuoteSelection — "quote this" affordance for selected thread text.
 *
 * Watches text selections inside jcode-ui prose (`.jcode-prose` under a
 * `[data-jcode-ui]` root) and floats two buttons at the selection:
 *
 *   Quote — hands the selected text to `onQuote` (typically wired into the
 *           composer via `formatQuote`).
 *   Copy  — copies the selection *as a blockquote* (same `formatQuote`
 *           extraction) through the shared clipboard helper, so selection
 *           copying uses the same target semantics as message/code-block
 *           copying across Web and Desktop. A denied clipboard shows
 *           "Copy failed" instead of pretending to succeed.
 *
 *   const input = useRef<ComposerHandle>(null)
 *   <QuoteSelection onQuote={(t) => input.current?.insertText(formatQuote(t))} />
 *   <ChatInput ref={input} />
 *
 * Renders in a portal so ancestor overflow/transform can't clip the buttons.
 */

import { memo, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChatBubbleBottomCenterTextIcon } from '@heroicons/react/24/outline'
import { copyText } from '../lib/clipboard.js'

export interface QuoteSelectionProps {
  /** Receives the selected plain text when the user clicks Quote. */
  onQuote: (text: string) => void
  /** Quote button label. Default "Quote". */
  label?: string
  /** Max characters captured. Default 2000. */
  maxLength?: number
  /** Offer the Copy (blockquote) button. Default true. */
  showCopy?: boolean
  /** Copy button label. Default "Copy". */
  copyLabel?: string
}

/** Turn selected text into a markdown blockquote block for the composer. */
export function formatQuote(text: string): string {
  const quoted = text
    .trim()
    .split('\n')
    .map((l) => `> ${l}`)
    .join('\n')
  return `${quoted}\n\n`
}

interface Anchor {
  x: number
  y: number
  text: string
}

type CopyFeedback = 'idle' | 'ok' | 'failed'

export const QuoteSelection = memo(function QuoteSelection({
  onQuote,
  label = 'Quote',
  maxLength = 2000,
  showCopy = true,
  copyLabel = 'Copy',
}: QuoteSelectionProps) {
  const [anchor, setAnchor] = useState<Anchor | null>(null)
  const [copyFeedback, setCopyFeedback] = useState<CopyFeedback>('idle')
  const buttonRef = useRef<HTMLButtonElement | null>(null)

  useEffect(() => {
    if (typeof document === 'undefined') return

    const update = () => {
      const sel = document.getSelection()
      if (!sel || sel.isCollapsed || sel.rangeCount === 0) {
        setAnchor(null)
        return
      }
      const text = sel.toString().trim()
      if (!text) {
        setAnchor(null)
        return
      }
      // Only offer quoting for selections that start inside jcode-ui prose.
      const start =
        sel.anchorNode instanceof Element ? sel.anchorNode : sel.anchorNode?.parentElement
      if (!start?.closest('.jcode-prose')?.closest('[data-jcode-ui]')) {
        setAnchor(null)
        return
      }
      const rect = sel.getRangeAt(0).getBoundingClientRect()
      if (rect.width === 0 && rect.height === 0) {
        setAnchor(null)
        return
      }
      setAnchor({
        x: rect.left + rect.width / 2,
        y: rect.top,
        text: text.slice(0, maxLength),
      })
    }

    // selectionchange fires continuously while dragging; a small debounce keeps
    // the button from chasing the cursor.
    let t: ReturnType<typeof setTimeout> | undefined
    const onSelectionChange = () => {
      clearTimeout(t)
      t = setTimeout(update, 120)
    }
    const onScroll = () => setAnchor(null)
    document.addEventListener('selectionchange', onSelectionChange)
    window.addEventListener('scroll', onScroll, true)
    return () => {
      clearTimeout(t)
      document.removeEventListener('selectionchange', onSelectionChange)
      window.removeEventListener('scroll', onScroll, true)
    }
  }, [maxLength])

  if (!anchor || typeof document === 'undefined') return null

  const copySelection = async () => {
    const ok = await copyText(formatQuote(anchor.text))
    setCopyFeedback(ok ? 'ok' : 'failed')
    // Flash the result on the button, then dismiss the affordance.
    setTimeout(() => {
      setCopyFeedback('idle')
      setAnchor(null)
    }, 1200)
  }

  const copyBtnLabel =
    copyFeedback === 'ok' ? 'Copied' : copyFeedback === 'failed' ? 'Copy failed' : copyLabel

  return createPortal(
    <div
      data-jcode-ui=""
      className="jcode-quote-btn-group"
      style={{ left: anchor.x, top: anchor.y }}
    >
      <button
        ref={buttonRef}
        type="button"
        className="jcode-quote-btn"
        data-copy-feedback={copyFeedback !== 'idle' ? copyFeedback : undefined}
        // preventDefault on mousedown so the click doesn't clear the selection
        // before we read it.
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => {
          onQuote(anchor.text)
          setAnchor(null)
          document.getSelection()?.removeAllRanges()
        }}
      >
        <ChatBubbleBottomCenterTextIcon className="h-3.5 w-3.5" />
        <span>{label}</span>
      </button>
      {showCopy && (
        <button
          type="button"
          className="jcode-quote-btn"
          data-copy-feedback={copyFeedback !== 'idle' ? copyFeedback : undefined}
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => void copySelection()}
        >
          <ChatBubbleBottomCenterTextIcon className="h-3.5 w-3.5" />
          <span>{copyBtnLabel}</span>
        </button>
      )}
    </div>,
    document.body,
  )
})
