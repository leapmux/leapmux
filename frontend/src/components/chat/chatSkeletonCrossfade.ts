import type { Accessor } from 'solid-js'
import type { UseChatVirtualizerResult } from './useChatVirtualizer'
import { createEffect, createSignal, onCleanup, untrack } from 'solid-js'

// ---------------------------------------------------------------------------
// Skeleton-crossfade orchestration
//
// Two independent skeleton lifecycles feed the chat's loading UI, extracted
// here from ChatView so each has a named, unit-testable home instead of living
// loose (timer maps, diffing effects, per-row state machines) in the ~1100-line
// component:
//
//  - createLingerSet: the PREMEASURE-hidden crossfade. When a row leaves
//    awaiting-measurement (its height committed, the real row fades in) its
//    loading-skeleton overlay lingers one beat in a fading-out wrapper instead
//    of popping away.
//  - createRowUpgradePhase: the FLING upgrade. A measured row entering during a
//    fast scroll mounts as an in-row skeleton, then upgrades skeleton ->
//    crossfade -> real once the scroll settles.
//
// Both are pure timer/signal state machines with no DOM of their own; ChatView
// renders the skeletons from the accessors they return.
// ---------------------------------------------------------------------------

/**
 * A reactive set of ids that LINGER for `lingerMs` after they leave `activeIds`,
 * then drop out; an id re-entering `activeIds` cancels its linger immediately.
 *
 * This is the state machine behind the loading-skeleton crossfade: a row leaving
 * awaiting-measurement keeps its skeleton for one fade-out beat, and a row that
 * re-enters awaiting (heightKey churn) cancels the linger because the live
 * overlay covers it again. The timer bookkeeping -- start-on-leave,
 * cancel-on-re-enter, clear-all-on-cleanup -- lives here rather than in the
 * component so it can be tested in isolation.
 *
 * Call within a reactive owner: it creates an effect, per-id timers, and an
 * onCleanup that clears them.
 */
export function createLingerSet(
  activeIds: Accessor<Iterable<string>>,
  lingerMs: number,
): { lingeringIds: Accessor<ReadonlySet<string>> } {
  const [lingeringIds, setLingeringIds] = createSignal<ReadonlySet<string>>(new Set())
  const timers = new Map<string, ReturnType<typeof setTimeout>>()

  const removeFromLingering = (id: string): void => {
    setLingeringIds((prev) => {
      if (!prev.has(id))
        return prev
      const next = new Set(prev)
      next.delete(id)
      return next
    })
  }
  const cancelLinger = (id: string): void => {
    const timer = timers.get(id)
    if (timer === undefined)
      return
    clearTimeout(timer)
    timers.delete(id)
    removeFromLingering(id)
  }

  let previous: ReadonlySet<string> = new Set()
  createEffect(() => {
    const current = new Set(activeIds())
    // Ids that just LEFT the active set (and aren't already lingering) start a
    // linger timer: the id fades out for `lingerMs`, then drops.
    for (const id of previous) {
      if (current.has(id) || timers.has(id))
        continue
      setLingeringIds(prev => new Set(prev).add(id))
      timers.set(id, setTimeout(() => {
        timers.delete(id)
        removeFromLingering(id)
      }, lingerMs))
    }
    // Ids that (re-)entered the active set cancel any pending linger.
    for (const id of current)
      cancelLinger(id)
    previous = current
  })

  onCleanup(() => {
    for (const timer of timers.values())
      clearTimeout(timer)
    timers.clear()
  })

  return { lingeringIds }
}

/** The one-way fling-skeleton upgrade phase of a single row. */
export type RowUpgradePhase = 'skeleton' | 'crossfade' | 'real'

/**
 * Per-row fling-skeleton upgrade phase. A MEASURED row entering the window
 * during a fast user scroll (momentum fling or a fast scrollbar/touch drag)
 * mounts as a skeleton instead of paying full bubble construction on the
 * scroll-critical path, then upgrades in place once the scroll settles.
 *
 * The initial phase is decided ONCE at row creation -- an already-real row never
 * downgrades (that would tear its DOM down mid-scroll), and only a measured row
 * can start as a skeleton (an unmeasured row must mount for real so measurement
 * / hide-until-measured proceed). The upgrade passes through a one-way
 * 'crossfade' beat (skeleton -> crossfade -> real): the real bubble mounts
 * beneath a fading-out skeleton copy so the swap never pops.
 *
 * Call within the row's reactive owner (the <For> callback): it allocates a
 * signal, a one-shot effect, and an onCleanup for the crossfade timer.
 */
