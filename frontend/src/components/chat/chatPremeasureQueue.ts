import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { UseChatVirtualizerResult, VirtualItem } from './useChatVirtualizer'
import { createComputed, createMemo, createSignal, untrack } from 'solid-js'
import { shallowEqualSets } from '~/lib/shallowEqual'
import { warnSlowScrollPhase } from './chatScrollGeometry'

// ---------------------------------------------------------------------------
// Hidden-premeasure queue
//
// The reconciler that keeps the three premeasure states coherent as the candidate
// bands, the window, and the virtualizer's height caches change:
//  - `pendingPremeasureIds`: the rows currently owed a hidden premeasure render.
//  - `collapsedPremeasureIds`: the IN-RANGE unmeasured rows the visible list must keep
//    collapsed until their height commits, so a tall unmeasured row can't paint past
//    its estimated slot and overlap the next row. Look-ahead rows are never collapsed
//    (they are not in the main <For>), and neither is the live tail (it has no
//    following row to protect; collapsing it would flash a blank slot).
//  - `unsettledPremeasureKeys`: rows whose ACCEPTED measurement is still settling
//    (pending images), keyed by the heightKey it was measured under, so the row keeps
//    its premeasure mount for a re-measure instead of being dropped as done.
//
// Extracted from ChatView (where the three signals, the reconciler, and the measure
// handler lived loose in an 1100-line component) so the coherence invariants have a
// named, unit-testable home. Owns no DOM: ChatView supplies the candidate bands and
// lookup memos and renders ChatHiddenPremeasure from the returned accessors.
// ---------------------------------------------------------------------------

export interface PremeasureQueueDeps {
  /** The height-cache surface consulted for done/pending states, plus the commit. */
  virt: Pick<UseChatVirtualizerResult, 'hasMeasuredHeight' | 'hasPendingPremeasuredHeight' | 'primeHeight'>
  /** Entry lookup over the current window (premeasure renders need the entry). */
  visibleEntryById: Accessor<ReadonlyMap<string, ClassifiedEntry>>
  /** Item lookup over the current window (heightKey / kind reads). */
  virtualItemById: Accessor<ReadonlyMap<string, VirtualItem>>
  /** The current items in display order (candidate assembly preserves it). */
  virtualItems: Accessor<readonly VirtualItem[]>
  /** The live tail's id (exempt from collapse), or undefined when windowed away. */
  liveTailVisibleId: Accessor<string | undefined>
  /** Unmeasured rows inside the rendered range (collapsed until they measure). */
  rangedCandidates: Accessor<readonly ChatDomPremeasureCandidate[]>
  /** Unmeasured rows just outside the range (premeasured, never collapsed). */
  lookAheadCandidates: Accessor<readonly ChatDomPremeasureCandidate[]>
  /**
   * Idle warm-up rows from deeper in the window (see createPremeasureWarmup).
   * Treated exactly like look-ahead rows: premeasured, never collapsed.
   * Optional so tests and non-warming callers can omit the band.
   */
  warmupCandidates?: Accessor<readonly ChatDomPremeasureCandidate[]>
}

/**
 * Create the premeasure queue. Call within a component's reactive owner (it allocates
 * signals and a computed); the returned accessors are stable for the owner's lifetime.
 * `onMeasure` is the ChatHiddenPremeasure callback: it commits the height and settles
 * or re-arms the row's queue entries by the measurement's outcome.
 */
