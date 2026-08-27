import { createSignal, onCleanup } from 'solid-js'

/**
 * How far ahead of the viewport an element counts as revealed.
 *
 * A margin rather than a bare edge test, so the work starts while the element
 * is still one short scroll away and is complete by the time it arrives.
 */
const REVEAL_MARGIN_PX = 200

/**
 * How long the latch waits for the observer before it gives up and reveals.
 *
 * The latch defers a LOAD, and no load is worth a control that never appears.
 * An IntersectionObserver reports nothing for an element with no box -- a row
 * whose container has not laid out, a subtree an ancestor still hides -- and it
 * reports nothing at all if the engine's implementation is stubbed. Either way
 * the deferred work must still arrive, so the fallback costs EAGERNESS, which
 * is the behaviour without this latch.
 */
const REVEAL_FALLBACK_MS = 1_000

export interface Revealed {
  /** True once the observed element came within reach of the viewport. */
  revealed: () => boolean
  /** A `ref` for the element whose arrival decides. */
  observe: (el: Element) => void
}

/**
 * A one-way latch: false until the user scrolls an element into view, true
 * after.
 *
 * It exists so a component does its work only when somebody looks at it. The
 * Preferences dialog is the case that needed it: the Account group leads the
 * list, so every open mounts it, and two of its rows issue a list request the
 * moment they mount -- for a user who came for Appearance and clicks away.
 * Both of those rows sit outside the viewport when the dialog opens.
 *
 * It LATCHES, and never returns to false. Scrolling past a row and back must
 * not re-run its load, and a revealed component must not be torn down under the
 * user's pointer. The observer disconnects on the first intersection, so the
 * cost is one observer that lives until the element arrives.
 *
 * With no `IntersectionObserver` -- jsdom, an engine too old for it -- the
 * element counts as revealed at once. That is the behaviour without this latch,
 * so a missing API costs eagerness rather than a component that never renders.
 *
 * An observer that EXISTS and stays silent takes the same answer, through the
 * fallback timer. The two cases are one rule: this latch may only ever DEFER
 * the work, never withhold it. Without the timer a zero-box element left the
 * deferred component unmounted for the life of the page -- and the components
 * behind it are settings editors, so the cost of getting that wrong is a
 * control the user cannot reach at all.
 */
export function createRevealed(): Revealed {
  const [revealed, setRevealed] = createSignal(false)
  let observer: IntersectionObserver | undefined
  let fallback: ReturnType<typeof setTimeout> | undefined

  const stop = () => {
    observer?.disconnect()
    observer = undefined
    if (fallback !== undefined) {
      clearTimeout(fallback)
      fallback = undefined
    }
  }
  onCleanup(stop)

  const observe = (el: Element) => {
    if (revealed())
      return
    if (typeof IntersectionObserver === 'undefined') {
      setRevealed(true)
      return
    }
    stop()
    observer = new IntersectionObserver((entries) => {
      if (!entries.some(entry => entry.isIntersecting))
        return
      setRevealed(true)
      stop()
    }, { rootMargin: `${REVEAL_MARGIN_PX}px` })
    observer.observe(el)
    fallback = setTimeout(() => {
      setRevealed(true)
      stop()
    }, REVEAL_FALLBACK_MS)
  }

  return { revealed, observe }
}
