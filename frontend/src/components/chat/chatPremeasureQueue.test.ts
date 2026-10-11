import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createOrderedTailReveal } from './chatOrderedReveal'
import { createPremeasureQueue } from './chatPremeasureQueue'
import { setup } from './useChatVirtualizer.testkit'

describe('chatPremeasureQueue', () => {
  it('settles an empty measured row and releases the later appended row', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [
        { id: 'empty', hasSpanLines: false, heightKey: 'empty-key' },
        { id: 'answer', hasSpanLines: false, heightKey: 'answer-key' },
      ]
      const entries = new Map(items.map(item => [item.id, { message: { id: item.id } } as ClassifiedEntry]))
      const { virt } = setup(items)
      const candidates = () => items.filter(item => !virt.hasMeasuredHeight(item.id)).map(item => ({ entry: entries.get(item.id)!, item }))
      const queue = createPremeasureQueue({
        virt,
        visibleEntryById: () => entries,
        virtualItemById: () => new Map(items.map(item => [item.id, item])),
        virtualItems: () => items,
        rangedCandidates: candidates,
        lookAheadCandidates: () => [],
      })
      const held = createOrderedTailReveal(() => items.map(item => item.id), id => !virt.hasMeasuredHeight(id) && queue.collapsedPremeasureIds().has(id))
      queue.onMeasure('answer', 40, 'answer-key', 0, true)
      expect(held().has('answer')).toBe(true)

      expect(queue.onMeasure('empty', 0, 'empty-key', 0, true)).toBe(true)

      expect(virt.hasMeasuredHeight('empty')).toBe(true)
      expect(queue.collapsedPremeasureIds().has('empty')).toBe(false)
      expect(queue.premeasureCandidates()).toEqual([])
      expect(held().has('answer')).toBe(false)
      dispose()
    })
  })
  function makeHarness(ids: string[]) {
    const measured = new Set<string>()
    const items = ids.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` } as VirtualItem))
    const entries = new Map(ids.map(id => [id, { message: { id } } as ClassifiedEntry]))
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
      rangedCandidates: ranged,
      lookAheadCandidates: lookAhead,
      warmupCandidates: warmup,
    })
    return { queue, candidate, setRanged, setLookAhead, setWarmup, itemById, measured }
  }

  it('queues ranged candidates as pending+collapsed (live tail included) and look-ahead as pending only', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b', 'c'])
      h.setRanged([h.candidate('a'), h.candidate('c')])
      h.setLookAhead([h.candidate('b')])

      // All three owed a premeasure render, in display order.
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a', 'b', 'c'])
      // Every in-range ranged row is collapsed -- the live tail 'c' included, so its
      // unmeasured content can't overflow onto the trailing in-flow tail UI and it
      // reveals in order with its siblings. The look-ahead row 'b' is not in the main
      // <For>, so it is never collapsed.
      expect([...h.queue.collapsedPremeasureIds()].sort()).toEqual(['a', 'c'])
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

  it('records actual virtualizer acceptance and stale-key refusal only in development', () => {
    const events: unknown[] = []
    const record = (event: Event) => {
      if (event instanceof CustomEvent)
        events.push(event.detail)
    }
    window.addEventListener('leapmux:chat-premeasure', record)
    vi.stubEnv('LEAPMUX_DEV', '1')
    try {
      createRoot((dispose) => {
        try {
          const h = makeHarness(['a'])
          const candidate = h.candidate('a')
          candidate.entry.message.seq = 6n
          const { virt } = setup([candidate.item])
          const queue = createPremeasureQueue({
            virt,
            visibleEntryById: () => new Map([['a', candidate.entry]]),
            virtualItemById: () => new Map([['a', candidate.item]]),
            virtualItems: () => [candidate.item],
            rangedCandidates: () => [candidate],
            lookAheadCandidates: () => [],
          })
          expect(queue.onMeasure('a', 88, 'old-key', 0, true)).toBe(false)
          expect(events).toContainEqual({
            phase: 'commit',
            id: 'a',
            seq: '6',
            height: 88,
            heightKey: 'old-key',
            currentHeightKey: 'k-a',
            accepted: false,
            hasMeasured: false,
            pending: false,
            settled: true,
            candidatePending: true,
            collapsed: true,
          })
          expect(queue.onMeasure('a', 88, 'k-a', 0, true)).toBe(true)
          expect(events).toContainEqual({
            phase: 'commit',
            id: 'a',
            seq: '6',
            height: 88,
            heightKey: 'k-a',
            currentHeightKey: 'k-a',
            accepted: true,
            hasMeasured: true,
            pending: false,
            settled: true,
            candidatePending: false,
            collapsed: false,
          })
          const count = events.length
          vi.stubEnv('LEAPMUX_DEV', undefined)
          queue.onMeasure('a', 101, 'k-a', 0, true)
          expect(events).toHaveLength(count)
        }
        finally {
          dispose()
        }
      })
    }
    finally {
      window.removeEventListener('leapmux:chat-premeasure', record)
      vi.unstubAllEnvs()
    }
  })
})
