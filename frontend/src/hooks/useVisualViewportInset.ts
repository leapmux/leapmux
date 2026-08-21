import type { Accessor } from 'solid-js'
import { createSignal, onCleanup, onMount } from 'solid-js'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { isSoftKeyboardTarget, setSoftKeyboardVisible } from '~/lib/softKeyboard'

/**
 * Bridge `window.visualViewport` state into two CSS custom properties
 * that `global.css.ts` consumes for the iOS layout (Safari and the
 * standalone PWA alike):
 *
 *   --vvh        `visualViewport.height` (px) — the SIZE of the region
 *                iOS actually shows. Published while an editable is
 *                focused, which is to say while the keyboard is up.
 *                Body height consumes it as
 *                `height: var(--vvh, 100dvh)`, because on iOS `dvh`
 *                does NOT shrink for the on-screen keyboard: WebKit
 *                moves part of the viewport out of sight instead of
 *                resizing it, so `visualViewport.height` is the only
 *                value that accounts for the keyboard there.
 *
 *   --vv-shift   The vertical correction the body applies as
 *                `transform: translateY(var(--vv-shift, 0px))` (px) —
 *                the POSITION of that region. Published while
 *                `visualViewport.offsetTop` exceeds 0.5 px, carrying
 *                its own sign, because WebKit displaces the page in
 *                opposite directions in the two keyboard states:
 *
 *                  Keyboard UP: iOS scrolls the visual viewport down to
 *                  reveal the focused composer. `body` is
 *                  `position: fixed`, so it stays with the LAYOUT
 *                  viewport and the app reads as shifted UP by
 *                  `offsetTop` — the mobile tab bar leaves the top of
 *                  the screen. Correction: `+offsetTop`.
 *
 *                  Keyboard DOWN: iOS 26 WebKit leaves a residual
 *                  `offsetTop` it never returns to 0 after the keyboard
 *                  dismisses (FB19889436). Correction: `-offsetTop`.
 *
 * THE KEYBOARD-UP PAIR SHIPS TOGETHER OR NOT AT ALL. The shift alone
 * runs away: with the body still `100dvh` it is taller than the visible
 * region, so moving it down by `offsetTop` pushes the composer below the
 * keyboard, iOS scrolls further to reveal it, the larger offset moves
 * the body down again, and the composer ends up permanently out of view,
 * flashing in on each keystroke. The height alone leaves the app the
 * right size in the wrong place — the original gap. With both, the body
 * covers the visible region exactly, the composer sits at its bottom
 * edge, and iOS has nothing left to scroll into view, which is what ends
 * the loop.
 *
 * Both are published on a SOFT-KEYBOARD device only, which
 * `(pointer: coarse)` identifies. On a desktop these same values track
 * a pinch-zoom, where resizing or translating the body fights the
 * user's own gesture.
 *
 * RETURNS that same keyboard-is-up condition as a signal, for the layout
 * decisions the properties above cannot express. `AppShell` hides the
 * mobile tab bar on it: with the keyboard up the bar carries nothing the
 * user can act on, and leaving it pinned to the top of a viewport that
 * just moved makes it re-place itself in view on every focus and blur.
 * Hiding gives its height to the transcript instead.
 *
 * KEYBOARD-UP IS FOCUS **AND** A SHRUNK VIEWPORT, never focus alone.
 * `MarkdownEditor` focuses the composer whenever it is enabled, so a
 * chat that merely opens holds focus with no keyboard anywhere — iOS
 * does not raise one for a programmatic focus outside a user gesture.
 * Keying on focus alone therefore hid the tab bar the moment a chat
 * opened, and gave the body a height for a keyboard that was not there.
 * The shrink is measured against `baselineHeight` below, not against
 * `window.innerHeight`: in the iOS standalone PWA `innerHeight` shrinks
 * alongside `visualViewport.height`, so that difference is always 0
 * there. Listeners are rAF-coalesced.
 */

/**
 * How much the visible viewport must lose before it counts as a keyboard.
 *
 * Comfortably below any on-screen keyboard (the shortest is ~200 px) and
 * comfortably above the browser-chrome changes that also resize the
 * visual viewport — iOS Safari collapsing its toolbar moves ~50-100 px,
 * and mistaking that for a keyboard would hide the tab bar mid-scroll.
 */
