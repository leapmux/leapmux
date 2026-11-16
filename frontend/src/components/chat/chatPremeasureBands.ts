import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate, PremeasureMeasureHandler } from './chatHiddenPremeasure'
import type { RowHeightCache } from './chatRowHeightCache'
import type { ChatVirtualizerRange, VirtualItem } from './useChatVirtualizer'
import { createMemo } from 'solid-js'
import { createPremeasureQueue } from './chatPremeasureQueue'
import { createPremeasureWarmup } from './chatPremeasureWarmup'

// ---------------------------------------------------------------------------
// Staging bands
//
// A loaded row joins the chat list only after a hidden copy measured it (see
// chatRowAdmission). This facade selects the loaded rows that get a hidden copy, in three
// bands, and wires them into the queue that keeps a row pending until it is measured:
//
//  - TARGET band: the rows that a waiting target needs measured next (the `wantedRows` of
//    the admission), so the place that the reader asked for can show.
//  - LOOK-AHEAD band: the unmeasured loaded rows just OUTSIDE the rendered range (within
//    LOOKAHEAD_PREMEASURE_ROWS on each side), so a row is measured, and can join the
//    list, before the reader reaches it. It skips the rendered range: a rendered row is
//    always measured.
//  - WARM-UP band: an idle drain of the other unmeasured loaded rows, so a later fling
//    anywhere lands on measured rows (createPremeasureWarmup).
//
// Every band is empty while staging is not enabled: no hidden copy renders at a width
// that still changes, or before the fonts of the chat arrived (see ChatView).
//
// All indexes are indexes of the LOADED rows, not of the rows of the virtualizer, which
// holds the admitted rows only.
// ---------------------------------------------------------------------------

// Loaded rows to stage BEYOND the rendered range in each direction, so a row is measured
// before the reader reaches it. Kept modest: the whole band measures in one frame
// (measurement isn't chunked), so this limits the burst. A fast fling that outruns it
// reaches the end of the measured rows and waits there.
export const LOOKAHEAD_PREMEASURE_ROWS = 12

/**
 * Clamp the rendered range to the common length of the entry / item arrays.
 * Pure index math, so the sub-range that the look-ahead band skips is always inside the
 * arrays. `len` is the common array length; `[start, end)` is the range clamped into
 * `[0, len]` with `start <= end`.
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
 * The staging candidate for the loaded row at `index`, or `undefined` when the row is
 * absent or measured. The single home of the "row is present AND still unmeasured" rule
 * for the target band and the look-ahead band, so the two cannot differ on what counts
 * as a row to stage. Pure given `all` / `items` / `isMeasured`.
 */
export function unmeasuredCandidateAt(
  all: readonly ClassifiedEntry[],
  items: readonly VirtualItem[],
  isMeasured: (item: VirtualItem) => boolean,
  index: number,
): ChatDomPremeasureCandidate | undefined {
  const entry = all[index]
  const item = items[index]
  return entry && item && !isMeasured(item)
    ? { entry, item }
    : undefined
}

/**
 * Collect the unmeasured staging candidates over the half-open index range `[from, to)`,
 * skipping the inner `[skipFrom, skipTo)` sub-range (the rows another band already covers;
 * default empty). Pure given `all` / `items` / `isMeasured`.
 */
export function collectUnmeasuredCandidates(
  all: readonly ClassifiedEntry[],
  items: readonly VirtualItem[],
  isMeasured: (item: VirtualItem) => boolean,
  from: number,
  to: number,
  skipFrom = to,
  skipTo = to,
): ChatDomPremeasureCandidate[] {
  const candidates: ChatDomPremeasureCandidate[] = []
  for (let index = from; index < to; index++) {
    if (index >= skipFrom && index < skipTo)
      continue
    const candidate = unmeasuredCandidateAt(all, items, isMeasured, index)
    if (candidate !== undefined)
      candidates.push(candidate)
  }
  return candidates
}

export interface ChatPremeasureBandsDeps {
  /** The classified entries of the loaded rows (chatEntryCache walks the whole window). */
  visibleEntries: Accessor<readonly ClassifiedEntry[]>
  /** The items of the loaded rows, index-aligned with visibleEntries. */
  loadedItems: Accessor<readonly VirtualItem[]>
  /** The height cache: the measured test of the bands, and the commit of the queue. */
  heights: Pick<RowHeightCache, 'isMeasured' | 'commit'>
  /** The rendered range, as indexes of the loaded rows. Empty while no row is admitted. */
  renderedRange: Accessor<ChatVirtualizerRange>
  /** The indexes of the loaded rows that a waiting target needs measured next (see RowAdmission.wantedRows). */
  wantedRows: Accessor<readonly number[]>
  /**
   * Whether a hidden copy may render: the rows have a width above 0, that width is
   * settled, and the fonts of the chat are ready (policy owned by ChatView).
   */
  stagingEnabled: Accessor<boolean>
  /** id -> entry for the loaded rows (built once in ChatView). */
  visibleEntryById: Accessor<ReadonlyMap<string, ClassifiedEntry>>
  /**
   * Whether the idle warm-up band may run: the pane is visible and quiet, and the first
   * view is shown (policy owned by ChatView). The bands add `stagingEnabled` to it.
   */
  warmupEnabled: Accessor<boolean>
}

