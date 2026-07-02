import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { ChatVirtualizerRange, UseChatVirtualizerResult, VirtualItem } from './useChatVirtualizer'
import { createMemo } from 'solid-js'
import { createPremeasureQueue } from './chatPremeasureQueue'
import { createPremeasureWarmup } from './chatPremeasureWarmup'

// ---------------------------------------------------------------------------
// Premeasure-band orchestration
//
// Hidden DOM premeasurement feeds the virtualizer real row heights before rows scroll
// into view, so a tall unmeasured row can't paint past its estimated slot and overlap
// the next one. Three candidate bands supply the rows to premeasure, and a coherence
// queue de-dupes / collapses / settles them. This facade wires them together, extracted
// from ChatView so the band math and sub-unit wiring have a named, testable home instead
// of ~90 lines threaded through the ~1100-line component:
//
//  - RANGED band: every unmeasured row the virtualizer currently renders.
//  - LOOK-AHEAD band: unmeasured rows just OUTSIDE the rendered range (within
//    LOOKAHEAD_PREMEASURE_ROWS each side), so a row has real height before it enters
//    the window. Skips the ranged sub-range so the two never double-cover a row.
//  - WARM-UP band: an idle drain of the rest of the window's unmeasured rows so a
//    later fling anywhere lands on real geometry (createPremeasureWarmup).
//
// createPremeasureQueue owns the pending/collapsed/unsettled coherence over those bands;
// this facade supplies the candidates + the id lookups it needs and re-exports the
// queue's outputs. ChatView renders ChatHiddenPremeasure from premeasureCandidates and
// hides in-range unmeasured rows via collapsedPremeasureIds.
// ---------------------------------------------------------------------------

// Rows to premeasure BEYOND the rendered range in each direction, so a row has its real
// height before it scrolls into the anchor's neighborhood -- avoiding the estimate->measured
// correction (and the blank-gap flash) at the moment it enters the window. Kept modest: the
// whole band premeasures in one frame (measurement isn't chunked), so this bounds the burst.
// A fast fling that outruns it falls back to the estimate slot (still no collapse-to-0).
export const LOOKAHEAD_PREMEASURE_ROWS = 12

/**
 * Clamp the virtualizer's rendered range to the common length of the entry / item arrays.
 * Pure index math shared by both bands so the look-ahead band's skipped sub-range can never
 * drift from the in-range band's coverage. `len` is the common array length; `[start, end)`
 * is the range clamped into `[0, len]` with `start <= end`.
 */
export function clampPremeasureRange(
  allLen: number,
  itemsLen: number,
  range: ChatVirtualizerRange,
): { len: number, start: number, end: number } {
  const len = Math.min(allLen, itemsLen)
  const start = Math.max(0, Math.min(range.start, len))
  const end = Math.max(start, Math.min(range.end, len))
  return { len, start, end }
}

/**
 * Collect the unmeasured premeasure candidates over the half-open index range `[from, to)`,
 * skipping the inner `[skipFrom, skipTo)` sub-range (the rows another band already covers;
 * default empty). The single home for the "row is present AND still unmeasured" predicate and
 * the collection loop the two bands share, so they can't drift on what counts as premeasurable.
 * Pure given `all` / `items` / `hasMeasuredHeight`.
 */
export function collectUnmeasuredCandidates(
  all: readonly ClassifiedEntry[],
  items: readonly VirtualItem[],
  hasMeasuredHeight: (id: string) => boolean,
  from: number,
  to: number,
  skipFrom = to,
  skipTo = to,
): ChatDomPremeasureCandidate[] {
  const candidates: ChatDomPremeasureCandidate[] = []
  for (let index = from; index < to; index++) {
    if (index >= skipFrom && index < skipTo)
      continue
    const entry = all[index]
    const item = items[index]
    if (entry && item && !hasMeasuredHeight(item.id))
      candidates.push({ entry, item })
  }
  return candidates
}

export interface ChatPremeasureBandsDeps {
  /** The whole window's classified entries (chatEntryCache walks the full window, incl. off-range rows). */
  visibleEntries: Accessor<readonly ClassifiedEntry[]>
  /** The per-row virtual descriptors, index-aligned with visibleEntries. */
  virtualItems: Accessor<readonly VirtualItem[]>
  /** The virtualizer geometry surface the bands, warm-up, and queue consult. */
  virt: Pick<UseChatVirtualizerResult, 'range' | 'hasMeasuredHeight' | 'hasPendingPremeasuredHeight' | 'primeHeight'>
  /** Effective inner content width; the bands stay empty until it is measured (> 0). */
  contentWidth: Accessor<number>
  /** id -> entry for the visible window (built once in ChatView; shared with its crossfade slice). */
  visibleEntryById: Accessor<ReadonlyMap<string, ClassifiedEntry>>
  /** The live-tail id the queue must never collapse (shared with ChatView's hide-until-measured). */
  liveTailVisibleId: Accessor<string | undefined>
  /** Whether the idle warm-up band may run (pane visible, sized, quiet -- policy owned by ChatView). */
  warmupEnabled: Accessor<boolean>
}