export function createRowUpgradePhase(
  virt: Pick<UseChatVirtualizerResult, 'fastScrollActive' | 'hasMeasuredHeight'>,
  id: string,
  crossfadeMs: number,
): Accessor<RowUpgradePhase> {
  const skeletonAtMount = untrack(() => virt.fastScrollActive() && virt.hasMeasuredHeight(id))
  const [phase, setPhase] = createSignal<RowUpgradePhase>(skeletonAtMount ? 'skeleton' : 'real')
  if (skeletonAtMount) {
    // The crossfade -> real timer lives at the ROW-OWNER scope, not inside the effect's
    // cleanup. The effect tracks fastScrollActive, so a NEW fling arriving during the
    // crossfade beat re-runs it; an in-effect onCleanup would then fire on that re-run and
    // clear the timer, but the body's guard (phase !== 'skeleton') no longer re-arms it --
    // stranding the row at 'crossfade' forever (its real bubble is already mounted, but a
    // dead skeleton-copy overlay lingers, never fading out). Owner-scope cleanup fires only
    // on row unmount, so the one-way skeleton -> crossfade -> real always completes,
    // regardless of later scroll activity.
    let crossfadeTimer: ReturnType<typeof setTimeout> | undefined
    createEffect(() => {
      // One-way: skeleton -> crossfade -> real, never back. Once the crossfade begins the
      // phase leaves 'skeleton', so a re-run for a later fling is a no-op.
      if (!virt.fastScrollActive() && untrack(phase) === 'skeleton') {
        setPhase('crossfade')
        crossfadeTimer = setTimeout(setPhase, crossfadeMs, 'real')
      }
    })
    onCleanup(() => {
      if (crossfadeTimer !== undefined)
        clearTimeout(crossfadeTimer)
    })
  }
  return phase
}

export interface FlingSkeletonRegistry {
  /**
   * Create (and register) the fling-skeleton upgrade phase for one row. Call within the
   * ROW's reactive owner (the <For> callback): it wires createRowUpgradePhase plus an
   * effect that keeps `skeletonIds` in sync with the phase and an onCleanup that drops the
   * row on unmount. Returns the phase accessor for the row to render from.
   */
  trackRow: (id: string) => Accessor<RowUpgradePhase>
  /**
   * The ids of rows CURRENTLY painting a fling skeleton (phase === 'skeleton'). Read by
   * overlays rendered OUTSIDE the rows -- above all the span-line gap bridges, which must
   * hide a bridge whose row shows a skeleton (no span column) instead of its real content,
   * so the rail segment doesn't dangle over the placeholder until the real row upgrades in.
   */
  skeletonIds: Accessor<ReadonlySet<string>>
}

/**
 * A registry over the per-row fling-skeleton phases that also exposes, as one reactive set,
 * which rows are currently skeletons. Rows render their own phase in place, but the gap-bridge
 * overlay lives OUTSIDE the rows and can't see a row's local phase; this collects those phases
 * into `skeletonIds` so the overlay can react. Create once per list (closes over `virt` and the
 * crossfade duration); call `trackRow` per rendered row.
 */
export function createFlingSkeletonRegistry(
  virt: Pick<UseChatVirtualizerResult, 'fastScrollActive' | 'hasMeasuredHeight'>,
  crossfadeMs: number,
): FlingSkeletonRegistry {
  const [skeletonIds, setSkeletonIds] = createSignal<ReadonlySet<string>>(new Set())
  // Toggle membership without churning the signal when it wouldn't change (most rows are
  // never skeletons, so their one effect run is a no-op).
  const setMembership = (id: string, present: boolean): void => {
    setSkeletonIds((prev) => {
      if (prev.has(id) === present)
        return prev
      const next = new Set(prev)
      if (present)
        next.add(id)
      else
        next.delete(id)
      return next
    })
  }
  const trackRow = (id: string): Accessor<RowUpgradePhase> => {
    const phase = createRowUpgradePhase(virt, id, crossfadeMs)
    createEffect(() => setMembership(id, phase() === 'skeleton'))
    onCleanup(() => setMembership(id, false))
    return phase
  }
  return { trackRow, skeletonIds }
}