const KEYBOARD_MIN_INSET_PX = 120
/**
 * The soft-keyboard test, or undefined where `matchMedia` is absent
 * (jsdom, an exotic host). Absent reads as "correct anyway": the
 * correction fires only on a non-zero `offsetTop`, and refusing it
 * outright would drop the iOS mitigation on any host that reports no
 * media queries.
 */
function coarsePointerQuery(): MediaQueryList | undefined {
  return typeof window.matchMedia === 'function'
    ? window.matchMedia('(pointer: coarse)')
    : undefined
}

/** @returns whether the soft keyboard is up, for layout that must react to it. */
export function useVisualViewportInset(): Accessor<boolean> {
  const [softKeyboardUp, setSoftKeyboardUp] = createSignal(false)
  if (typeof window === 'undefined')
    return softKeyboardUp

  onMount(() => {
    let editableFocused = isSoftKeyboardTarget(document.activeElement)
    // Skip redundant DOM writes: iOS Safari fires `visualViewport.scroll`
    // continuously during address-bar / keyboard animation, and most
    // ticks produce the same px value. Empty string = "currently unset".
    let lastVvh = ''
    let lastShift = ''
    // The visible height with no keyboard, which every shrink is measured
    // against. Seeded at mount because a page that just loaded cannot have a
    // keyboard up, then re-taken on every tick with nothing focused, so it
    // follows a rotation or a browser-chrome change while it can.
    let baselineHeight = window.visualViewport?.height ?? 0
    // Held rather than re-queried per frame: a MediaQueryList tracks the
    // device live, so this stays current when a paired mouse flips an
    // iPad to a fine pointer.
    const coarsePointer = coarsePointerQuery()

    const apply = () => {
      const root = document.documentElement
      const vv = window.visualViewport
      const softKeyboard = coarsePointer?.matches ?? true
      const height = vv?.height ?? 0
      if (!editableFocused && height > 0)
        baselineHeight = height
      // An editable holds focus on a device whose keyboard takes screen
      // space, AND the viewport lost enough height to be that keyboard. Both
      // properties below and the returned signal answer to it; see the module
      // comment for why the focus half alone is not the keyboard.
      const keyboardUp = editableFocused
        && softKeyboard
        && height > 0
        && baselineHeight - height >= KEYBOARD_MIN_INSET_PX
      setSoftKeyboardUp(keyboardUp)
      // The same fact, for the callers that are not in the reactive graph:
      // the composer releases focus on a completed send only when a keyboard
      // is genuinely in the way. See `~/lib/softKeyboard`.
      setSoftKeyboardVisible(keyboardUp)

      const nextVvh = keyboardUp && vv ? `${vv.height}px` : ''
      if (nextVvh !== lastVvh) {
        if (nextVvh)
          root.style.setProperty('--vvh', nextVvh)
        else
          root.style.removeProperty('--vvh')
        lastVvh = nextVvh
      }

      // Sub-pixel jitter (e.g. 0.333 after a scroll) is ignored.
      const offsetTop = vv?.offsetTop ?? 0
      // See the module comment for why the two keyboard states take
      // opposite signs, and why the keyboard-up sign is only correct
      // beside the `--vvh` height above.
      const nextShift = offsetTop > 0.5 && softKeyboard
        ? (keyboardUp ? `${offsetTop}px` : `-${offsetTop}px`)
        : ''
      if (nextShift !== lastShift) {
        if (nextShift)
          root.style.setProperty('--vv-shift', nextShift)
        else
          root.style.removeProperty('--vv-shift')
        lastShift = nextShift
      }
    }

    const coalescer = createRafCoalescer<void>(apply)
    const schedule = () => coalescer.push()

    const onFocusIn = (e: FocusEvent) => {
      if (isSoftKeyboardTarget(e.target as Element | null)) {
        editableFocused = true
        schedule()
      }
    }
    const onFocusOut = () => {
      // Wait a microtask so a focus *transition* between two editables
      // (e.g. Tab key) doesn't flicker `--vvh` off and back on.
      queueMicrotask(() => {
        if (!isSoftKeyboardTarget(document.activeElement)) {
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
      document.documentElement.style.removeProperty('--vv-shift')
      setSoftKeyboardUp(false)
      setSoftKeyboardVisible(false)
    })
  })

  return softKeyboardUp
}
