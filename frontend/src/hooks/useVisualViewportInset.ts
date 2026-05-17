import { onCleanup, onMount } from 'solid-js'
import { createRafCoalescer } from '~/lib/rafCoalesce'

/**
 * Bridge `window.visualViewport` state into two CSS custom properties
 * that `global.css.ts` consumes for the iOS standalone PWA layout:
 *
 *   --vvh        `visualViewport.height` (px), published **only**
 *                while an editable element is focused. Consumers that
 *                want to clamp against the visible-above-keyboard
 *                region (composer popovers, dropdowns) can read it.
 *                Body height itself uses `100dvh` and does not
 *                consume `--vvh`.
 *
 *   --vv-offset  `visualViewport.offsetTop` (px), published **only**
 *                while no editable is focused AND the offset is
 *                > 0.5 px. Body uses it as
 *                `transform: translateY(calc(-1 * var(--vv-offset, 0px)))`
 *                to cancel the residual offset that iOS 26 WebKit
 *                leaves non-zero after keyboard dismiss (FB19889436).
 *                Suppressed during keyboard-up because iOS already
 *                translates the visual viewport then; counter-
 *                translating would double-shift the layout.
 *
 * Focus is the keyboard signal of choice because in iOS standalone
 * PWA mode `window.innerHeight` shrinks alongside
 * `visualViewport.height` when the keyboard appears — a diff-based
 * detector never fires there. Listeners are rAF-coalesced.
 */
function isEditable(el: Element | null): boolean {
  if (!el)
    return false
  const tag = el.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA')
    return true
  return (el as HTMLElement).isContentEditable === true
}

export function useVisualViewportInset() {
  if (typeof window === 'undefined')
    return

  onMount(() => {
    let editableFocused = isEditable(document.activeElement)
    // Skip redundant DOM writes: iOS Safari fires `visualViewport.scroll`
    // continuously during address-bar / keyboard animation, and most
    // ticks produce the same px value. Empty string = "currently unset".
    let lastVvh = ''
    let lastOffset = ''

    const apply = () => {
      const root = document.documentElement
      const vv = window.visualViewport

      const nextVvh = editableFocused && vv ? `${vv.height}px` : ''
      if (nextVvh !== lastVvh) {
        if (nextVvh)
          root.style.setProperty('--vvh', nextVvh)
        else
          root.style.removeProperty('--vvh')
        lastVvh = nextVvh
      }

      // Sub-pixel jitter (e.g. 0.333 after a scroll) is ignored.
      const offsetTop = vv?.offsetTop ?? 0
      const nextOffset = !editableFocused && offsetTop > 0.5 ? `${offsetTop}px` : ''
      if (nextOffset !== lastOffset) {
        if (nextOffset)
          root.style.setProperty('--vv-offset', nextOffset)
        else
          root.style.removeProperty('--vv-offset')
        lastOffset = nextOffset
      }
    }

    const coalescer = createRafCoalescer<void>(apply)
    const schedule = () => coalescer.push()

    const onFocusIn = (e: FocusEvent) => {
      if (isEditable(e.target as Element | null)) {
        editableFocused = true
        schedule()
      }
    }
    const onFocusOut = () => {
      // Wait a microtask so a focus *transition* between two editables
      // (e.g. Tab key) doesn't flicker `--vvh` off and back on.
      queueMicrotask(() => {
        if (!isEditable(document.activeElement)) {
          editableFocused = false
          schedule()
        }
      })
    }

    // Initial sync write so the first paint after hydration has a value
    // (or correctly omits one when nothing is focused).
    apply()

    const vv = window.visualViewport
    if (vv) {
      vv.addEventListener('resize', schedule)
      vv.addEventListener('scroll', schedule)
    }
    // Belt-and-braces: listen to window.resize too. On some iOS Chrome
    // builds and embedded WebViews `visualViewport.resize` is silent
    // when the virtual keyboard appears, but `window.resize` still
    // fires. Cheap to attach; redundant when both fire (rAF dedupes).
    window.addEventListener('resize', schedule)
    // iOS quirk: returning from background (Safari → home → reopen)
    // can leave `offsetTop` dirty. `pageshow` fires after such a
    // restore (incl. bfcache) and gives us a chance to resync.
    window.addEventListener('pageshow', schedule)
    document.addEventListener('focusin', onFocusIn)
    document.addEventListener('focusout', onFocusOut)

    onCleanup(() => {
      if (vv) {
        vv.removeEventListener('resize', schedule)
        vv.removeEventListener('scroll', schedule)
      }
      window.removeEventListener('resize', schedule)
      window.removeEventListener('pageshow', schedule)
      document.removeEventListener('focusin', onFocusIn)
      document.removeEventListener('focusout', onFocusOut)
      coalescer.abort()
      // Remove the custom properties so test environments are isolated.
      // In production unmount is effectively unload, so this is a no-op.
      document.documentElement.style.removeProperty('--vvh')
      document.documentElement.style.removeProperty('--vv-offset')
    })
  })
}
