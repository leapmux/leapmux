import type { ClassifiedEntry } from './chatEntryCache'
import type { VirtualItem } from './useChatVirtualizer'
import { describe, expect, it } from 'vitest'
import { clampPremeasureRange, collectUnmeasuredCandidates } from './chatPremeasureBands'

describe('chatpremeasurebands', () => {
  // The collector reads only `item.id`; the entry is opaque (passed straight through), so a
  // marker object suffices and lets the tests assert index-aligned entry<->item pairing.
  const item = (id: string): VirtualItem => ({ id, hasSpanLines: false })
  const entry = (id: string): ClassifiedEntry => ({ marker: id } as unknown as ClassifiedEntry)

  describe('clamppremeasurerange', () => {
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

  describe('collectunmeasuredcandidates', () => {
    const all = [entry('a'), entry('b'), entry('c'), entry('d'), entry('e')]
    const items = [item('a'), item('b'), item('c'), item('d'), item('e')]

    it('collects only the unmeasured rows in [from, to), pairing each entry with its item', () => {
      const measured = new Set(['b'])
      const out = collectUnmeasuredCandidates(all, items, id => measured.has(id), 0, 4)
      expect(out.map(c => c.item.id)).toEqual(['a', 'c', 'd'])
      // Index-aligned pairing: candidate N carries the entry AND item at the same index.
      expect(out[0].entry).toBe(all[0])
      expect(out[1].entry).toBe(all[2])
      expect(out[1].item).toBe(items[2])
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
})
