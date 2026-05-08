import type { DragTeardownHandle } from './dragTeardown'
import type { GridAxis } from '~/stores/layout.store'
import { rebalancePair } from '~/lib/pairDrag'
import { createRafCoalescer } from '~/lib/rafCoalesce'
import { MIN_SPLIT_RATIO } from '~/stores/layout.store'

// Shared pair-rebalance drag helper used by both `SplitRenderer` and
// `GridRenderer` in `TilingLayout.tsx`.
//
// Why not setPointerCapture: window-level capture-phase listeners survive
// fast drags that exit the 4px handle and tolerate Playwright's
// mouse-only event synthesis (mouse fallback below).
// Why pointercancel still commits: it's rare, the live-preview ratios are
// a reasonable resting position, and one finalize path keeps the helper
// idempotent.
// Why an external teardown that does NOT commit: callers (onCleanup,
// structural-change effects) need to abort cleanly when the drag's
// premise becomes invalid.

export interface PairDragOptions {
  axis: GridAxis
  /** Separator index — rebalances `startRatios[index]` against `[index + 1]`. */
  index: number
  /** The pointerdown event from the handle; its `currentTarget` is the handle. */
  startEvent: PointerEvent
  /** Container whose bounding rect provides the drag-axis denominator. */
  containerRef: HTMLElement
  /** Snapshot of the ratios at drag start; full array, not just the pair. */
  startRatios: readonly number[]
  /** Live-preview emitter; receives the rebalanced ratios on each move. */
  setDragRatios: (next: number[] | null) => void
  /** Called once on internal finalize (pointerup / pointercancel) if the user moved. */
  commit: (final: number[]) => void
  /** Fires once after any finalize path so the renderer can clear its `activeTeardown` ref. */
  onDone?: () => void
  /**
   * When true, coalesce pointer/mouse moves via `requestAnimationFrame`:
   * only the latest event in a frame reaches `setDragRatios`. The pending
   * frame is flushed synchronously on pointerup so `commit` sees the
   * final geometry. Off by default to preserve the synchronous semantics
   * unit tests rely on; production call sites (`startManagedAxisDrag`)
   * opt in.
   */
  coalesce?: boolean
}

/**
 * Returns `null` when pre-flight guards fail (no listeners attached, no
 * state mutated). Otherwise returns a teardown that aborts without
 * committing — used by `onCleanup` and structural-change effects.
 * Internal pointerup/pointercancel commit through the same path; the
 * teardown is idempotent against that.
 */