export interface ChatPremeasureBands {
  /** Every candidate to hand ChatHiddenPremeasure this frame (ranged + look-ahead + warm-up, de-duped). */
  premeasureCandidates: Accessor<readonly ChatDomPremeasureCandidate[]>
  /** The IN-RANGE unmeasured rows the visible list must keep hidden until their height commits. */
  collapsedPremeasureIds: Accessor<ReadonlySet<string>>
  /** ChatHiddenPremeasure's measure callback: commits the height and settles the candidate. */
  onMeasure: (id: string, height: number, heightKey: string | undefined, measureDurationMs: number, settled: boolean) => boolean
}

/**
 * Wire the three premeasure bands into the coherence queue. Call within a reactive owner
 * (ChatView's body): it creates the band memos, the warm-up unit, and the queue.
 */
export function createChatPremeasureBands(deps: ChatPremeasureBandsDeps): ChatPremeasureBands {
  // The current entry/item arrays plus the virtualizer range clamped to their common length --
  // the shared preamble of the two bands below, so their windows can never disagree.
  const clampedWindow = (): {
    all: readonly ClassifiedEntry[]
    items: readonly VirtualItem[]
    len: number
    start: number
    end: number
  } => {
    const all = deps.visibleEntries()
    const items = deps.virtualItems()
    const { len, start, end } = clampPremeasureRange(all.length, items.length, deps.virt.range())
    return { all, items, len, start, end }
  }

  // Hidden DOM premeasurement mirrors the bounded rendered window: every unmeasured row the
  // virtualizer currently selects gets a hidden render. No idle delay / cost model / batch
  // budget -- native virtualization already caps the number of mounted rows.
  const rangedCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    if (deps.contentWidth() <= 0)
      return []
    const { all, items, start, end } = clampedWindow()
    return collectUnmeasuredCandidates(all, items, deps.virt.hasMeasuredHeight, start, end)
  })
  // Look-ahead band: unmeasured rows just OUTSIDE the rendered range (within
  // LOOKAHEAD_PREMEASURE_ROWS each side). Entries exist for out-of-range rows because
  // visibleEntries walks the whole window. These get a hidden premeasure render only -- they
  // are NOT in the main <For>, so they need no visibility flag and are not collapsed. The
  // in-range rows (skipped here) are covered by rangedCandidates.
  const lookAheadCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    if (deps.contentWidth() <= 0)
      return []
    const { all, items, len, start, end } = clampedWindow()
    const bandStart = Math.max(0, start - LOOKAHEAD_PREMEASURE_ROWS)
    const bandEnd = Math.min(len, end + LOOKAHEAD_PREMEASURE_ROWS)
    return collectUnmeasuredCandidates(all, items, deps.virt.hasMeasuredHeight, bandStart, bandEnd, start, end)
  })
  const virtualItemById = createMemo(() => {
    const result = new Map<string, VirtualItem>()
    for (const item of deps.virtualItems())
      result.set(item.id, item)
    return result
  })

  // Idle warm-up: while the pane is visible, sized, and quiet, drain the rest of the window's
  // unmeasured rows in small batches so a later fling anywhere lands on real geometry.
  const warmup = createPremeasureWarmup({
    enabled: deps.warmupEnabled,
    visibleEntries: deps.visibleEntries,
    virtualItems: deps.virtualItems,
    range: deps.virt.range,
    hasMeasuredHeight: deps.virt.hasMeasuredHeight,
    excludedBandRows: LOOKAHEAD_PREMEASURE_ROWS,
  })
  // The pending/collapsed/unsettled premeasure coherence lives in its own unit.
  const queue = createPremeasureQueue({
    virt: deps.virt,
    visibleEntryById: deps.visibleEntryById,
    virtualItemById,
    virtualItems: deps.virtualItems,
    liveTailVisibleId: deps.liveTailVisibleId,
    rangedCandidates,
    lookAheadCandidates,
    warmupCandidates: warmup.warmupCandidates,
  })

  return {
    premeasureCandidates: queue.premeasureCandidates,
    collapsedPremeasureIds: queue.collapsedPremeasureIds,
    onMeasure: queue.onMeasure,
  }
}
