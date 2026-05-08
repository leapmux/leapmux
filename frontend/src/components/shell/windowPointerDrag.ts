import { onCleanup } from 'solid-js'
import { createRafCoalescer } from '~/lib/rafCoalesce'

export interface WindowPointerDragOptions {
  /** Called on every `pointermove` while the drag is active. */
  onMove: (e: PointerEvent) => void
  /**
   * Called once after `pointerup` (after listeners are removed) — but only
   * if at least one `pointermove` actually fired. A bare click on the drag
   * surface skips `onUp` so callers don't pay for re-persist/refocus side
   * effects on a no-op gesture.
   */
  onUp?: () => void
  /**
   * Called once after every natural drag end (`pointerup` or
   * `pointercancel`), regardless of whether a `pointermove` fired. Use for
   * cleanup that must run even on a bare click — cursor reset, dragging-
   * indicator clear, etc. Fires after `onUp` (when `onUp` runs at all).
   * Does NOT fire on `cancel()` or owner disposal: those are hard aborts
   * where the caller already abandoned the gesture.
   */
  onFinish?: () => void
  /**
   * When true, coalesce `pointermove` dispatches via
   * `requestAnimationFrame` — only the most recent event reaches `onMove`
   * per frame. A pending frame is flushed synchronously on `pointerup`
   * before `onUp` fires, so the final geometry is committed before any
   * `onUp` side effects (debounced persist, focus rebroadcast, etc.).
   *
   * Off by default to preserve the synchronous semantics expected by unit
   * tests that assert on per-event dispatch counts.
   */
  coalesce?: boolean
}

export interface WindowPointerDragController {
  /**
   * Begin a new drag. Any previously-active drag from this controller is
   * silently cancelled first (no `onUp` fires for it) so back-to-back
   * drag-starts from a single component never double up listeners.
   */
  start: (opts: WindowPointerDragOptions) => void
  /** Cancel the active drag without firing `onUp`. No-op if none is active. */
  cancel: () => void
}

/**
 * Manage a single document-level pointer drag at a time within a
 * component. Used by `FloatingWindowContainer`'s drag and resize handlers
 * — both want to register `pointermove`/`pointerup` on `document`, both
 * want only one drag active at any time, and both want automatic cleanup
 * if the component unmounts mid-gesture.
 *
 * Why document-level (not `setPointerCapture`): the drag handle is a
 * narrow strip and the user routinely moves the cursor outside it during
 * fast drags. Document-level listeners cover the strip + everything
 * beyond it.
 *
 * The controller does NOT own the start-event side effects (`preventDefault`,
 * `bringToFront`, captured geometry, etc.) — callers run those in their
 * own `pointerdown` handler before calling `start`.
 */
export function useWindowPointerDrag(): WindowPointerDragController {
  let active: { stop: () => void } | null = null

  const cancel = () => {
    active?.stop()
    active = null
  }

  onCleanup(cancel)

  const start = (opts: WindowPointerDragOptions): void => {
    // Tear down any previous drag from this controller before wiring up a
    // new one — a fresh `pointerdown` always supersedes whatever was in
    // flight.
    cancel()

    // Tracks whether at least one move dispatched during the gesture, so
    // `onUp` skips on bare clicks. Set when the move handler actually
    // delivers an event (after coalescing, if enabled).
    let moved = false

    const dispatchMove = (e: PointerEvent): void => {
      moved = true
      opts.onMove(e)
    }

    const coalescer = opts.coalesce ? createRafCoalescer(dispatchMove) : null

    const onMove: (e: PointerEvent) => void = coalescer
      ? e => coalescer.push(e)
      : dispatchMove

    function handleUp(): void {
      // Flush a queued frame before firing `onUp` so the final geometry
      // is committed first; otherwise debounced persist + onUp could
      // observe the next-to-last position.
      coalescer?.flush()
      document.removeEventListener('pointermove', onMove)
      document.removeEventListener('pointerup', handleUp)
      document.removeEventListener('pointercancel', handleUp)
      active = null
      if (moved)
        opts.onUp?.()
      opts.onFinish?.()
    }

    function stop(): void {
      // No flush on `stop`: cancel is a hard abort (component unmount,
      // superseding drag) and any in-flight `onMove` would write stale
      // geometry to a controller the caller already abandoned.
      coalescer?.abort()
      document.removeEventListener('pointermove', onMove)
      document.removeEventListener('pointerup', handleUp)
      document.removeEventListener('pointercancel', handleUp)
      active = null
    }

    // `passive: true` lets the browser skip the scroll-blocking probe per
    // event — the move handler updates store geometry but never calls
    // `preventDefault`, so the passive flag is safe.
    document.addEventListener('pointermove', onMove, { passive: true })
    document.addEventListener('pointerup', handleUp)
    // Treat pointercancel as a natural end-of-drag (same as
    // `startPairRebalanceDrag`): the geometry written during the gesture
    // is a reasonable resting position, and routing through `handleUp`
    // keeps the listener-cleanup path single-source.
    document.addEventListener('pointercancel', handleUp)
    active = { stop }
  }

  return { start, cancel }
}
