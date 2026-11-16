import type { Accessor } from 'solid-js'
import type { ChatDomPremeasureCandidate, PremeasureMeasureHandler } from './chatHiddenPremeasure'
import type { RowHeightCache } from './chatRowHeightCache'
import { createMemo, untrack } from 'solid-js'
import { emitDevEvent } from '~/lib/devInstrument'
import { shallowEqualSets } from '~/lib/shallowEqual'
import { warnSlowScrollPhase } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Hidden-premeasure queue
//
// The set of the loaded rows that are owed a hidden copy (`pendingPremeasureIds`), kept
// coherent as the staging bands, the loaded rows, and the height cache change.
//
// A row stays pending until it is measured or leaves the loaded rows. A hidden copy
// reports no height while one of its images still loads (see ChatHiddenPremeasure), so
// the queue keeps no record of a measurement that still settles.
//
// While staging is not enabled, the queue gives no candidate, so no hidden copy exists.
// It keeps the pending set, and a new copy of each pending row mounts when staging is
// enabled again.
//
// Owns no DOM: the staging bands supply the loaded rows, their lookup by id, and the
// candidates, and ChatView renders ChatHiddenPremeasure from the returned accessor.
// ---------------------------------------------------------------------------

export interface PremeasureQueueDeps {
  /** The height cache: the done test of a pending row, and the commit of a measure. */
  heights: Pick<RowHeightCache, 'isMeasured' | 'commit'>
  /** Whether a hidden copy may render (see ChatPremeasureBandsDeps.stagingEnabled). */
  stagingEnabled: Accessor<boolean>
  /**
   * The loaded rows in display order, each entry with its item. A row keeps its object
   * while its entry and its height key stay the same (see createChatPremeasureBands).
   */
  rows: Accessor<readonly ChatDomPremeasureCandidate[]>
  /** id -> row over `rows`. */
  rowById: Accessor<ReadonlyMap<string, ChatDomPremeasureCandidate>>
  /** The unmeasured rows that a waiting target needs. */
  targetCandidates: Accessor<readonly ChatDomPremeasureCandidate[]>
  /** The unmeasured rows just outside the rendered range. */
  lookAheadCandidates: Accessor<readonly ChatDomPremeasureCandidate[]>
  /** Idle warm-up rows from deeper in the loaded rows (see createPremeasureWarmup). */
  warmupCandidates: Accessor<readonly ChatDomPremeasureCandidate[]>
}

const NO_CANDIDATES: readonly ChatDomPremeasureCandidate[] = []

/**
 * Create the premeasure queue. Call within a component's reactive owner (it allocates
 * memos); the returned accessors are stable for the owner's lifetime.
 * `onMeasure` is the ChatHiddenPremeasure callback: it commits the height, and a row
 * with a committed height stops being pending.
 */
export function createPremeasureQueue(deps: PremeasureQueueDeps) {
  // A row that the cache answers for is done (see RowHeightCache.isMeasured).
  const { isMeasured } = deps.heights

  // The pending rows: the set of the run before, with each row that a band gives, less
  // each row that left the loaded rows or is measured. The measured test reads `version`,
  // so the commit of a measure drops its row here. A row gains a height with no change of
  // `version` only when an element of it mounts, and a pending row never mounts: it is not
  // measured, and the admission admits only measured rows (see chatRowAdmission).
  const pendingPremeasureIds = createMemo<ReadonlySet<string>>((previous) => {
    const rowById = deps.rowById()
    const next = new Set(previous)
    for (const candidate of deps.targetCandidates())
      next.add(candidate.item.id)
    for (const candidate of deps.lookAheadCandidates())
      next.add(candidate.item.id)
    for (const candidate of deps.warmupCandidates())
      next.add(candidate.item.id)
    for (const id of [...next]) {
      const row = rowById.get(id)
      if (row === undefined || isMeasured(row.item))
        next.delete(id)
    }
    return next
  }, new Set<string>(), { equals: shallowEqualSets })

  // The pending rows in display order. ChatHiddenPremeasure's <For> keys its copies by
  // object identity, so the memo gives each row object of `rows` as it is: the object stays
  // while the entry and the height key of the row stay the same (see
  // createChatPremeasureBands). A change to the pending set then mounts only the rows that
  // it adds.
  //
  // While staging is not enabled, the memo gives no candidate, and <For> then disposes
  // every copy. Each pending row gets a new copy afterwards, although its object is the
  // same.
  const premeasureCandidates = createMemo<readonly ChatDomPremeasureCandidate[]>(() => {
    const ids = pendingPremeasureIds()
    if (!deps.stagingEnabled() || ids.size === 0)
      return NO_CANDIDATES
    return deps.rows().filter(row => ids.has(row.item.id))
  })

  const onMeasure: PremeasureMeasureHandler = (id, height, heightKey, measureDurationMs) => {
    // Forced-layout cost of premeasuring this row. When a batch of look-ahead rows
    // premeasures in one frame, the FIRST getBoundingClientRect reflows the dirty DOM
    // (expensive) and the rest are cheap -- so a slow duration here localizes a
    // premeasure-driven main-thread stall (a stalled scroll delivers its moves as one
    // batched catch-up delta). Only a slow measure logs (see warnSlowScrollPhase).
    // The current row. A row that left the loaded rows commits nothing.
    const row = untrack(deps.rowById).get(id)
    const item = row?.item
    warnSlowScrollPhase('premeasure', measureDurationMs, { id, kind: item?.kind })
    // The cache refuses a height of another key, and a hidden-layout read for a mounted
    // row that has a height (see RowHeightCache.commit).
    const accepted = item !== undefined && deps.heights.commit(item, height, heightKey, 'premeasure') !== 'none'
    // Whether the cache answers for the row after the commit, for the dev event. The
    // pending set drops a measured row by itself: a commit that changes an answer changes
    // `version`.
    const hasMeasured = item !== undefined && untrack(() => isMeasured(item))
    emitDevEvent('leapmux:chat-premeasure', () => ({
      phase: 'commit',
      id,
      seq: row?.entry.message.seq?.toString() ?? null,
      height: Number.isFinite(height)
        ? height
        : String(height),
      heightKey: heightKey ?? null,
      currentHeightKey: item?.heightKey ?? null,
      accepted,
      hasMeasured,
      candidatePending: untrack(pendingPremeasureIds).has(id),
    }))
  }

  return { premeasureCandidates, onMeasure }
}
