import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { ChatVirtualizerRange, VirtualItem } from './useChatVirtualizer'
import { createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { cancelIdle, requestIdle } from '~/lib/idleCallback'

/**
 * Idle-time premeasure warm-up.
 *
 * The ranged and look-ahead premeasure bands only cover rows near the
 * viewport, so a long fling still outruns them into estimated (unmeasured)
 * territory — the estimate->real corrections there are what shift the
 * offset map mid-scroll. This unit drains the REST of the window during
 * idle: while the user isn't scrolling (and the pane is visible, sized, and
 * no syntax-highlight work is active), it feeds small batches of unmeasured rows — nearest to
 * the viewport first — into the premeasure queue as a third candidate band.
 * After a few idle seconds every loaded row has real geometry and scrolling
 * anywhere in the window is measurement-free.
 *
 * Yielding model: each tick runs in an idle period of the browser, and mounts
 * its rows one at a time while the period lasts, so a batch of heavy rows does
 * not become one long task. A tick always holds at least one row, or a page
 * with short idle periods would never warm up. The `enabled` condition drops
 * the band to empty when the user scrolls. Active hidden rows then unmount and
 * their premeasure frame entries cancel.
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
  visibleEntries: Accessor<readonly ClassifiedEntry[]>
  virtualItems: Accessor<readonly VirtualItem[]>
  range: Accessor<ChatVirtualizerRange>
  hasMeasuredHeight: (id: string) => boolean
  /**
   * Half-width (rows) of the band the look-ahead premeasure already covers
   * around the range — warm-up starts beyond it (LOOKAHEAD_PREMEASURE_ROWS).
   */
  excludedBandRows: number
}

const EMPTY: ChatDomPremeasureCandidate[] = []

function sameCandidateIds(a: readonly ChatDomPremeasureCandidate[], b: readonly ChatDomPremeasureCandidate[]): boolean {
  if (a.length !== b.length)
    return false
  for (let i = 0; i < a.length; i++) {
    // The length check above keeps `i` in range on both; the undefined checks are the type-level guard alone.
    const x = a[i]
    const y = b[i]
    if (x === undefined || y === undefined || x.item.id !== y.item.id || x.item.heightKey !== y.item.heightKey)
      return false
  }
  return true
}

/**
 * Create the warm-up ticker. Call within a reactive owner. The returned
 * accessor is wired into the premeasure queue's `warmupCandidates` band
 * (pending-only — warm-up rows are never in the main <For>, so they need no
 * collapse entry, exactly like look-ahead rows).
 */
export function createPremeasureWarmup(deps: PremeasureWarmupDeps) {
  const [warmupCandidates, setWarmupCandidates] = createSignal<readonly ChatDomPremeasureCandidate[]>(EMPTY)
  let timer: ReturnType<typeof setTimeout> | undefined
  let idleHandle: number | undefined

  const cancelTimer = (): void => {
    if (timer !== undefined) {
      clearTimeout(timer)
      timer = undefined
    }
    if (idleHandle !== undefined) {
      cancelIdle(idleHandle)
      idleHandle = undefined
    }
  }

  /**
   * The next batch: unmeasured rows outside the already-covered band,
   * walking outward from its edges and alternating above/below so both
   * scroll directions warm evenly, nearest-first.
   */
  const computeNextBatch = (): ChatDomPremeasureCandidate[] => {
    const all = deps.visibleEntries()
    const items = deps.virtualItems()
    const len = Math.min(all.length, items.length)
    if (len === 0)
      return EMPTY
    const r = deps.range()
    const bandLo = Math.max(0, r.start - deps.excludedBandRows)
    const bandHi = Math.min(len, r.end + deps.excludedBandRows)
    const out: ChatDomPremeasureCandidate[] = []
    const maybePush = (index: number): void => {
      const entry = all[index]
      const item = items[index]
      if (entry && item && !deps.hasMeasuredHeight(item.id))
        out.push({ entry, item })
    }
    let above = bandLo - 1
    let below = bandHi
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

  /** Wait `delayMs`, then run the next tick in the next idle period of the browser. */
  const scheduleTick = (delayMs: number): void => {
    timer = setTimeout(() => {
      timer = undefined
      idleHandle = requestIdle(tick)
    }, delayMs)
  }

  function tick(deadline: IdleDeadline): void {
    idleHandle = undefined
    if (!untrack(deps.enabled))
      return
    const next = untrack(computeNextBatch)
    if (next.length === 0) {
      // Nothing left to measure: park until the arming effect re-fires
      // (items change, re-enable).
      setWarmupCandidates(prev => (prev.length === 0
        ? prev
        : EMPTY))
      return
    }
    // A row of the last batch that still waits for its measure keeps its
    // candidate object, so it stays mounted rather than mounting again.
    const mounted = new Map(untrack(warmupCandidates).map(candidate => [candidate.item.id, candidate]))
    const batch: ChatDomPremeasureCandidate[] = []
    for (const candidate of next) {
      const kept = mounted.get(candidate.item.id)
      if (kept !== undefined && kept.item.heightKey === candidate.item.heightKey) {
        batch.push(kept)
        continue
      }
      if (batch.length > 0 && deadline.timeRemaining() <= 0)
        break
      batch.push(candidate)
      // Mounts this row now, so the deadline check above sees its cost.
      setWarmupCandidates(batch.slice())
    }
    setWarmupCandidates(prev => (sameCandidateIds(prev, batch)
      ? prev
      : batch))
    scheduleTick(WARMUP_TICK_MS)
  }

  createEffect(() => {
    if (!deps.enabled()) {
      // Scroll/stream/hidden: stop feeding immediately so the hidden rows
      // unmount and no new measures land mid-gesture.
      cancelTimer()
      setWarmupCandidates(EMPTY)
      return
    }
    // Track the item list so newly loaded (paginated/streamed) rows restart
    // a parked ticker; read length only — the arming decision doesn't care
    // about content.
    deps.virtualItems()
    cancelTimer()
    scheduleTick(WARMUP_IDLE_DELAY_MS)
  })

  onCleanup(cancelTimer)

  return { warmupCandidates }
}
