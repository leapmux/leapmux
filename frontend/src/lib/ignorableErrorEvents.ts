import { monotonicNow } from './monotonicNow'

/**
 * Classify and swallow the window `error` events that carry nothing anyone can
 * act on, so they cannot pop the @solidjs/start dev overlay or toast the user.
 *
 * @solidjs/start's dev overlay (dev-overlay/index.jsx) listens for ANY window
 * `error` event and shows a full-screen 500 dialog; both classes below arrive
 * with no `error` object, so the overlay renders the raw event and tells the
 * developer nothing. `installGlobalErrorSink` reports the same two classes as a
 * "Something went wrong" toast, which tells the user even less. Both consumers
 * ask this module the same question, so the answer lives in one place.
 *
 * Two classes qualify, and they need DIFFERENT treatment -- see
 * `installIgnorableErrorSuppressor` for why only one of them is hidden from the
 * browser console.
 */

/** Which property makes an event ignorable. Also the label in the debug line. */
export type IgnorableErrorReason = 'resize-observer-loop' | 'muted'

/**
 * Both spellings of the ResizeObserver delivery-loop warning: modern Chromium
 * ("...completed with undelivered notifications") and older Chromium / other
 * engines ("...loop limit exceeded").
 *
 * The browser fires this whenever it cannot deliver every resize notification
 * within a single frame's observation loop and defers the remainder to the next
 * frame. It is SELF-HEALING -- the deferred notifications arrive next frame and
 * nothing is actually broken -- but the chat's virtualizer observes every
 * mounted row, so a long transcript with lots of async content settling (syntax
 * highlighting, images) while scrolling routinely trips the loop. The message is
 * emitted by the browser based on delivery timing/volume, so a callback-side
 * early-return or rAF/microtask deferral cannot prevent it (that work already
 * exists for the avoidable causes; this is the residual browser-inherent case).
 */
const RESIZE_OBSERVER_LOOP_RE
  = /^ResizeObserver loop (?:limit exceeded|completed with undelivered notifications)/

/** Whether an error-event message is the benign ResizeObserver delivery-loop warning. */
export function isResizeObserverLoopError(message: unknown): boolean {
  return typeof message === 'string' && RESIZE_OBSERVER_LOOP_RE.test(message)
}

/**
 * The sanitized message that the HTML spec's "muted errors" path substitutes.
 * The spec says `Script error.`; the period is optional here because not every
 * engine appends it.
 */
const MUTED_ERROR_MESSAGE_RE = /^Script error\.?$/

/** True when the number is zero or the event double omitted it. */
function zeroOrAbsent(value: number | undefined): boolean {
  return value === undefined || value === 0
}

/**
 * Whether the browser refused to describe this error to page JS.
 *
 * When an error comes from a script the page loaded cross-origin without CORS,
 * or the engine synthesizes one with no associated script, the browser replaces
 * every informative field before any listener runs: `message` becomes
 * `Script error.`, `filename` becomes empty, `lineno`/`colno` become 0, and
 * `error` becomes null. Nothing survives that a handler could report, log or
 * repair, so the only correct response is to ignore the event.
 *
 * iOS Safari reaches this path far more readily than Chromium does, which is how
 * a plain "Script error." overlay appears when the iOS share sheet resizes and
 * snapshots the page.
 *
 * Every field is checked, not just the message. A genuine same-origin error
 * always carries its `error` object, and an engine that mutes the message while
 * still supplying a filename or a line has given the developer something to work
 * with -- neither is ignorable. The predicate reads the fields off the event
 * rather than testing `instanceof ErrorEvent`, so a test double works.
 */
export function isMutedErrorEvent(event: Event): boolean {
  const { message, filename, lineno, colno, error } = event as Partial<ErrorEvent>
  return typeof message === 'string'
    && MUTED_ERROR_MESSAGE_RE.test(message)
    && (error === null || error === undefined)
    && (filename === undefined || filename === '')
    && zeroOrAbsent(lineno)
    && zeroOrAbsent(colno)
}

/**
 * Why the event is ignorable, or undefined when it is a real fault to surface.
 *
 * Takes an `Event` rather than an `ErrorEvent` because both callers listen on a
 * window, where a listener sees whatever the browser dispatches.
 */
export function ignorableErrorReason(event: Event): IgnorableErrorReason | undefined {
  if (isResizeObserverLoopError((event as Partial<ErrorEvent>).message))
    return 'resize-observer-loop'
  if (isMutedErrorEvent(event))
    return 'muted'
  return undefined
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
 * Rate limit for the suppressed-event debug line below, applied per reason. The
 * browser can emit the RO-loop error once per frame while a long transcript
 * settles, so an unthrottled line would just move the noise from the overlay to
 * the console; one line per window per reason (with a count of what it absorbed)
 * keeps the signal without the spam.
 */
const SUPPRESSED_DEBUG_LOG_INTERVAL_MS = 10_000

/** The running tally behind one reason's rate-limited debug line. */
interface SuppressedTally {
  count: number
  lastLoggedAt: number
}

/**
 * Install a capture-phase `error` listener that suppresses every ignorable
 * window error. Returns a disposer that removes the listener.
 *
 * Registered from `entry-client.tsx` BEFORE `mount()` runs, so it is added ahead
 * of the dev overlay's own window `error` listener (registered in a createEffect
 * during mount). At the target phase listeners fire in registration order, so
 * running first lets `stopImmediatePropagation()` keep the event from reaching
 * the overlay. No-op outside a DOM (SSR / non-browser), where `target` resolves
 * to undefined.
 *
 * `preventDefault()` additionally quiets the browser's own console report, so it
 * is applied to the RO loop ONLY. A muted error keeps its console line: the
 * browser sanitizes the event it hands to JS, but still reports the real
 * message, file and line to the console, which makes that line the one place the
 * cause survives. Silencing it would hide the very thing a developer attaches
 * Web Inspector to read.
 *
 * The browser emits the SAME RO message for the benign deferred-delivery case
 * and a genuine per-frame measure/write feedback loop, so full silence would
 * also mask a real regression in the deferred-mount machinery that avoids the
 * avoidable cases. A rate-limited `console.debug` per reason keeps that signal
 * observable (a healthy session logs it rarely; a feedback loop shows a rapidly
 * climbing suppressed count) without reviving the overlay or the per-event
 * console noise.
 */
export function installIgnorableErrorSuppressor(
  target: ErrorEventTarget | undefined = defaultTarget(),
): () => void {
  if (!target)
    return () => {}
  const tallies = new Map<IgnorableErrorReason, SuppressedTally>()
  const onError = (event: Event) => {
    const reason = ignorableErrorReason(event)
    if (reason === undefined)
      return
    event.stopImmediatePropagation()
    if (reason === 'resize-observer-loop')
      event.preventDefault()
    const tally = tallies.get(reason)
      ?? { count: 0, lastLoggedAt: Number.NEGATIVE_INFINITY }
    tally.count += 1
    tallies.set(reason, tally)
    const now = monotonicNow()
    if (now - tally.lastLoggedAt >= SUPPRESSED_DEBUG_LOG_INTERVAL_MS) {
      tally.lastLoggedAt = now
      // eslint-disable-next-line no-console -- deliberate dev-only diagnostic; the logger would re-enter the error path this suppressor guards
      console.debug(`[leapmux] suppressed ignorable window error: ${reason} (x${tally.count} this session)`)
    }
  }
  target.addEventListener('error', onError, true)
  return () => target.removeEventListener('error', onError, true)
}
