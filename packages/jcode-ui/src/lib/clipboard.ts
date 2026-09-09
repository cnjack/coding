/**
 * Clipboard writes with an explicit success/failure result.
 *
 * Every copy affordance in jcode-ui (message copy, code-block copy, quote
 * selection, the copy-target menu) goes through this helper so clipboard
 * failures are never silently swallowed — callers flip their feedback to a
 * "copy failed" state instead of showing a fake success.
 *
 * Strategy:
 *   1. Async Clipboard API when available (secure contexts, Tauri WebView).
 *   2. Legacy `document.execCommand('copy')` fallback via an off-screen
 *      textarea (insecure contexts / older WebViews).
 *
 * Returns true when the write was accepted, false when both paths fail or no
 * DOM/clipboard API exists (SSR).
 */

export async function copyText(text: string): Promise<boolean> {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Permission denied or document not focused — fall through to the
      // legacy path before giving up.
    }
  }
  return copyViaExecCommand(text)
}

function copyViaExecCommand(text: string): boolean {
  if (typeof document === 'undefined') return false
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  // Off-screen but focusable/selectable; avoids layout shift and scrolling.
  ta.style.position = 'fixed'
  ta.style.top = '0'
  ta.style.left = '-9999px'
  ta.style.opacity = '0'
  let ok = false
  try {
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    ok = document.execCommand('copy')
  } catch {
    ok = false
  } finally {
    document.body.removeChild(ta)
  }
  return ok
}
