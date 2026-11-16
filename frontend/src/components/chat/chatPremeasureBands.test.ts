import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatVirtualizerRange, VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { clampPremeasureRange, collectUnmeasuredCandidates, createChatPremeasureBands } from './chatPremeasureBands'
import { WARMUP_IDLE_DELAY_MS } from './chatPremeasureWarmup'
import { createRowHeightCache } from './chatRowHeightCache'

describe('chatPremeasureBands', () => {
  // The collector reads only `item.id`; the entry is opaque (passed straight through), so a
  // marker object suffices and lets the tests assert index-aligned entry<->item pairing.
  const item = (id: string): VirtualItem => ({ id, hasSpanLines: false })
  const entry = (id: string): ClassifiedEntry => ({ marker: id } as unknown as ClassifiedEntry)

  describe('clampPremeasureRange', () => {
    it('passes an in-bounds range through unchanged', () => {
      expect(clampPremeasureRange(10, 10, { start: 2, end: 6 })).toEqual({ len: 10, start: 2, end: 6 })
    })

    it('uses the shorter of the two array lengths as len and clamps to it', () => {
      // items shorter than entries: the common length bounds the window.
      expect(clampPremeasureRange(10, 4, { start: 1, end: 8 })).toEqual({ len: 4, start: 1, end: 4 })
    })

    it('clamps an entirely out-of-bounds range down to the length', () => {
      expect(clampPremeasureRange(5, 5, { start: 9, end: 20 })).toEqual({ len: 5, start: 5, end: 5 })
    })

    it('never lets end fall below start (an inverted range collapses to empty at start)', () => {
      expect(clampPremeasureRange(8, 8, { start: 6, end: 2 })).toEqual({ len: 8, start: 6, end: 6 })
    })

    it('handles empty arrays', () => {
      expect(clampPremeasureRange(0, 0, { start: 0, end: 3 })).toEqual({ len: 0, start: 0, end: 0 })
    })
  })

  describe('collectUnmeasuredCandidates', () => {
    const all = [
      entry('a'),
      entry('b'),
      entry('c'),
      entry('d'),
      entry('e'),
    ]
    const items = [
      item('a'),
      item('b'),
      item('c'),
      item('d'),
      item('e'),
    ]

    it('collects only the unmeasured rows in [from, to), pairing each entry with its item', () => {
      const measured = new Set(['b'])
      const out = collectUnmeasuredCandidates(all, items, row => measured.has(row.id), 0, 4)
      expect(out.map(c => c.item.id)).toEqual(['a', 'c', 'd'])
      // Index-aligned pairing: candidate N carries the entry AND item at the same index.
      // `?.` is the type-level guard alone; toBe fails just as hard on an absent candidate.
      expect(out[0]?.entry).toBe(all[0])
      expect(out[1]?.entry).toBe(all[2])
      expect(out[1]?.item).toBe(items[2])
    })

    it('excludes the [skipFrom, skipTo) sub-range another band already covers', () => {
      const out = collectUnmeasuredCandidates(all, items, () => false, 0, 5, 1, 3)
      // indices 1,2 skipped -> a, d, e
      expect(out.map(c => c.item.id)).toEqual(['a', 'd', 'e'])
    })

    it('does not skip anything with the default (empty) skip range', () => {
      const out = collectUnmeasuredCandidates(all, items, () => false, 0, 5)
      expect(out.map(c => c.item.id)).toEqual(['a', 'b', 'c', 'd', 'e'])
    })

    it('returns empty when every row is already measured', () => {
      expect(collectUnmeasuredCandidates(all, items, () => true, 0, 5)).toEqual([])
    })

    it('skips index positions missing an entry or item', () => {
      // A short items array leaves later indices without a paired item -> those are skipped.
      const out = collectUnmeasuredCandidates(all, items.slice(0, 2), () => false, 0, 5)
      expect(out.map(c => c.item.id)).toEqual(['a', 'b'])
    })

    it('is empty for an empty range', () => {
      expect(collectUnmeasuredCandidates(all, items, () => false, 3, 3)).toEqual([])
    })
  })

  describe('createChatPremeasureBands', () => {
    /**
     * The bands over 30 loaded rows `r0` to `r29` with a real height cache. No row is
     * measured, no target waits, no row is rendered, staging is enabled, and the idle
     * warm-up is off, unless the test sets it. `measure` commits a height for the rows at
     * the given indexes.
     */
    function mountBands(options: { warmupEnabled?: boolean } = {}) {
      const ids = Array.from({ length: 30 }, (_, index) => `r${index}`)
      const rows: VirtualItem[] = ids.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` }))
      const entries = ids.map(id => ({
        message: { id },
      } as ClassifiedEntry))
      const entryById = new Map(entries.map(entry => [entry.message.id, entry]))
      const heights = createRowHeightCache()
      const [renderedRange, setRenderedRange] = createSignal<ChatVirtualizerRange>({ start: 0, end: 0 })
      const [wantedRows, setWantedRows] = createSignal<readonly number[]>([])
      const [stagingEnabled, setStagingEnabled] = createSignal(true)
      let dispose!: () => void
      const bands = createRoot((disposeRoot) => {
        dispose = disposeRoot
        return createChatPremeasureBands({
          visibleEntries: () => entries,
          virtualItems: () => rows,
          heights,
          renderedRange,
          wantedRows,
          stagingEnabled,
          visibleEntryById: () => entryById,
          warmupEnabled: () => options.warmupEnabled ?? false,
        })
      })
      return {
        bands,
        setRenderedRange,
        setWantedRows,
        setStagingEnabled,
        measure: (indexes: readonly number[]) => {
          for (const index of indexes)
            heights.commit(rows[index]!, 100, rows[index]!.heightKey, 'premeasure')
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
      h.measure(Array.from({ length: 30 }, (_, index) => index).filter(index => ![8, 9, 15, 16].includes(index)))
      h.setRenderedRange({ start: 10, end: 15 })

      expect(h.candidateIds()).toEqual(['r8', 'r9', 'r15', 'r16'])
      h.dispose()
    })

    it('skips the rendered range in the look-ahead band', () => {
      const h = mountBands()
      h.setRenderedRange({ start: 10, end: 15 })

      // 12 rows on each side of the rendered range, and no row of the range.
      expect(h.candidateIds()).toEqual([
        ...Array.from({ length: 10 }, (_, index) => `r${index}`),
        ...Array.from({ length: 12 }, (_, index) => `r${15 + index}`),
      ])
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
      h.measure(Array.from({ length: 30 }, (_, index) => index).filter(index => index !== 3 && index !== 16))
      h.setRenderedRange({ start: 10, end: 15 })
      h.setWantedRows([3])

      // r3 is the target row, and r16 lies in the look-ahead band.
      expect(h.priorityIds()).toEqual(['r3', 'r16'])
      h.dispose()
    })

    describe('with the idle warm-up on', () => {
      /** How long the stand-in idle callback takes to arrive. */
      const IDLE_LATENCY_MS = 1
      /** The time from the start of a warm-up to its first batch. */
      const FIRST_BATCH_MS = WARMUP_IDLE_DELAY_MS + IDLE_LATENCY_MS
      /** Every row index of the harness. */
      const allRows = Array.from({ length: 30 }, (_, index) => index)

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

      it('runs no warm-up while staging is not enabled', () => {
        const h = mountBands({ warmupEnabled: true })
        // With the rows 10 to 14 rendered, the look-ahead band holds r0 to r9 and r15 to
        // r26, and the rows r27 to r29 are left for the warm-up.
        const lookAheadIds = [
          ...Array.from({ length: 10 }, (_, index) => `r${index}`),
          ...Array.from({ length: 12 }, (_, index) => `r${15 + index}`),
        ]
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
