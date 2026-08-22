import { showWarnToastWithLoggedCause } from '~/components/common/Toast'

export interface CopyTextOptions {
  /**
   * Tell the user when the write does not land. Defaults to `true`.
   *
   * Set it to `false` only for a copy that the user did not ask for by name.
   * The terminal's copy-on-select is the one such caller: it writes on every
   * selection change, so a single drag would raise a toast for each update of
   * the highlight.
   */
  announceFailure?: boolean
}

/**
 * Write `text` to the system clipboard, reporting whether it landed.
 *
 * Never rejects and never throws. Two paths, in order: the async Clipboard API,
 * then {@link copyWithTemporaryTextArea}. The fallback is what carries a
 * non-secure origin -- plain `http://` on a LAN, which is how the app is read
 * on a phone -- because the browser exposes no `navigator.clipboard` there at
 * all. An empty input writes nothing and reports `false`, so a cleared
 * selection cannot clobber the clipboard.
 *
 * When BOTH paths fail the user is told, and told which of the two causes
 * applies. That announcement lives HERE rather than at each call site so that
 * no caller can drop it: every copy in the app reaches the clipboard through
 * this function, and the silent `false` that it used to return is what let an
 * unreachable clipboard look like a dead button.
 *
 * A caller that reports success itself must still await the result, so it
 * cannot claim to have copied something it did not.
 */
export async function copyTextToClipboard(text: string, options: CopyTextOptions = {}): Promise<boolean> {
  if (text.length === 0)
    return false

  // Held for the log line at the end: a rejection names the real cause -- a
  // denied permission, a document that does not hold focus -- and the
  // fallback's bare boolean does not.
  let cause: unknown
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    }
    catch (err) {
      cause = err
    }
  }

  if (copyWithTemporaryTextArea(text))
    return true

  if (options.announceFailure ?? true)
    showWarnToastWithLoggedCause(failureMessage(), cause)
  return false
}

/**
 * The sentence the user reads when nothing could reach the clipboard.
 *
 * Two causes, because the remedy differs. The browser withholds
 * `navigator.clipboard` from every origin except `https`, `localhost`,
 * `127.0.0.1`, `::1` and `*.localhost`, and the user answers that by changing
 * how they reach the app. Anything else is the browser refusing a clipboard
 * that does exist, which the user answers at the browser.
 */
function failureMessage(): string {
  // An explicit `false`, not a falsy test: jsdom leaves `isSecureContext`
  // undefined, and "unknown" must not accuse the page of being insecure.
  // `~/lib/systemInfo` reads the same property the same way, for this reason.
  if (typeof window !== 'undefined' && window.isSecureContext === false)
    return 'Could not copy. The browser gives no clipboard to an insecure page. Open the app over HTTPS, or on localhost.'
  return 'Could not copy. The browser refused access to the clipboard.'
}

/**
 * Copy through a temporary `<textarea>` and the legacy `execCommand`.
 *
 * `execCommand` copies THE SELECTION, so this makes one and puts the user's own
 * selection back before it returns. Restoring is not a nicety: the chat
 * transcript and both file views run this while a live highlight is on screen,
 * and `SelectionQuotePopover` acts on that same highlight straight after --
 * clearing it to report the copy, or keeping it up when the copy failed. Each
 * saved range is a CLONE, because the ranges a live `Selection` hands out follow
 * it and would otherwise all collapse onto the textarea.
 *
 * `focus()` + `select()` is the recipe, NOT the widely-published iOS one that
 * marks the element `contentEditable` and selects a `Range` over its contents.
 * Measured on an insecure origin, in both engines: the `Range` form copies
 * nothing in WebKit -- `execCommand` returns `false` -- while this form
 * succeeds in WebKit and in Blink. `readOnly` is what keeps the on-screen
 * keyboard down for a textarea that the user never sees.
 *
 * Reports `false` rather than throwing where there is no document (a
 * server-side render) or no `execCommand` (jsdom).
 */
function copyWithTemporaryTextArea(text: string): boolean {
  if (typeof document === 'undefined' || typeof document.execCommand !== 'function' || !document.body)
    return false

  const activeElement = document.activeElement
  const selection = typeof window === 'undefined' ? null : window.getSelection()
  const savedRanges = saveSelectionRanges(selection)

  const holder = document.createElement('textarea')
  holder.value = text
  holder.readOnly = true
  holder.setAttribute('aria-hidden', 'true')
  // Off the screen but still RENDERED. `display: none`, `visibility: hidden`
  // and a zero size each make the textarea unselectable, and a selection is
  // the one thing this path needs.
  holder.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;padding:0;border:0;margin:0;opacity:0'
  document.body.appendChild(holder)

  try {
    holder.focus({ preventScroll: true })
    holder.select()
    holder.setSelectionRange(0, text.length)
    return document.execCommand('copy')
  }
  catch {
    return false
  }
  finally {
    restoreAfterFallback(holder, selection, savedRanges, activeElement)
  }
}

/**
 * The ranges that make up `selection` now, each one cloned.
 *
 * Defensive, and deliberately so: this runs before the `try` below it, and
 * `copyTextToClipboard` promises never to throw. `getRangeAt` raises when the
 * count it was asked about has already changed, which a selection that the user
 * is still dragging can do between two statements.
 */
function saveSelectionRanges(selection: Selection | null): Range[] {
  if (!selection)
    return []
  try {
    return Array.from({ length: selection.rangeCount }, (_, index) => selection.getRangeAt(index).cloneRange())
  }
  catch {
    return []
  }
}

/**
 * Undo everything the fallback disturbed: the textarea, the selection, the focus.
 *
 * Guarded as a whole because it runs from a `finally`. An exception raised here
 * would REPLACE the result that the copy already produced, so a write that
 * landed would reach the caller as a thrown error inside a click handler.
 * Removing the textarea comes first and cannot throw, so a refusal further down
 * still cannot leave the element in the document.
 */
function restoreAfterFallback(
  holder: HTMLTextAreaElement,
  selection: Selection | null,
  savedRanges: Range[],
  activeElement: Element | null,
): void {
  holder.remove()
  try {
    if (selection) {
      selection.removeAllRanges()
      for (const range of savedRanges)
        selection.addRange(range)
    }
    // The focus goes back to whatever held it -- the composer, xterm's helper
    // textarea -- so a copy cannot swallow the next keystroke.
    if (activeElement instanceof HTMLElement)
      activeElement.focus({ preventScroll: true })
  }
  catch {
    // The engine refused to take the selection or the focus back. The copy
    // itself already happened, and there is no second way to ask for either.
  }
}
