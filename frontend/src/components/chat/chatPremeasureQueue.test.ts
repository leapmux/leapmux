import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createPremeasureQueue } from './chatPremeasureQueue'

describe('chatpremeasurequeue', () => {
  function makeHarness(ids: string[], liveTailId?: string) {
    const measured = new Set<string>()
    const items = ids.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` } as VirtualItem))
    const entries = new Map(ids.map(id => [id, { msg: { id } } as ClassifiedEntry]))
    const itemById = new Map(items.map(item => [item.id, item]))
    const candidate = (id: string): ChatDomPremeasureCandidate => ({ entry: entries.get(id)!, item: itemById.get(id)! })
    const [ranged, setRanged] = createSignal<ChatDomPremeasureCandidate[]>([])
    const [lookAhead, setLookAhead] = createSignal<ChatDomPremeasureCandidate[]>([])
    const [warmup, setWarmup] = createSignal<ChatDomPremeasureCandidate[]>([])
    const queue = createPremeasureQueue({
      virt: {
        hasMeasuredHeight: id => measured.has(id),
        hasPendingPremeasuredHeight: () => false,
        primeHeight: (id) => {
          measured.add(id)
          return true
        },
      },
      visibleEntryById: () => entries,
      virtualItemById: () => itemById,
      virtualItems: () => items,
      liveTailVisibleId: () => liveTailId,
      rangedCandidates: ranged,
      lookAheadCandidates: lookAhead,
      warmupCandidates: warmup,
    })
    return { queue, candidate, setRanged, setLookAhead, setWarmup, itemById, measured }
  }

  it('queues ranged candidates as pending+collapsed (live tail exempt) and look-ahead as pending only', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b', 'c'], 'c')
      h.setRanged([h.candidate('a'), h.candidate('c')])
      h.setLookAhead([h.candidate('b')])

      // All three owed a premeasure render, in display order.
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a', 'b', 'c'])
      // Only the in-range non-tail row is collapsed: the look-ahead row is not in the
      // main <For> (nothing to overlap) and collapsing the live tail would flash a
      // blank slot with no following row to protect.
      expect([...h.queue.collapsedPremeasureIds()]).toEqual(['a'])
      dispose()
    })
  })

  it('queues warm-up candidates as pending-only, like look-ahead rows', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b'])
      h.setWarmup([h.candidate('b')])

      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])
      // Warm-up rows are not in the main <For>, so they must never collapse.
      expect([...h.queue.collapsedPremeasureIds()]).toEqual([])

      // A settled measurement retires the warm-up row like any other.
      expect(h.queue.onMeasure('b', 60, 'k-b', 0, true)).toBe(true)
      expect(h.queue.premeasureCandidates()).toEqual([])
      dispose()
    })
  })

  it('a settled measurement retires the row from pending and collapse', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b'])
      h.setRanged([h.candidate('a'), h.candidate('b')])

      expect(h.queue.onMeasure('a', 120, 'k-a', 0, true)).toBe(true)
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])
      // The next band recompute (the committed height drops it from the candidates)
      // clears the collapse now that the row has real geometry.
      h.setRanged([h.candidate('b')])
      expect([...h.queue.collapsedPremeasureIds()]).toEqual(['b'])
      dispose()
    })
  })

  it('an unsettled measurement keeps the row mounted for a re-measure under the same heightKey', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a'])
      h.setRanged([h.candidate('a')])

      // Accepted but images still loading: the height committed (hasMeasuredHeight is
      // now true), yet the row must KEEP its premeasure mount so the image-settle
      // re-measure can land -- the unsettled key is what exempts it from "done".
      expect(h.queue.onMeasure('a', 80, 'k-a', 0, false)).toBe(true)
      expect(h.measured.has('a')).toBe(true)
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a'])

      // The settle re-measure retires it.
      expect(h.queue.onMeasure('a', 100, 'k-a', 0, true)).toBe(true)
      expect(h.queue.premeasureCandidates()).toEqual([])
      dispose()
    })
  })
})
