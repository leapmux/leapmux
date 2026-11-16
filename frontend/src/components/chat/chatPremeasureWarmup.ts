import type { Accessor } from 'solid-js'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { ChatRowItem } from './chatRowItem'
import type { ChatVirtualizerRange } from './useChatVirtualizer'
import { createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { cancelIdle, requestIdle } from '~/lib/idleCallback'

/**
 * Idle-time premeasure warm-up.
 *
 * The target and look-ahead staging bands only cover rows near the view and
 * near a place that waits, so a long fling still outruns them: it reaches the
 * end of the measured rows and waits there. This unit drains the REST of the
 * loaded rows during idle: while the user isn't scrolling (the pane is visible
 * and sized, and the pause that each scroll opens for the syntax highlighter is
 * over), it feeds small
 * batches of unmeasured rows — nearest to the rendered rows first — into the
 * premeasure queue as a third candidate band. After a few idle seconds every
 * loaded row is measured, and a scroll anywhere in the window waits for nothing.
 *
 * Yielding model: each tick runs in an idle period of the browser, and mounts
 * its rows one at a time while the period lasts, so a batch of heavy rows does
 * not become one long task. A tick always holds at least one row, or a page
 * with short idle periods would never warm up. The `enabled` condition drops
 * the band to empty when the user scrolls, so the warm-up adds no row during
 * the gesture.
 *
 * Emptying the band unmounts nothing. The premeasure queue keeps each row that
 * a band added pending until the row measures or leaves the window, so a row
 * that waits for its content (a Markdown body or an edit diff in a worker)
 * stays mounted, and its measure commits at once, during a gesture too: the
 * layout shift keeps the view in place.
 */

/** The most rows one tick mounts: one shared premeasure frame's worth. */
export const WARMUP_BATCH_ROWS = 8
/** Quiet time required before the first warm-up batch (and after re-enable). */
export const WARMUP_IDLE_DELAY_MS = 800
/** Spacing between a tick and the request for the next idle period, while unmeasured rows remain. */
export const WARMUP_TICK_MS = 300

export interface PremeasureWarmupDeps {
  /**
   * Master condition: pane visible, sized, and without active scroll work.
   * While false the band is empty and the ticker is parked.
   */
  enabled: Accessor<boolean>
  /** The loaded rows in display order, each entry with its item (see createChatPremeasureBands). */
  rows: Accessor<readonly ChatDomPremeasureCandidate[]>
  /**
   * The loaded rows that the warm-up skips: the rendered range and the
   * look-ahead band around it. The warm-up starts beyond them. Indexes of
   * `rows`, inside `[0, rows().length]`.
   */
  skip: Accessor<ChatVirtualizerRange>
  /** Whether the row is measured (see RowHeightCache.isMeasured). */
  isMeasured: (item: ChatRowItem) => boolean
}

const EMPTY: ChatDomPremeasureCandidate[] = []

/**
 * Create the warm-up ticker. Call within a reactive owner. The returned
 * accessor is wired into the premeasure queue's `warmupCandidates` band.
 */
export function createPremeasureWarmup(deps: PremeasureWarmupDeps) {
  const [warmupCandidates, setWarmupCandidates] = createSignal<readonly ChatDomPremeasureCandidate[]>(EMPTY)
  // Cancels the stage of the next tick that is waiting now: its delay timer, or
  // its request for an idle period. Undefined while no tick is scheduled.
  let cancelPendingTick: (() => void) | undefined

  const cancelScheduledTick = (): void => {
    cancelPendingTick?.()
    cancelPendingTick = undefined
  }

  /**
   * The next batch: unmeasured rows outside the already-covered band,
   * walking outward from its edges and alternating above/below so both
   * scroll directions warm evenly, nearest-first.
   */
  const computeNextBatch = (): ChatDomPremeasureCandidate[] => {
    const rows = deps.rows()
    const len = rows.length
    if (len === 0)
      return EMPTY
    const skip = deps.skip()
    const out: ChatDomPremeasureCandidate[] = []
    const maybePush = (index: number): void => {
      const row = rows[index]
      if (row !== undefined && !deps.isMeasured(row.item))
        out.push(row)
    }
    let above = skip.start - 1
    let below = skip.end
    while (out.length < WARMUP_BATCH_ROWS && (above >= 0 || below < len)) {
      if (above >= 0) {
        maybePush(above)
        above -= 1
      }
      if (out.length < WARMUP_BATCH_ROWS && below < len) {
        maybePush(below)
        below += 1
      }
    }
    return out.length === 0
      ? EMPTY
      : out
  }

  /**
   * Wait `delayMs`, then run the next tick in the next idle period of the browser.
   * Replaces the tick that is scheduled already, so at most one tick waits.
   */
  const scheduleTick = (delayMs: number): void => {
    cancelScheduledTick()
    const timer = setTimeout(() => {
      const idleHandle = requestIdle(tick)
      cancelPendingTick = () => cancelIdle(idleHandle)
    }, delayMs)
    cancelPendingTick = () => clearTimeout(timer)
  }

  function tick(deadline: IdleDeadline): void {
    cancelPendingTick = undefined
    if (!untrack(deps.enabled))
      return
    const next = untrack(computeNextBatch)
    if (next.length === 0) {
      // Nothing left to measure: park until the arming effect re-fires
      // (the loaded rows change, re-enable).
      setWarmupCandidates(prev => (prev.length === 0
        ? prev
        : EMPTY))
      return
    }
    const batch: ChatDomPremeasureCandidate[] = []
    for (const candidate of next) {
      if (batch.length > 0 && deadline.timeRemaining() <= 0)
        break
      batch.push(candidate)
      // Each publish mounts this row now, so the deadline check above sees its
      // cost. The premeasure queue mounts only the rows that it does not hold
      // yet: a row that an earlier tick added, and that still waits for its
      // measure, stays mounted.
      setWarmupCandidates(batch.slice())
    }
    scheduleTick(WARMUP_TICK_MS)
  }

  createEffect(() => {
    if (!deps.enabled()) {
      // Scroll, stream or hidden pane: add no more rows. The rows that the band
      // already added stay mounted until they measure (see the header).
      cancelScheduledTick()
      setWarmupCandidates(EMPTY)
      return
    }
    // Track the loaded rows, so newly loaded (paginated/streamed) rows restart
    // a parked ticker. A row that gets a new object (a new entry or a new
    // height key) restarts the ticker too.
    deps.rows()
    scheduleTick(WARMUP_IDLE_DELAY_MS)
  })

  onCleanup(cancelScheduledTick)

  return { warmupCandidates }
}
