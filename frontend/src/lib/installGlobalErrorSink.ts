import { ignorableErrorReason, isResizeObserverLoopError } from './ignorableErrorEvents'
import { monotonicNow } from './monotonicNow'

/**
 * Surface faults that never reach an `ErrorBoundary`.
 *
 * Solid's boundaries catch what throws inside the render/reactive graph, and
 * nothing else. Event handlers, `.then`/`.catch`, `setTimeout` callbacks and
 * WebSocket handlers all bypass them, so until this existed an async failure was
 * invisible to the user AND to the developer in production -- the app simply did
 * not do the thing. That is not a hypothetical class: two live instances were
 * found in one review pass (a bare `navigator.clipboard.writeText` in the worker
 * context menu, and another in the selection popover that also aborted the three
 * statements after it).
 *
 * Deliberately a TOAST, not the full-screen fallback. A stray rejection -- from
 * a browser extension, a cancelled fetch, a third-party script -- must not
 * tombstone a working app; the boundaries stay the mechanism for "this subtree
 * cannot render". Reporting is left to the caller for the same reason the level
 * is not decided here: `showWarnToast` both toasts and logs at warn, so this
 * module must not log as well or every fault appears twice.
 *
 * Passive: it neither `preventDefault`s nor stops propagation, so the browser's
 * native reporting and the dev overlay still see everything.
 */

/** The subset of `Window` this needs, so tests can pass a double. */
export interface GlobalErrorTarget {
  addEventListener: (type: string, listener: (event: Event) => void) => void
  removeEventListener: (type: string, listener: (event: Event) => void) => void
}

export interface GlobalErrorSinkOpts {
  /**
   * Where a surfaced fault goes. The app passes `showWarnToast`, which toasts
   * AND logs at warn level -- this module deliberately does neither itself.
   *
   * Injected rather than imported so `lib/` keeps no dependency on a component,
   * and so the composition root stays the one place that decides how a fault is
   * presented.
   */
  report: (message: string, err: unknown) => void
  target?: GlobalErrorTarget
}

/**
 * How long the same message stays deduplicated.
 *
 * A failing handler on a hot path (a rejected send retried per keystroke) would
 * otherwise queue one toast per occurrence and bury the app under them. Repeats
 * inside the window are dropped entirely: the first one already told the user,
 * and `showWarnToast` already logged it.
 */
const DEDUPE_WINDOW_MS = 10_000

/**
 * Cap on remembered messages, so a fault whose text varies every time (a message
 * carrying a timestamp or a request id) cannot grow the map without bound.
 */
const MAX_TRACKED_MESSAGES = 32

const FALLBACK_MESSAGE = 'Something went wrong'

function defaultTarget(): GlobalErrorTarget | undefined {
  return typeof window === 'undefined' ? undefined : window
}

/** The thrown value behind either event shape, for `formatErrorMessage`. */
function causeOf(event: Event): unknown {
  if ('reason' in event)
    return (event as PromiseRejectionEvent).reason
  const errorEvent = event as ErrorEvent
  return errorEvent.error ?? errorEvent.message
}

/**
 * Install `error` + `unhandledrejection` listeners that report anything neither
 * boundary would have caught. Returns a disposer.
 *
 * No-op outside a DOM (SSR / non-browser), where `target` resolves to undefined.
 */
export function installGlobalErrorSink(opts: GlobalErrorSinkOpts): () => void {
  const target = opts.target ?? defaultTarget()
  if (!target)
    return () => {}

  const lastReportedAt = new Map<string, number>()

  const shouldReport = (key: string): boolean => {
    const now = monotonicNow()
    const previous = lastReportedAt.get(key)
    if (previous !== undefined && now - previous < DEDUPE_WINDOW_MS)
      return false
    // Re-inserting moves the key to the end of the Map's insertion order, so the
    // eviction below always drops the least recently reported one.
    lastReportedAt.delete(key)
    lastReportedAt.set(key, now)
    if (lastReportedAt.size > MAX_TRACKED_MESSAGES)
      lastReportedAt.delete(lastReportedAt.keys().next().value!)
    return true
  }

  const onFault = (event: Event) => {
    const cause = causeOf(event)
    // In dev the capture-phase suppressor stops these before they get here; in
    // prod it is not installed at all. Neither class is something to put in
    // front of the user: a self-healing browser delivery warning is not a
    // fault, and a muted error has already had every field the toast could
    // report stripped out of it, so it can only say "Something went wrong"
    // about an app that is working (iOS Safari does exactly that when the share
    // sheet resizes and snapshots the page). Diagnosis loses nothing -- this
    // sink is passive, so the browser still reports the unsanitized error to
    // the console. The first check covers a REJECTION whose reason is the RO
    // message, which has no `message` field for the second one to read.
    if (isResizeObserverLoopError(cause) || ignorableErrorReason(event) !== undefined)
      return
    if (shouldReport(cause instanceof Error ? cause.message : String(cause)))
      opts.report(FALLBACK_MESSAGE, cause)
  }

  target.addEventListener('error', onFault)
  target.addEventListener('unhandledrejection', onFault)
  return () => {
    target.removeEventListener('error', onFault)
    target.removeEventListener('unhandledrejection', onFault)
  }
}
