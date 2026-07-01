/**
 * Swallow the benign "ResizeObserver loop ..." browser error so it can't pop the
 * @solidjs/start dev overlay.
 *
 * Chromium fires a `window` `error` event with one of these messages whenever it
 * can't deliver every resize notification within a single frame's observation
 * loop and defers the remainder to the next frame. It is SELF-HEALING -- the
 * deferred notifications arrive next frame and nothing is actually broken -- but
 * the chat's virtualizer observes every mounted row, so a long transcript with
 * lots of async content settling (syntax highlighting, images) while scrolling
 * routinely trips the loop. The message is emitted by the browser based on
 * delivery timing/volume, so a callback-side early-return or rAF/microtask
 * deferral cannot prevent it (that work already exists for the avoidable causes;
 * this is the residual browser-inherent case).
 *
 * @solidjs/start's dev overlay (dev-overlay/index.jsx) listens for ANY window
 * `error` event and shows a full-screen 500 dialog; because the RO event carries
 * no `error` object it renders the raw event. This installer intercepts ONLY the
 * two known RO-loop messages so genuine errors still reach the overlay.
 */

// Both spellings: modern Chromium ("...completed with undelivered notifications")
// and older Chromium / other engines ("...loop limit exceeded").
const RESIZE_OBSERVER_LOOP_RE
  = /^ResizeObserver loop (?:limit exceeded|completed with undelivered notifications)/

/** Whether an error-event message is the benign ResizeObserver delivery-loop warning. */
export function isResizeObserverLoopError(message: unknown): boolean {
  return typeof message === 'string' && RESIZE_OBSERVER_LOOP_RE.test(message)
}

/** The event-target surface the installer needs (a Window in the browser). */
export interface ErrorEventTarget {
  addEventListener: (
    type: 'error',
    listener: (event: Event) => void,
    options?: boolean | AddEventListenerOptions,
  ) => void
  removeEventListener: (
    type: 'error',
    listener: (event: Event) => void,
    options?: boolean | EventListenerOptions,
  ) => void
}

function defaultTarget(): ErrorEventTarget | undefined {
  return typeof window === 'undefined' ? undefined : window
}

/**
 * Install a capture-phase `error` listener that suppresses the benign
 * ResizeObserver loop warning. Returns a disposer that removes the listener.
 *
 * Registered from `entry-client.tsx` BEFORE `mount()` runs, so it is added ahead
 * of the dev overlay's own window `error` listener (registered in a createEffect
 * during mount). At the target phase listeners fire in registration order, so
 * running first lets `stopImmediatePropagation()` keep the event from reaching
 * the overlay. `preventDefault()` additionally quiets the console line. No-op
 * outside a DOM (SSR / non-browser), where `target` resolves to undefined.
 */
export function installResizeObserverLoopErrorSuppressor(
  target: ErrorEventTarget | undefined = defaultTarget(),
): () => void {
  if (!target)
    return () => {}
  const onError = (event: Event) => {
    if (!isResizeObserverLoopError((event as ErrorEvent).message))
      return
    event.stopImmediatePropagation()
    event.preventDefault()
  }
  target.addEventListener('error', onError, true)
  return () => target.removeEventListener('error', onError, true)
}
