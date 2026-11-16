import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatDomPremeasureCandidate } from './chatHiddenPremeasure'
import type { ChatRowItem } from './chatRowItem'
import type { ChatVirtualizerRange } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { clampPremeasureRange, collectUnmeasuredCandidates, createChatPremeasureBands, unmeasuredCandidateAt } from './chatPremeasureBands'
import { WARMUP_IDLE_DELAY_MS } from './chatPremeasureWarmup'
import { createRowHeightCache } from './chatRowHeightCache'

describe('chatPremeasureBands', () => {
  // The collector reads only `item.id`; the entry is opaque, so a marker object suffices.
  const row = (id: string): ChatDomPremeasureCandidate => ({
    entry: { marker: id } as unknown as ClassifiedEntry,
    item: { id, hasSpanLines: false },
  })

  describe('clampPremeasureRange', () => {
    it('passes an in-bounds range through unchanged', () => {
      expect(clampPremeasureRange(10, { start: 2, end: 6 })).toEqual({ start: 2, end: 6 })
    })

    it('clamps the end of a range that runs past the rows', () => {
      expect(clampPremeasureRange(4, { start: 1, end: 8 })).toEqual({ start: 1, end: 4 })
    })

    it('clamps a negative start to 0', () => {
      expect(clampPremeasureRange(4, { start: -3, end: 2 })).toEqual({ start: 0, end: 2 })
    })

    it('clamps an entirely out-of-bounds range down to the length', () => {
      expect(clampPremeasureRange(5, { start: 9, end: 20 })).toEqual({ start: 5, end: 5 })
    })

    it('never lets end fall below start (an inverted range collapses to empty at start)', () => {
      expect(clampPremeasureRange(8, { start: 6, end: 2 })).toEqual({ start: 6, end: 6 })
    })

    it('clamps any range to empty at 0 when no row is loaded', () => {
      expect(clampPremeasureRange(0, { start: 0, end: 3 })).toEqual({ start: 0, end: 0 })
    })
  })

  describe('collectUnmeasuredCandidates', () => {
    const rows = [
      row('a'),
      row('b'),
      row('c'),
      row('d'),
      row('e'),
    ]

    it('collects only the unmeasured rows in [from, to), as the row objects themselves', () => {
      const measured = new Set(['b'])
      const out = collectUnmeasuredCandidates(rows, item => measured.has(item.id), 0, 4)
      expect(out.map(c => c.item.id)).toEqual(['a', 'c', 'd'])
      // A candidate is the object of its row, so a hidden copy keeps its row object.
      expect(out[0]).toBe(rows[0])
      expect(out[1]).toBe(rows[2])
    })

    it('excludes the [skipFrom, skipTo) sub-range another band already covers', () => {
      const out = collectUnmeasuredCandidates(rows, () => false, 0, 5, 1, 3)
      // indices 1,2 skipped -> a, d, e
      expect(out.map(c => c.item.id)).toEqual(['a', 'd', 'e'])
    })

    it('does not skip anything with the default (empty) skip range', () => {
      const out = collectUnmeasuredCandidates(rows, () => false, 0, 5)
      expect(out.map(c => c.item.id)).toEqual(['a', 'b', 'c', 'd', 'e'])
    })

    it('returns empty when every row is already measured', () => {
      expect(collectUnmeasuredCandidates(rows, () => true, 0, 5)).toEqual([])
    })

    it('is empty for an empty range', () => {
      expect(collectUnmeasuredCandidates(rows, () => false, 3, 3)).toEqual([])
    })
  })

  describe('unmeasuredCandidateAt', () => {
    const rows = ['a', 'b'].map(row)

    it('gives the object of an unmeasured row', () => {
      expect(unmeasuredCandidateAt(rows, () => false, 1)).toBe(rows[1])
    })

    it('gives nothing for a measured row', () => {
      expect(unmeasuredCandidateAt(rows, item => item.id === 'b', 1)).toBeUndefined()
    })

    it.each([-1, 2])('gives nothing for the index %i, which holds no row', (index) => {
      expect(unmeasuredCandidateAt(rows, () => false, index)).toBeUndefined()
    })
  })

  describe('createChatPremeasureBands', () => {
    /** Every row index of the harness. */
    const allRows = Array.from({ length: 30 }, (_, index) => index)
    // With the rows 10 to 14 rendered, the look-ahead band holds r0 to r9 and r15 to r26:
    // 12 rows on each side of the rendered range, and no row of the range.
    const lookAheadIds = [
      ...Array.from({ length: 10 }, (_, index) => `r${index}`),
      ...Array.from({ length: 12 }, (_, index) => `r${15 + index}`),
    ]

    /**
     * The bands over 30 loaded rows `r0` to `r29` with a real height cache. No row is
     * measured, no target waits, no row is rendered, staging is enabled, and the idle
     * warm-up is off, unless the test sets it. `measure` commits a height for the rows at
     * the given indexes. `setEntries` and `setItems` replace the entries and the items of
     * the loaded rows, as the memos of ChatView do.
     */
    function mountBands(options: { warmupEnabled?: boolean } = {}) {
      const ids = Array.from({ length: 30 }, (_, index) => `r${index}`)
      const [items, setItems] = createSignal<readonly ChatRowItem[]>(
        ids.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` })),
      )
      const [entries, setEntries] = createSignal<readonly ClassifiedEntry[]>(ids.map(id => ({
        message: { id },
      } as ClassifiedEntry)))
      const heights = createRowHeightCache({
        loadedIds: () => ids,
      })
      const [renderedRange, setRenderedRange] = createSignal<ChatVirtualizerRange>({ start: 0, end: 0 })
      const [wantedRows, setWantedRows] = createSignal<readonly number[]>([])
      const [stagingEnabled, setStagingEnabled] = createSignal(true)
      let dispose!: () => void
      const bands = createRoot((disposeRoot) => {
        dispose = disposeRoot
        return createChatPremeasureBands({
          visibleEntries: entries,
          loadedItems: items,
          heights,
          renderedRange,
          wantedRows,
          stagingEnabled,
          warmupEnabled: () => options.warmupEnabled ?? false,
        })
      })
      return {
        bands,
        setItems,
        setEntries,
        setRenderedRange,
        setWantedRows,
        setStagingEnabled,
        measure: (indexes: readonly number[]) => {
          for (const index of indexes) {
            const item = items()[index]!
            heights.commit(item, 100, item.heightKey, 'premeasure')
          }
        },
        candidateIds: () => bands.premeasureCandidates().map(candidate => candidate.item.id),
        priorityIds: () => [
          ...bands.priorityIds(),
        ],
        dispose,
      }
    }

    it('stages the rows that a waiting target wants, in the order of its scan', () => {
      const h = mountBands()
      h.setWantedRows([29, 28])

      // The priority set keeps the order of the target band. The candidates are in display
      // order, which is the order of the hidden copies.
      expect(h.priorityIds()).toEqual(['r29', 'r28'])
      expect(h.candidateIds()).toEqual(['r28', 'r29'])
      h.dispose()
    })

    it('skips a wanted index that lies past the loaded rows', () => {
      const h = mountBands()
      h.setWantedRows([29, 30, -1])

      expect(h.priorityIds()).toEqual(['r29'])
      h.dispose()
    })

    it('stages no look-ahead row while no row is rendered', () => {
      const h = mountBands()
      h.setRenderedRange({ start: 0, end: 0 })

      expect(h.candidateIds()).toEqual([])
      expect(h.priorityIds()).toEqual([])
      h.dispose()
    })

    it('stages the unmeasured rows around the rendered range, in indexes of the loaded rows', () => {
      const h = mountBands()
      // Every row is measured except the two on each side of the rendered range.
      h.measure(allRows.filter(index => ![8, 9, 15, 16].includes(index)))
      h.setRenderedRange({ start: 10, end: 15 })

      expect(h.candidateIds()).toEqual(['r8', 'r9', 'r15', 'r16'])
      h.dispose()
    })

    it('skips the rendered range in the look-ahead band', () => {
      const h = mountBands()
      h.setRenderedRange({ start: 10, end: 15 })

      expect(h.candidateIds()).toEqual(lookAheadIds)
      h.dispose()
    })

    it('gives no candidate in any band while staging is not enabled', () => {
      const h = mountBands()
      h.setRenderedRange({ start: 10, end: 15 })
      h.setStagingEnabled(false)
      h.setWantedRows([29])

      expect(h.candidateIds()).toEqual([])
      expect(h.priorityIds()).toEqual([])

      // The rows arrive when staging is enabled again.
      h.setStagingEnabled(true)
      expect(h.priorityIds()[0]).toBe('r29')
      expect(h.candidateIds()).toContain('r29')
      h.dispose()
    })

    it('lists the target rows and the look-ahead rows as priority rows', () => {
      const h = mountBands()
      h.measure(allRows.filter(index => index !== 3 && index !== 16))
      h.setRenderedRange({ start: 10, end: 15 })
      h.setWantedRows([3])

      // r3 is the target row, and r16 lies in the look-ahead band.
      expect(h.priorityIds()).toEqual(['r3', 'r16'])
      h.dispose()
    })

    // ChatHiddenPremeasure's <For> keys its copies by object identity, so a new object for
    // an unchanged row mounts its whole hidden copy again.
    describe('the object of a loaded row', () => {
      it('stays the same while the entry and the height key of the row stay the same', () => {
        const h = mountBands()
        h.setWantedRows([0, 1])
        const candidates = h.bands.premeasureCandidates()
        const [first, second] = candidates

        // ChatView rebuilds every item object, and it gives a new array of the same entries.
        h.setItems(prev => prev.map(item => ({ ...item })))
        h.setEntries(prev => [...prev])

        expect(h.bands.premeasureCandidates()[0]).toBe(first)
        expect(h.bands.premeasureCandidates()[1]).toBe(second)
        // No row got a new object, so no reader of the loaded rows ran again.
        expect(h.bands.premeasureCandidates()).toBe(candidates)
        h.dispose()
      })

      it('is new when the height key of the row changes', () => {
        const h = mountBands()
        h.setWantedRows([0, 1])
        const [first, second] = h.bands.premeasureCandidates()

        h.setItems(prev => prev.map(item => (item.id === 'r0'
          ? { ...item, heightKey: 'k-r0-2' }
          : item)))

        const rekeyed = h.bands.premeasureCandidates()
        expect(rekeyed[0]).not.toBe(first)
        expect(rekeyed[0]?.item.heightKey).toBe('k-r0-2')
        expect(rekeyed[1]).toBe(second)
        h.dispose()
      })

      it('is new when the entry of the row changes', () => {
        const h = mountBands()
        h.setWantedRows([0, 1])
        const [first, second] = h.bands.premeasureCandidates()

        const rebuilt = {
          message: { id: 'r1' },
        } as ClassifiedEntry
        h.setEntries(prev => prev.map(entry => (entry.message.id === 'r1'
          ? rebuilt
          : entry)))

        const reclassified = h.bands.premeasureCandidates()
        expect(reclassified[0]).toBe(first)
        expect(reclassified[1]).not.toBe(second)
        expect(reclassified[1]?.entry).toBe(rebuilt)
        h.dispose()
      })
    })

    describe('with the idle warm-up on', () => {
      /** How long the stand-in idle callback takes to arrive. */
      const IDLE_LATENCY_MS = 1
      /** The time from the start of a warm-up to its first batch. */
      const FIRST_BATCH_MS = WARMUP_IDLE_DELAY_MS + IDLE_LATENCY_MS
      beforeEach(() => {
        vi.useFakeTimers()
        vi.stubGlobal('requestIdleCallback', (callback: IdleRequestCallback) => setTimeout(() => callback({
          didTimeout: false,
          timeRemaining: () => 50,
        }), IDLE_LATENCY_MS))
        vi.stubGlobal('cancelIdleCallback', (handle: ReturnType<typeof setTimeout>) => clearTimeout(handle))
      })

      afterEach(() => {
        vi.useRealTimers()
        vi.unstubAllGlobals()
      })

      it('gives a warm-up row as a candidate, and never as a priority row', () => {
        const h = mountBands({ warmupEnabled: true })
        // r3 is the target row, r16 lies in the look-ahead band, and r28 lies outside it.
        h.measure(allRows.filter(index => ![3, 16, 28].includes(index)))
        h.setRenderedRange({ start: 10, end: 15 })
        h.setWantedRows([3])
        expect(h.candidateIds()).toEqual(['r3', 'r16'])

        vi.advanceTimersByTime(FIRST_BATCH_MS)

        // The warm-up reached r28. The worker priority stays with the rows that the reader
        // waits for or reaches next.
        expect(h.candidateIds()).toEqual(['r3', 'r16', 'r28'])
        expect(h.priorityIds()).toEqual(['r3', 'r16'])
        h.dispose()
      })

      it('starts the warm-up at an empty rendered range, where the look-ahead band covers no row', () => {
        const h = mountBands({ warmupEnabled: true })
        h.setRenderedRange({ start: 5, end: 5 })

        vi.advanceTimersByTime(FIRST_BATCH_MS)

        // The batch alternates outward from index 5: r4, r5, r3, r6, and so on. The
        // candidates are in display order.
        expect(h.candidateIds()).toEqual([
          'r1',
          'r2',
          'r3',
          'r4',
          'r5',
          'r6',
          'r7',
          'r8',
        ])
        expect(h.priorityIds()).toEqual([])
        h.dispose()
      })

      it('runs no warm-up while staging is not enabled', () => {
        const h = mountBands({ warmupEnabled: true })
        // With the rows 10 to 14 rendered, the rows r27 to r29 lie outside the look-ahead
        // band, so they are left for the warm-up.
        h.setRenderedRange({ start: 10, end: 15 })
        h.setStagingEnabled(false)
        // Long enough for several batches of a warm-up that ran.
        vi.advanceTimersByTime(FIRST_BATCH_MS * 5)
        expect(h.candidateIds()).toEqual([])
        expect(h.priorityIds()).toEqual([])

        h.setStagingEnabled(true)

        // The look-ahead rows arrive at once. No warm-up row does: the warm-up collected
        // none during the pause, and it waits for idle time again.
        expect(h.candidateIds()).toEqual(lookAheadIds)
        vi.advanceTimersByTime(FIRST_BATCH_MS)
        expect(h.candidateIds()).toEqual([
          ...lookAheadIds,
          'r27',
          'r28',
          'r29',
        ])
        h.dispose()
      })
    })
  })
})
