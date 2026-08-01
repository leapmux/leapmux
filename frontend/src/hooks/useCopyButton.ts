import { createSignal } from 'solid-js'
import { copyTextToClipboard } from '~/lib/clipboard'

/**
 * Reusable clipboard-copy-with-feedback hook. Returns a `copied` signal and a
 * `copy` handler.
 *
 * Routed through `copyTextToClipboard` rather than touching
 * `navigator.clipboard` directly, so the "no clipboard API on a non-secure
 * origin" case is handled in one place -- and so the "Copied!" flash only fires
 * when something was actually copied, instead of on any platform where the
 * write silently could not happen.
 */
export function useCopyButton(getText: () => string | undefined) {
  const [copied, setCopied] = createSignal(false)
  const copy = async () => {
    const text = getText()
    if (!text || !await copyTextToClipboard(text))
      return
    setCopied(true)
    setTimeout(setCopied, 2000, false)
  }
  return { copied, copy }
}