export interface ChatPremeasureBands {
  /**
   * The rows that ChatHiddenPremeasure holds a copy of, in display order: each row that a
   * band added (target, look-ahead, warm-up) and that is not measured yet. A row that left
   * every band stays until it is measured or leaves the loaded rows (see the premeasure
   * queue). Empty while staging is not enabled.
   */
  premeasureCandidates: Accessor<readonly ChatDomPremeasureCandidate[]>
  /**
   * The ids of the rows of the target band and of the look-ahead band: the staged rows that
   * the reader waits for, or reaches next. Their worker jobs (Markdown, a diff) run before
   * those of the warm-up band.
   */
  priorityIds: Accessor<ReadonlySet<string>>
  /** ChatHiddenPremeasure's measure callback: commits the height, and the row stops being pending. */
  onMeasure: PremeasureMeasureHandler
}

const NO_CANDIDATES: ChatDomPremeasureCandidate[] = []

/**
 * Wire the three staging bands into the premeasure queue. Call within a reactive owner
 * (ChatView's body): it creates the band memos, the warm-up unit, and the queue.
 */
export function createChatPremeasureBands(deps: ChatPremeasureBandsDeps): ChatPremeasureBands {
  const { isMeasured } = deps.heights

  // Target band: the rows that the waiting target needs, in the order of its scan (see
  // rowsWantedForTarget).
  const targetCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    if (!deps.stagingEnabled())
      return NO_CANDIDATES
    const all = deps.visibleEntries()
    const items = deps.loadedItems()
    const candidates: ChatDomPremeasureCandidate[] = []
    for (const index of deps.wantedRows()) {
      const candidate = unmeasuredCandidateAt(all, items, isMeasured, index)
      if (candidate !== undefined)
        candidates.push(candidate)
    }
    return candidates
  })
  // Look-ahead band: the unmeasured loaded rows just OUTSIDE the rendered range (within
  // LOOKAHEAD_PREMEASURE_ROWS each side). With no rendered row it is empty: the view shows
  // no row then, and the target band stages the rows of the place that waits.
  const lookAheadCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    if (!deps.stagingEnabled())
      return NO_CANDIDATES
    const all = deps.visibleEntries()
    const items = deps.loadedItems()
    const { len, start, end } = clampPremeasureRange(all.length, items.length, deps.renderedRange())
    if (end <= start)
      return NO_CANDIDATES
    const bandStart = Math.max(0, start - LOOKAHEAD_PREMEASURE_ROWS)
    const bandEnd = Math.min(len, end + LOOKAHEAD_PREMEASURE_ROWS)
    return collectUnmeasuredCandidates(all, items, isMeasured, bandStart, bandEnd, start, end)
  })
  const loadedItemById = createMemo(() => {
    const result = new Map<string, VirtualItem>()
    for (const item of deps.loadedItems())
      result.set(item.id, item)
    return result
  })

  // Idle warm-up: while staging is enabled and the pane is visible and quiet, drain the
  // other unmeasured loaded rows in small batches so a later fling anywhere lands on
  // measured rows.
  const warmup = createPremeasureWarmup({
    enabled: () => deps.stagingEnabled() && deps.warmupEnabled(),
    visibleEntries: deps.visibleEntries,
    loadedItems: deps.loadedItems,
    range: deps.renderedRange,
    isMeasured,
    excludedBandRows: LOOKAHEAD_PREMEASURE_ROWS,
  })
  // The pending rows live in their own unit.
  const queue = createPremeasureQueue({
    heights: deps.heights,
    stagingEnabled: deps.stagingEnabled,
    visibleEntryById: deps.visibleEntryById,
    loadedItemById,
    loadedItems: deps.loadedItems,
    targetCandidates,
    lookAheadCandidates,
    warmupCandidates: warmup.warmupCandidates,
  })

  const priorityIds = createMemo<ReadonlySet<string>>(() => {
    const ids = new Set<string>()
    for (const candidate of targetCandidates())
      ids.add(candidate.item.id)
    for (const candidate of lookAheadCandidates())
      ids.add(candidate.item.id)
    return ids
  })

  return {
    premeasureCandidates: queue.premeasureCandidates,
    priorityIds,
    onMeasure: queue.onMeasure,
  }
}
