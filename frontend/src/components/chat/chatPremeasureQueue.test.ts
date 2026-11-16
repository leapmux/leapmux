import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createPremeasureQueue } from './chatPremeasureQueue'
import { createRowHeightCache } from './chatRowHeightCache'

describe('chatPremeasureQueue', () => {
  /**
   * A queue over the loaded rows `ids` (height key `k-${id}`), with a real height cache.
   * No row is measured at the start, and staging is enabled.
   */
  function makeHarness(ids: string[]) {
    const heights = createRowHeightCache({
      loadedIds: () => ids,
    })
    const items = ids.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` } as VirtualItem))
    const entries = new Map(ids.map(id => [
      id,
      {
        message: { id },
      } as ClassifiedEntry,
    ]))
    const itemById = new Map(items.map(item => [item.id, item]))
    const candidate = (id: string): ChatDomPremeasureCandidate => ({
      entry: entries.get(id)!,
      item: itemById.get(id)!,
    })
    const [target, setTarget] = createSignal<ChatDomPremeasureCandidate[]>([])
    const [lookAhead, setLookAhead] = createSignal<ChatDomPremeasureCandidate[]>([])
    const [warmup, setWarmup] = createSignal<ChatDomPremeasureCandidate[]>([])
    const [stagingEnabled, setStagingEnabled] = createSignal(true)
    const queue = createPremeasureQueue({
      heights,
      stagingEnabled,
      visibleEntryById: () => entries,
      loadedItemById: () => itemById,
      loadedItems: () => items,
      targetCandidates: target,
      lookAheadCandidates: lookAhead,
      warmupCandidates: warmup,
    })
    return {
      queue,
      heights,
      candidate,
      setTarget,
      setLookAhead,
      setWarmup,
      setStagingEnabled,
      itemById,
    }
  }

  it('settles a row that measures at a height of 0', () => {
    createRoot((dispose) => {
      const h = makeHarness(['empty', 'answer'])
      h.setTarget([
        h.candidate('empty'),
        h.candidate('answer'),
      ])
      h.queue.onMeasure('answer', 40, 'k-answer', 0)

      // A valid empty row has a height of 0, and it counts as measured.
      h.queue.onMeasure('empty', 0, 'k-empty', 0)

      expect(h.heights.heightOf(h.itemById.get('empty')!)).toBe(0)
      expect(h.queue.premeasureCandidates()).toEqual([])
      dispose()
    })
  })

  it('drops a pending row when a commit outside its copy measures it', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b'])
      h.setTarget([
        h.candidate('a'),
        h.candidate('b'),
      ])
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a', 'b'])

      // The row measures through another path than the callback of its copy, as a stored
      // height does. The queue follows the cache, so the copy of the row ends.
      h.heights.commit(h.itemById.get('a')!, 80, 'k-a', 'visible')

      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])
      dispose()
    })
  })

  it('queues the target rows and the look-ahead rows as pending', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b', 'c'])
      h.setTarget([
        h.candidate('c'),
        h.candidate('a'),
      ])
      h.setLookAhead([
        h.candidate('b'),
      ])

      // All three owed a hidden copy, in display order, whatever the order of a band.
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a', 'b', 'c'])
      dispose()
    })
  })

  it('queues warm-up candidates as pending, like look-ahead rows', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b'])
      h.setWarmup([
        h.candidate('b'),
      ])

      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])

      // A measurement retires the warm-up row like any other.
      h.queue.onMeasure('b', 60, 'k-b', 0)
      expect(h.queue.premeasureCandidates()).toEqual([])
      dispose()
    })
  })

  it('commits the height of a staged row to the cache, and retires the row', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b'])
      h.setTarget([
        h.candidate('a'),
        h.candidate('b'),
      ])

      h.queue.onMeasure('a', 120, 'k-a', 0)

      expect(h.heights.heightOf(h.itemById.get('a')!)).toBe(120)
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])
      // The row stays retired when its band still lists it: the cache holds its height.
      h.setTarget([
        h.candidate('a'),
        h.candidate('b'),
      ])
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])
      dispose()
    })
  })

  it('commits nothing for a row that left the loaded rows', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a'])
      h.setTarget([
        h.candidate('a'),
      ])

      h.queue.onMeasure('gone', 120, 'k-gone', 0)

      expect(h.heights.debugEntry('gone')).toBeUndefined()
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a'])
      dispose()
    })
  })

  it('refuses a height that a copy measured under another key', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a'])
      h.setTarget([
        h.candidate('a'),
      ])

      h.queue.onMeasure('a', 120, 'k-old', 0)

      expect(h.heights.heightOf(h.itemById.get('a')!)).toBeUndefined()
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a'])
      dispose()
    })
  })

  it('gives no candidate while staging is not enabled, and each pending row again afterwards', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b'])
      h.setTarget([
        h.candidate('a'),
        h.candidate('b'),
      ])
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a', 'b'])

      // The bands are empty while staging is not enabled, and the pending rows stay.
      h.setStagingEnabled(false)
      h.setTarget([])
      const paused = h.queue.premeasureCandidates()
      expect(paused).toEqual([])
      // A change of the pending rows during the pause gives the same empty list, so the
      // hidden copies see no change.
      h.setWarmup([])
      expect(h.queue.premeasureCandidates()).toBe(paused)

      // The pause disposed every hidden copy, so each pending row gets a new copy.
      h.setStagingEnabled(true)
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['a', 'b'])
      dispose()
    })
  })

  it('drops a row that was measured during a pause of the staging', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b'])
      h.setTarget([
        h.candidate('a'),
        h.candidate('b'),
      ])
      h.setStagingEnabled(false)

      // A stored height arrives for row a while no hidden copy exists.
      h.heights.commit(h.itemById.get('a')!, 80, 'k-a', 'premeasure')
      h.setStagingEnabled(true)

      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])
      dispose()
    })
  })

  it('keeps each candidate object while its entry and height key stay the same', () => {
    // ChatHiddenPremeasure's <For> keys rows by object identity, so a new object
    // for an unchanged row remounts its whole hidden bubble.
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b', 'c'])
      h.setTarget([
        h.candidate('a'),
        h.candidate('b'),
      ])
      const [first, second] = h.queue.premeasureCandidates()

      h.setWarmup([
        h.candidate('c'),
      ])
      const grown = h.queue.premeasureCandidates()
      expect(grown.map(c => c.item.id)).toEqual(['a', 'b', 'c'])
      expect(grown[0]).toBe(first)
      expect(grown[1]).toBe(second)

      // A measure retires its own row and leaves the others mounted.
      h.queue.onMeasure('a', 10, 'k-a', 0)
      const shrunk = h.queue.premeasureCandidates()
      expect(shrunk.map(c => c.item.id)).toEqual(['b', 'c'])
      expect(shrunk[0]).toBe(second)
      expect(shrunk[1]).toBe(grown[2])
      dispose()
    })
  })

  it('gives a row a new candidate object when its entry or its height key changes', () => {
    createRoot((dispose) => {
      const entryOf = (id: string) => ({
        message: { id },
      } as ClassifiedEntry)
      const [entries, setEntries] = createSignal<ReadonlyMap<string, ClassifiedEntry>>(new Map<string, ClassifiedEntry>()
        .set('a', entryOf('a'))
        .set('b', entryOf('b')))
      const [items, setItems] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, heightKey: 'k-a' },
        { id: 'b', hasSpanLines: false, heightKey: 'k-b' },
      ])
      const queue = createPremeasureQueue({
        heights: createRowHeightCache({
          loadedIds: () => items().map(item => item.id),
        }),
        stagingEnabled: () => true,
        visibleEntryById: entries,
        loadedItemById: () => new Map(items().map(item => [item.id, item])),
        loadedItems: items,
        targetCandidates: () => items().map(item => ({
          entry: entries().get(item.id)!,
          item,
        })),
        lookAheadCandidates: () => [],
      })
      const [a, b] = queue.premeasureCandidates()

      // The window rebuilds every item object, but the height keys stay the same.
      setItems(prev => prev.map(item => ({ ...item })))
      expect(queue.premeasureCandidates()[0]).toBe(a)
      expect(queue.premeasureCandidates()[1]).toBe(b)

      setItems(prev => prev.map(item => (item.id === 'a'
        ? { ...item, heightKey: 'k-a2' }
        : item)))
      const rekeyed = queue.premeasureCandidates()
      expect(rekeyed[0]).not.toBe(a)
      expect(rekeyed[0]?.item.heightKey).toBe('k-a2')
      expect(rekeyed[1]).toBe(b)

      const rebuiltB = entryOf('b')
      setEntries(prev => new Map(prev).set('b', rebuiltB))
      const reclassified = queue.premeasureCandidates()
      expect(reclassified[0]).toBe(rekeyed[0])
      expect(reclassified[1]).not.toBe(b)
      expect(reclassified[1]?.entry).toBe(rebuiltB)
      dispose()
    })
  })

  it('records the acceptance of the cache and the refusal of a stale key only in development', () => {
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
          h.setTarget([candidate])
          const { queue } = h
          queue.onMeasure('a', 88, 'old-key', 0)
          expect(events).toContainEqual({
            phase: 'commit',
            id: 'a',
            seq: '6',
            height: 88,
            heightKey: 'old-key',
            currentHeightKey: 'k-a',
            accepted: false,
            hasMeasured: false,
            candidatePending: true,
          })
          queue.onMeasure('a', 88, 'k-a', 0)
          expect(events).toContainEqual({
            phase: 'commit',
            id: 'a',
            seq: '6',
            height: 88,
            heightKey: 'k-a',
            currentHeightKey: 'k-a',
            accepted: true,
            hasMeasured: true,
            candidatePending: false,
          })
          const count = events.length
          vi.stubEnv('LEAPMUX_DEV', undefined)
          queue.onMeasure('a', 101, 'k-a', 0)
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