export function startPairRebalanceDrag(opts: PairDragOptions): (() => void) | null {
  const { axis, index, startEvent, containerRef, startRatios, setDragRatios, commit, onDone, coalesce } = opts

  const rect = containerRef.getBoundingClientRect()
  const total = axis === 'col' ? rect.width : rect.height
  if (total <= 0)
    return null

  const a = startRatios[index]
  const b = startRatios[index + 1]
  if (a == null || b == null)
    return null
  const sumPair = a + b
  // Guard against persisted-state drift: if the existing pair sum is
  // already below 2× the floor, the clamp below cannot produce valid
  // ratios.
  if (sumPair < MIN_SPLIT_RATIO * 2)
    return null

  startEvent.preventDefault()
  startEvent.stopPropagation()

  const handle = startEvent.currentTarget as HTMLElement
  const startCoord = axis === 'col' ? startEvent.clientX : startEvent.clientY
  const pointerId = startEvent.pointerId
  handle.dataset.dragging = ''

  let latestRatios: number[] | null = null
  let finalized = false
  // Browsers fire both `pointermove` and a legacy `mousemove` per mouse
  // tick. Once we've seen a real pointer event, ignore the mouse-event
  // mirror so the rebalance + setDragRatios cost runs once per tick. The
  // mouse listener is only there for automation tools (Playwright) that
  // synthesise mouse events without a pointer counterpart.
  let sawPointerEvent = false

  const dispatchMove = (ev: PointerEvent | MouseEvent) => {
    const cur = axis === 'col' ? ev.clientX : ev.clientY
    const deltaRatio = (cur - startCoord) / total
    const [newA, newB] = rebalancePair(a, sumPair, deltaRatio, MIN_SPLIT_RATIO)
    const next = [...startRatios]
    next[index] = newA
    next[index + 1] = newB
    latestRatios = next
    setDragRatios(next)
  }

  // rAF coalescing — when `coalesce` is on, hold the latest move event
  // for the next animation frame so a 120Hz trackpad doesn't trigger two
  // rebalance + setDragRatios writes per paint.
  const coalescer = coalesce ? createRafCoalescer<PointerEvent | MouseEvent>(dispatchMove) : null

  const onMove = (ev: PointerEvent | MouseEvent) => {
    if (ev instanceof PointerEvent) {
      if (ev.pointerId !== pointerId)
        return
      sawPointerEvent = true
    }
    else if (sawPointerEvent) {
      return
    }
    if (coalescer) {
      coalescer.push(ev)
      return
    }
    dispatchMove(ev)
  }

  // `passive: true` lets the browser skip the scroll-blocking probe per
  // event — the move handler updates a Solid signal but never calls
  // `preventDefault`, so the passive flag is safe.
  const moveOpts = { passive: true, capture: true } as const
  const upOpts = { capture: true } as const
  const removeListeners = () => {
    window.removeEventListener('pointermove', onMove, moveOpts)
    window.removeEventListener('pointerup', onUp, upOpts)
    window.removeEventListener('pointercancel', onUp, upOpts)
    window.removeEventListener('mousemove', onMove, moveOpts)
    window.removeEventListener('mouseup', onUp, upOpts)
  }

  const finalize = (commitFinal: boolean) => {
    if (finalized)
      return
    finalized = true
    removeListeners()
    // Flush a queued move so `commit` sees the final geometry; on a hard
    // abort (teardown), discard the pending event instead.
    if (commitFinal)
      coalescer?.flush()
    else
      coalescer?.abort()
    delete handle.dataset.dragging
    setDragRatios(null)
    if (commitFinal && latestRatios !== null)
      commit(latestRatios)
    onDone?.()
  }

  function onUp(ev: PointerEvent | MouseEvent) {
    if (ev instanceof PointerEvent && ev.pointerId !== pointerId)
      return
    finalize(true)
  }

  window.addEventListener('pointermove', onMove, moveOpts)
  window.addEventListener('pointerup', onUp, upOpts)
  window.addEventListener('pointercancel', onUp, upOpts)
  // Some automation tools (Playwright's mouse API) dispatch only mouse
  // events without matching pointer events; mirror them. The
  // `sawPointerEvent` guard inside `onMove` prevents native double-fire
  // when both stacks are live.
  window.addEventListener('mousemove', onMove, moveOpts)
  window.addEventListener('mouseup', onUp, upOpts)

  return () => finalize(false)
}

/** Per-axis runtime config supplied by SplitRenderer/GridRenderer. */
export interface AxisDragConfig {
  startRatios: readonly number[]
  setDragRatios: (next: number[] | null) => void
  commit: (final: number[]) => void
}

/**
 * Wire an axis-aware pair-rebalance drag against the given container and
 * teardown handle. Returns `null` (no drag started) when the container is
 * unmounted or the resolver opts out (callback returned `null`); otherwise
 * starts a managed drag and tracks its teardown.
 *
 * Both `SplitRenderer` (single axis derived from `direction`) and
 * `GridRenderer` (axis chosen per separator) feed the resolver — it owns
 * the per-axis ratios + commit closure.
 */
export function startManagedAxisDrag(
  axis: GridAxis,
  index: number,
  startEvent: PointerEvent,
  containerRef: HTMLElement | undefined,
  dragTeardown: DragTeardownHandle,
  resolve: (axis: GridAxis) => AxisDragConfig | null,
): void {
  if (!containerRef)
    return
  const cfg = resolve(axis)
  if (!cfg)
    return
  dragTeardown.set(startPairRebalanceDrag({
    axis,
    index,
    startEvent,
    containerRef,
    startRatios: cfg.startRatios,
    setDragRatios: cfg.setDragRatios,
    commit: cfg.commit,
    onDone: () => dragTeardown.set(null),
    coalesce: true,
  }))
}
