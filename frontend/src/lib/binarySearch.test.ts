import { describe, expect, it } from 'vitest'
import { largestIndexWhere, lowerBoundBySeq, smallestIndexWhere } from './binarySearch'

describe('binarysearch', () => {
  describe('largestindexwhere', () => {
    // Non-decreasing array; find the largest index with arr[i] <= target.
    const arr = [0, 10, 20, 30, 40]
    it('finds the largest index whose value is <= target', () => {
      expect(largestIndexWhere(arr.length, i => arr[i] <= 25)).toBe(2) // 20
      expect(largestIndexWhere(arr.length, i => arr[i] <= 30)).toBe(3) // exact boundary
      expect(largestIndexWhere(arr.length, i => arr[i] <= 999)).toBe(4) // last
    })

    it('returns 0 when the predicate holds for nothing (below the floor)', () => {
      expect(largestIndexWhere(arr.length, i => arr[i] <= -1)).toBe(0)
    })

    it('handles a single-element and matches a linear scan across the range', () => {
      expect(largestIndexWhere(1, () => true)).toBe(0)
      for (let target = -5; target <= 45; target += 3) {
        const pred = (i: number) => arr[i] <= target
        let expected = 0
        for (let i = 0; i < arr.length; i++) {
          if (pred(i))
            expected = i
        }
        expect(largestIndexWhere(arr.length, pred)).toBe(expected)
      }
    })
  })

  describe('smallestindexwhere', () => {
    const arr = [0, 10, 20, 30, 40]
    it('finds the smallest index whose value is >= target', () => {
      expect(smallestIndexWhere(arr.length, i => arr[i] >= 15, arr.length - 1)).toBe(2) // 20
      expect(smallestIndexWhere(arr.length, i => arr[i] >= 20, arr.length - 1)).toBe(2) // exact
      expect(smallestIndexWhere(arr.length, i => arr[i] >= 0, arr.length - 1)).toBe(0) // first
    })

    it('returns the fallback when the predicate holds for nothing', () => {
      expect(smallestIndexWhere(arr.length, i => arr[i] >= 999, 42)).toBe(42)
    })
  })

  describe('lowerboundbyseq', () => {
    const items = [{ seq: 2n }, { seq: 5n }, { seq: 5n }, { seq: 9n }] as const

    it('finds the first index whose seq is >= target (membership + gap)', () => {
      expect(lowerBoundBySeq(items, 5n)).toBe(1) // first of the duplicate 5s
      expect(lowerBoundBySeq(items, 2n)).toBe(0) // first
      expect(lowerBoundBySeq(items, 9n)).toBe(3) // last
      expect(lowerBoundBySeq(items, 6n)).toBe(3) // gap -> next-higher (insertion point)
      expect(lowerBoundBySeq(items, 1n)).toBe(0) // below the floor
    })

    it('returns the length (insertion point) when every seq is smaller', () => {
      expect(lowerBoundBySeq(items, 10n)).toBe(items.length)
    })

    it('handles the empty list', () => {
      expect(lowerBoundBySeq([], 5n)).toBe(0)
    })

    it('restricts the search to the [0, hi) prefix, ignoring trailing rows', () => {
      // The prefix bound excludes indices >= hi entirely, so a target present only in the
      // excluded suffix lands at hi (the insertion point), never scanning the trailing rows --
      // this is the "server region, excluding trailing optimistic locals" call shape.
      const withTail = [{ seq: 2n }, { seq: 5n }, { seq: 0n }, { seq: 0n }] as const
      expect(lowerBoundBySeq(withTail, 5n, 2)).toBe(1)
      expect(lowerBoundBySeq(withTail, 9n, 2)).toBe(2) // hi, not the array length
    })

    it('matches a linear lower-bound scan across the range', () => {
      const asc: { seq: bigint }[] = [{ seq: 0n }, { seq: 3n }, { seq: 3n }, { seq: 8n }, { seq: 100n }]
      for (let t = -2n; t <= 102n; t += 1n) {
        let expected = asc.length
        for (let i = 0; i < asc.length; i++) {
          if (asc[i].seq >= t) {
            expected = i
            break
          }
        }
        expect(lowerBoundBySeq(asc, t)).toBe(expected)
      }
    })
  })
})
