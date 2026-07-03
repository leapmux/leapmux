import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { ChatVirtualizerRange, VirtualItem } from './useChatVirtualizer'
import { createEffect, createSignal, onCleanup, untrack } from 'solid-js'

/**
 * Idle-time premeasure warm-up.
 *
 * The ranged and look-ahead premeasure bands only cover rows near the
 * viewport, so a long fling still outruns them into estimated (unmeasured)
 * territory — the estimate->real corrections there are what shift the
 * offset map mid-scroll. This unit drains the REST of the window during
 * idle: while the user isn't scrolling (and the pane is visible, sized, and
 * not streaming), it feeds small batches of unmeasured rows — nearest to
 * the viewport first — into the premeasure queue as a third candidate band.
 * After a few idle seconds every loaded row has real geometry and scrolling
 * anywhere in the window is measurement-free.
 *
 * Yielding model: batches are small (WARMUP_BATCH_ROWS per tick) so each
 * hidden render+measure fits comfortably in a frame's slack, and the
 * `enabled` gate drops the band to empty the moment the user scrolls or a
 * stream starts — in-flight hidden rows unmount and their premeasure frame
 * entries cancel.
 */

/** Rows premeasured per idle tick — one shared premeasure frame's worth. */
export const WARMUP_BATCH_ROWS = 8
/** Quiet time required before the first warm-up batch (and after re-enable). */
export const WARMUP_IDLE_DELAY_MS = 800
/** Spacing between batches while unmeasured rows remain. */
export const WARMUP_TICK_MS = 300

export interface PremeasureWarmupDeps {
  /**
   * Master gate: pane visible + sized, no active scroll, no live stream.
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
    if (a[i].item.id !== b[i].item.id || a[i].item.heightKey !== b[i].item.heightKey)
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

  const cancelTimer = (): void => {
    if (timer !== undefined) {
      clearTimeout(timer)
      timer = undefined
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
    return out.length === 0 ? EMPTY : out
  }

  const tick = (): void => {
    timer = undefined
    if (!untrack(deps.enabled))
      return
    const batch = untrack(computeNextBatch)
    setWarmupCandidates(prev => (sameCandidateIds(prev, batch) ? prev : batch))
    // Keep ticking while there is work; an empty scan parks the ticker until
    // the arming effect re-fires (items change, re-enable).
    if (batch.length > 0)
      timer = setTimeout(tick, WARMUP_TICK_MS)
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
    timer = setTimeout(tick, WARMUP_IDLE_DELAY_MS)
  })

  onCleanup(cancelTimer)

  return { warmupCandidates }
}
