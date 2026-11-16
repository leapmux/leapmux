import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import { createMemo, createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createPremeasureQueue } from './chatPremeasureQueue'
import { createRowHeightCache } from './chatRowHeightCache'

describe('chatPremeasureQueue', () => {
  /**
   * A queue over the loaded rows `ids` (height key `k-${id}`), with a real height cache.
   * No row is measured at the start, and staging is enabled. `candidate` gives the object
   * of a row, and `setRows` replaces the loaded rows. Call within a reactive owner.
   */
  function makeHarness(ids: string[]) {
    const heights = createRowHeightCache({
      loadedIds: () => ids,
    })
    const allRows: readonly ChatDomPremeasureCandidate[] = ids.map(id => ({
      entry: {
        message: { id },
      } as ClassifiedEntry,
      item: { id, hasSpanLines: false, heightKey: `k-${id}` },
    }))
    const rowOf = new Map(allRows.map(row => [row.item.id, row]))
    const itemById = new Map(allRows.map(row => [row.item.id, row.item]))
    const candidate = (id: string): ChatDomPremeasureCandidate => rowOf.get(id)!
    const [rows, setRows] = createSignal(allRows)
    const [target, setTarget] = createSignal<ChatDomPremeasureCandidate[]>([])
    const [lookAhead, setLookAhead] = createSignal<ChatDomPremeasureCandidate[]>([])
    const [warmup, setWarmup] = createSignal<ChatDomPremeasureCandidate[]>([])
    const [stagingEnabled, setStagingEnabled] = createSignal(true)
    const queue = createPremeasureQueue({
      heights,
      stagingEnabled,
      rows,
      rowById: createMemo(() => new Map(rows().map(row => [row.item.id, row]))),
      targetCandidates: target,
      lookAheadCandidates: lookAhead,
      warmupCandidates: warmup,
    })
    return {
      queue,
      heights,
      allRows,
      candidate,
      setRows,
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

  it('gives each pending row as its object of the loaded rows, so a change of the pending rows keeps the other objects', () => {
    // ChatHiddenPremeasure's <For> keys its copies by object identity, so a new object
    // for an unchanged row mounts its whole hidden copy again.
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b', 'c'])
      h.setTarget([
        h.candidate('a'),
        h.candidate('b'),
      ])
      const [first, second] = h.queue.premeasureCandidates()
      expect(first).toBe(h.candidate('a'))
      expect(second).toBe(h.candidate('b'))

      h.setWarmup([
        h.candidate('c'),
      ])
      const grown = h.queue.premeasureCandidates()
      expect(grown.map(c => c.item.id)).toEqual(['a', 'b', 'c'])
      expect(grown[0]).toBe(h.candidate('a'))
      expect(grown[1]).toBe(h.candidate('b'))
      expect(grown[2]).toBe(h.candidate('c'))

      // A measure retires its own row and leaves the others mounted.
      h.queue.onMeasure('a', 10, 'k-a', 0)
      const shrunk = h.queue.premeasureCandidates()
      expect(shrunk.map(c => c.item.id)).toEqual(['b', 'c'])
      expect(shrunk[0]).toBe(h.candidate('b'))
      expect(shrunk[1]).toBe(h.candidate('c'))
      dispose()
    })
  })

  it('drops a pending row that leaves the loaded rows, so the row is not pending when it returns', () => {
    createRoot((dispose) => {
      const h = makeHarness(['a', 'b'])
      h.setTarget([
        h.candidate('a'),
        h.candidate('b'),
      ])

      // Row a leaves the loaded rows while its band still lists it.
      h.setRows(rows => rows.filter(row => row.item.id !== 'a'))
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])

      // The band drops the row, and the row returns: no band adds it, so it stays out.
      h.setTarget([
        h.candidate('b'),
      ])
      h.setRows(h.allRows)
      expect(h.queue.premeasureCandidates().map(c => c.item.id)).toEqual(['b'])
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
