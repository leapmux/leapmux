import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate, PremeasureMeasureHandler } from './chatHiddenPremeasure'
import type { RowHeightCache } from './chatRowHeightCache'
import type { ChatRowItem } from './chatRowItem'
import type { ChatVirtualizerRange } from './useChatVirtualizer'
import { createMemo } from 'solid-js'
import { shallowEqual, shallowEqualArrays } from '~/lib/shallowEqual'
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
 * Clamp the rendered range to the `len` loaded rows. Pure index math, so the sub-range
 * that the look-ahead band skips is always inside the rows. The result is the range
 * clamped into `[0, len]`, with `start <= end`.
 */
export function clampPremeasureRange(len: number, range: ChatVirtualizerRange): ChatVirtualizerRange {
  const start = Math.max(0, Math.min(range.start, len))
  const end = Math.max(start, Math.min(range.end, len))
  return { start, end }
}

/**
 * The loaded row at `index` as a staging candidate, or `undefined` when the index holds
 * no row or the row is measured. The single home of the "row is present AND still
 * unmeasured" rule for the target band and the look-ahead band, so the two cannot differ
 * on what counts as a row to stage. Pure given `rows` / `isMeasured`.
 */
export function unmeasuredCandidateAt(
  rows: readonly ChatDomPremeasureCandidate[],
  isMeasured: (item: ChatRowItem) => boolean,
  index: number,
): ChatDomPremeasureCandidate | undefined {
  const row = rows[index]
  return row !== undefined && !isMeasured(row.item)
    ? row
    : undefined
}

/**
 * Collect the unmeasured staging candidates over the half-open index range `[from, to)`,
 * skipping the inner `[skipFrom, skipTo)` sub-range (the rows another band already covers;
 * default empty). Pure given `rows` / `isMeasured`.
 */
export function collectUnmeasuredCandidates(
  rows: readonly ChatDomPremeasureCandidate[],
  isMeasured: (item: ChatRowItem) => boolean,
  from: number,
  to: number,
  skipFrom = to,
  skipTo = to,
): ChatDomPremeasureCandidate[] {
  const candidates: ChatDomPremeasureCandidate[] = []
  for (let index = from; index < to; index++) {
    if (index >= skipFrom && index < skipTo)
      continue
    const candidate = unmeasuredCandidateAt(rows, isMeasured, index)
    if (candidate !== undefined)
      candidates.push(candidate)
  }
  return candidates
}

export interface ChatPremeasureBandsDeps {
  /** The classified entries of the loaded rows (chatEntryCache walks the whole window). */
  visibleEntries: Accessor<readonly ClassifiedEntry[]>
  /** The items of the loaded rows, index-aligned with visibleEntries. */
  loadedItems: Accessor<readonly ChatRowItem[]>
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

  // The loaded rows in display order, each entry with its item: the one input of the three
  // bands and of the queue. ChatHiddenPremeasure's <For> keys its copies by object
  // identity, so a new object for an unchanged row disposes its hidden copy and mounts it
  // again. A row therefore keeps its object while its entry and its height key stay the
  // same: the copy renders the entry, its measure commits under the height key, and
  // ChatView derives every other field of the item from the entry. The memo keeps its
  // array while every row keeps its object, so a new array of the same entries notifies
  // no reader.
  const loadedRows = createMemo<readonly ChatDomPremeasureCandidate[]>((previous) => {
    const entries = deps.visibleEntries()
    const previousById = new Map(previous.map(row => [row.item.id, row]))
    const rows: ChatDomPremeasureCandidate[] = []
    for (const [index, item] of deps.loadedItems().entries()) {
      const entry = entries[index]
      if (entry === undefined)
        continue
      const kept = previousById.get(item.id)
      rows.push(kept !== undefined && kept.entry === entry && kept.item.heightKey === item.heightKey
        ? kept
        : { entry, item })
    }
    return rows
  }, [], { equals: shallowEqualArrays })
  // id -> row over the loaded rows, for the lookups of the queue.
  const rowById = createMemo<ReadonlyMap<string, ChatDomPremeasureCandidate>>(
    () => new Map(loadedRows().map(row => [row.item.id, row])),
  )

  // Target band: the rows that the waiting target needs, in the order of its scan (see
  // rowsWantedForTarget).
  const targetCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    if (!deps.stagingEnabled())
      return NO_CANDIDATES
    const rows = loadedRows()
    const candidates: ChatDomPremeasureCandidate[] = []
    for (const index of deps.wantedRows()) {
      const candidate = unmeasuredCandidateAt(rows, isMeasured, index)
      if (candidate !== undefined)
        candidates.push(candidate)
    }
    return candidates
  })
  // The rendered range `[start, end)`, clamped to the loaded rows, and the span
  // `[bandStart, bandEnd)` that adds LOOKAHEAD_PREMEASURE_ROWS rows on each side of it. The
  // look-ahead band is that span less the rendered range, and the warm-up starts beyond the
  // span. With no rendered row the span is empty, at the clamped start of the range: the
  // view shows no row then, and the target band stages the rows of the place that waits.
  const lookAheadBand = createMemo(() => {
    const len = loadedRows().length
    const { start, end } = clampPremeasureRange(len, deps.renderedRange())
    return end <= start
      ? {
          start,
          end,
          bandStart: start,
          bandEnd: start,
        }
      : {
          start,
          end,
          bandStart: Math.max(0, start - LOOKAHEAD_PREMEASURE_ROWS),
          bandEnd: Math.min(len, end + LOOKAHEAD_PREMEASURE_ROWS),
        }
  }, undefined, { equals: shallowEqual })
  // Look-ahead band: the unmeasured loaded rows just OUTSIDE the rendered range (within
  // LOOKAHEAD_PREMEASURE_ROWS each side).
  const lookAheadCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    if (!deps.stagingEnabled())
      return NO_CANDIDATES
    const { start, end, bandStart, bandEnd } = lookAheadBand()
    if (end <= start)
      return NO_CANDIDATES
    return collectUnmeasuredCandidates(loadedRows(), isMeasured, bandStart, bandEnd, start, end)
  })

  // Idle warm-up: while staging is enabled and the pane is visible and quiet, drain the
  // other unmeasured loaded rows in small batches so a later fling anywhere lands on
  // measured rows.
  const warmup = createPremeasureWarmup({
    enabled: () => deps.stagingEnabled() && deps.warmupEnabled(),
    rows: loadedRows,
    skip: () => {
      const { bandStart, bandEnd } = lookAheadBand()
      return { start: bandStart, end: bandEnd }
    },
    isMeasured,
  })
  // The pending rows live in their own unit.
  const queue = createPremeasureQueue({
    heights: deps.heights,
    stagingEnabled: deps.stagingEnabled,
    rows: loadedRows,
    rowById,
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
