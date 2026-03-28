import { createSignal } from 'solid-js'

/** Reusable clipboard-copy-with-feedback hook. Returns a `copied` signal and a `copy` handler. */
export function useCopyButton(getText: () => string | undefined) {
  const [copied, setCopied] = createSignal(false)
  const copy = async () => {
    const text = getText()
    if (!text)
      return
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(setCopied, 2000, false)
    }
    catch {
      // ignore clipboard errors
    }
  }
  return { copied, copy }
}