export function createPremeasureQueue(deps: PremeasureQueueDeps) {
  const removeIdFromSet = (ids: ReadonlySet<string>, id: string): ReadonlySet<string> => {
    if (!ids.has(id))
      return ids
    const next = new Set(ids)
    next.delete(id)
    return next
  }
  const removeIdFromMap = <V>(items: ReadonlyMap<string, V>, id: string): ReadonlyMap<string, V> => {
    if (!items.has(id))
      return items
    const next = new Map(items)
    next.delete(id)
    return next
  }
  const [pendingPremeasureIds, setPendingPremeasureIds] = createSignal<ReadonlySet<string>>(new Set())
  const [collapsedPremeasureIds, setCollapsedPremeasureIds] = createSignal<ReadonlySet<string>>(new Set())
  const [unsettledPremeasureKeys, setUnsettledPremeasureKeys] = createSignal<ReadonlyMap<string, string | undefined>>(new Map())

  createComputed(() => {
    const entries = deps.visibleEntryById()
    const items = deps.virtualItemById()
    const liveTailId = deps.liveTailVisibleId()
    const unsettled = untrack(unsettledPremeasureKeys)
    const nextPending = new Set(untrack(pendingPremeasureIds))
    const nextCollapsed = new Set(untrack(collapsedPremeasureIds))
    const nextUnsettled = new Map(unsettled)
    for (const candidate of deps.rangedCandidates()) {
      nextPending.add(candidate.item.id)
      if (candidate.item.id !== liveTailId)
        nextCollapsed.add(candidate.item.id)
    }
    // Look-ahead and warm-up band rows are premeasured (pending) but never marked
    // invisible: they are not rendered in the main list, so they need no collapse entry.
    for (const candidate of deps.lookAheadCandidates())
      nextPending.add(candidate.item.id)
    for (const candidate of deps.warmupCandidates?.() ?? [])
      nextPending.add(candidate.item.id)
    if (liveTailId !== undefined)
      nextCollapsed.delete(liveTailId)
    for (const id of [...nextPending]) {
      const item = items.get(id)
      if (!entries.has(id) || !item) {
        nextPending.delete(id)
        nextUnsettled.delete(id)
        continue
      }
      const unsettledKeyMatches = nextUnsettled.has(id) && nextUnsettled.get(id) === item.heightKey
      if ((deps.virt.hasMeasuredHeight(id) || deps.virt.hasPendingPremeasuredHeight(id)) && !unsettledKeyMatches)
        nextPending.delete(id)
    }
    for (const id of [...nextCollapsed]) {
      if (!entries.has(id) || !items.has(id) || deps.virt.hasMeasuredHeight(id))
        nextCollapsed.delete(id)
    }
    for (const [id, heightKey] of [...nextUnsettled]) {
      const item = items.get(id)
      if (!entries.has(id) || !item || item.heightKey !== heightKey)
        nextUnsettled.delete(id)
    }

    const prevPending = untrack(pendingPremeasureIds)
    if (!shallowEqualSets(prevPending, nextPending))
      setPendingPremeasureIds(nextPending)
    const prevCollapsed = untrack(collapsedPremeasureIds)
    if (!shallowEqualSets(prevCollapsed, nextCollapsed))
      setCollapsedPremeasureIds(nextCollapsed)
    const prevUnsettled = untrack(unsettledPremeasureKeys)
    if (prevUnsettled.size !== nextUnsettled.size || [...prevUnsettled].some(([id, heightKey]) => nextUnsettled.get(id) !== heightKey))
      setUnsettledPremeasureKeys(nextUnsettled)
  })

  const premeasureCandidates = createMemo<ChatDomPremeasureCandidate[]>(() => {
    const ids = pendingPremeasureIds()
    if (ids.size === 0)
      return []
    const entries = deps.visibleEntryById()
    const unsettled = unsettledPremeasureKeys()
    const candidates: ChatDomPremeasureCandidate[] = []
    for (const item of deps.virtualItems()) {
      const unsettledKeyMatches = unsettled.has(item.id) && unsettled.get(item.id) === item.heightKey
      if (!ids.has(item.id) || (deps.virt.hasMeasuredHeight(item.id) && !unsettledKeyMatches))
        continue
      const entry = entries.get(item.id)
      if (entry)
        candidates.push({ entry, item })
    }
    return candidates
  })

  const onMeasure = (id: string, height: number, heightKey: string | undefined, measureDurationMs: number, settled: boolean): boolean => {
    // Forced-layout cost of premeasuring this row. When a batch of look-ahead rows
    // premeasures in one frame, the FIRST getBoundingClientRect reflows the dirty DOM
    // (expensive) and the rest are cheap -- so a slow duration here localizes a
    // premeasure-driven main-thread stall (the batched catch-up scroll deltas Detector
    // B reports). Only a slow measure logs (see warnSlowScrollPhase).
    warnSlowScrollPhase('premeasure', measureDurationMs, { id, kind: deps.virtualItemById().get(id)?.kind })
    const accepted = deps.virt.primeHeight(id, height, heightKey)
    const hasCommittedOrPendingHeight = accepted || deps.virt.hasMeasuredHeight(id) || deps.virt.hasPendingPremeasuredHeight(id)
    if (settled && hasCommittedOrPendingHeight) {
      setPendingPremeasureIds(ids => removeIdFromSet(ids, id))
      setUnsettledPremeasureKeys(keys => removeIdFromMap(keys, id))
    }
    else if (!settled && hasCommittedOrPendingHeight) {
      setUnsettledPremeasureKeys((keys) => {
        if (keys.has(id) && keys.get(id) === heightKey)
          return keys
        const next = new Map(keys)
        next.set(id, heightKey)
        return next
      })
    }
    return hasCommittedOrPendingHeight
  }

  return { premeasureCandidates, collapsedPremeasureIds, onMeasure }
}
