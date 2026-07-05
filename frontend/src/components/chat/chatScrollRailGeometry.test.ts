import type { PreparedGeometry, SeqSpaceGeometry } from './chatScrollRailGeometry'
import type { VirtualItem } from './useChatVirtualizer'
import { describe, expect, it } from 'vitest'
// dotFraction lives in chatRailPolicy (rail decision), but it's tested here alongside its
// geometry siblings fractionToSeq / railYToSeq because they share the seq-range fail-closed
// contract; the clustering / owner-resolution policy tests live in chatRailPolicy.test.ts.
import { dotFraction } from './chatRailPolicy'
import {
  centerAxisFraction,
  centerAxisY,
  computeSeqThumb,
  contentYForSeq,
  dragThumbPx,
  fixedThumbHeightPx,
  fractionToSeq,
  prepareGeometry,
  projectThumbPx,
  railYToSeq,
  rowStartSeqs,
  seqAtContentY,
  seqNumberAtFraction,
  seqSpan,
} from './chatScrollRailGeometry'

/** Build a raw geometry with uniform-height rows for the given seqs (0n = optimistic local). */
function geoOf(seqs: bigint[], rowPx = 100): SeqSpaceGeometry {
  const items: VirtualItem[] = seqs.map((seq, i) => ({ id: `m${i}`, hasSpanLines: false, seq }))
  return {
    items,
    offsetOfIndex: i => i * rowPx,
    totalHeight: seqs.length * rowPx,
  }
}

/** A PreparedGeometry (geo + its precomputed rowStartSeqs) for the given seqs. */
function prepOf(seqs: bigint[], rowPx = 100): PreparedGeometry {
  return prepareGeometry(geoOf(seqs, rowPx))
}

describe('chatscrollrailgeometry', () => {
  describe('seq at content y', () => {
    it('returns the row seq at a row boundary and interpolates within a row', () => {
      const prep = prepOf([1n, 2n, 3n, 4n, 5n])
      expect(seqAtContentY(prep, 0)).toBe(1)
      expect(seqAtContentY(prep, 100)).toBe(2)
      expect(seqAtContentY(prep, 150)).toBe(2.5) // halfway through row 1 (seq 2 -> 3)
      // The very bottom maps to the terminal boundary (maxSeq + 1), i.e. fraction 1 later.
      expect(seqAtContentY(prep, 500)).toBe(6)
    })

    it('absorbs seq gaps (deleted/hidden rows) linearly between visible rows', () => {
      // seqs jump 3 -> 7: the pixel span between them covers seqs [3,7).
      const prep = prepOf([1n, 3n, 7n])
      expect(seqAtContentY(prep, 100)).toBe(3)
      expect(seqAtContentY(prep, 150)).toBe(5) // halfway from seq 3 to seq 7
      expect(seqAtContentY(prep, 200)).toBe(7)
    })

    it('maps trailing optimistic locals above the last server seq (never as the oldest)', () => {
      const prep = prepOf([4n, 5n, 0n]) // a pending local at the tail
      expect(seqAtContentY(prep, 0)).toBe(4)
      expect(seqAtContentY(prep, 200)).toBe(6) // local sits one unit above seq 5
    })

    it('returns null for an all-locals / empty window', () => {
      expect(seqAtContentY(prepOf([0n, 0n]), 50)).toBeNull()
      expect(seqAtContentY(prepOf([]), 0)).toBeNull()
    })
  })

  describe('content y for seq (inverse)', () => {
    it('round-trips with seqAtContentY within the loaded window', () => {
      const prep = prepOf([1n, 2n, 3n, 4n, 5n])
      for (const y of [0, 50, 150, 250, 399]) {
        const seqF = seqAtContentY(prep, y)!
        expect(contentYForSeq(prep, seqF)).toBeCloseTo(y, 5)
      }
    })

    it('returns null for a seq outside the loaded window span', () => {
      const prep = prepOf([10n, 11n, 12n])
      expect(contentYForSeq(prep, 5)).toBeNull()
      expect(contentYForSeq(prep, 99)).toBeNull()
    })
  })

  describe('compute seq thumb', () => {
    const base = { hasMoreOlder: false, hasMoreNewer: false, distFromBottomPx: 999 }

    it('computes the viewport share when the whole conversation is loaded', () => {
      const thumb = computeSeqThumb(prepOf([1n, 2n, 3n, 4n, 5n]), { ...base, scrollTop: 0, clientHeight: 200, minSeq: 1n, maxSeq: 5n })!
      expect(thumb.topFraction).toBeCloseTo(0, 5)
      expect(thumb.visibleFraction).toBeCloseTo(0.4, 5) // viewing seqs 1..3 of 5
    })

    it('reports a smaller visible seq span when the loaded window is a slice of remote history', () => {
      // Loaded window = seqs 10..12 (fits the viewport), whole conversation = 1..100.
      const thumb = computeSeqThumb(prepOf([10n, 11n, 12n]), {
        scrollTop: 0,
        clientHeight: 500, // >= totalHeight (300): the native scrollbar would show full height
        minSeq: 1n,
        maxSeq: 100n,
        hasMoreOlder: true,
        hasMoreNewer: true,
        distFromBottomPx: 999,
      })!
      expect(thumb.topFraction).toBeCloseTo(0.09, 5) // window starts ~9% into the history
      expect(thumb.visibleFraction).toBeCloseTo(0.03, 5) // 3 of 100 seqs
    })

    it('snaps to the top/bottom edges when the window is at the true start/end', () => {
      const thumb = computeSeqThumb(prepOf([1n, 2n, 3n, 4n, 5n]), {
        scrollTop: 0,
        clientHeight: 500,
        minSeq: 1n,
        maxSeq: 5n,
        hasMoreOlder: false,
        hasMoreNewer: false,
        distFromBottomPx: 0, // at the bottom
      })!
      expect(thumb.topFraction).toBe(0)
      expect(thumb.topFraction + thumb.visibleFraction).toBe(1)
    })

    it('returns null for an empty conversation and a full thumb for a single seq', () => {
      expect(computeSeqThumb(prepOf([1n]), { ...base, scrollTop: 0, clientHeight: 100, minSeq: 0n, maxSeq: 0n })).toBeNull()
      const single = computeSeqThumb(prepOf([7n]), { ...base, scrollTop: 0, clientHeight: 100, minSeq: 7n, maxSeq: 7n })!
      expect(single.topFraction).toBe(0)
      expect(single.visibleFraction).toBeCloseTo(1, 5)
    })

    it('returns null (never a NaN thumb) for an inverted range where maxSeq < minSeq', () => {
      // A stale minSeq stranded above a delete-lowered maxSeq makes span <= 0, which without
      // the guard yields NaN fractions -> the thumb/track render at `NaNpx`. Hide instead.
      const thumb = computeSeqThumb(prepOf([3n, 4n]), { ...base, scrollTop: 0, clientHeight: 100, minSeq: 9n, maxSeq: 4n })
      expect(thumb).toBeNull()
    })
  })

  describe('fraction to seq / dot fraction / rail y to seq', () => {
    it('fractionToSeq maps clamped fractions to the nearest seq', () => {
      expect(fractionToSeq(0, 1n, 101n)).toBe(1n)
      expect(fractionToSeq(1, 1n, 101n)).toBe(101n)
      expect(fractionToSeq(0.5, 1n, 101n)).toBe(51n)
      expect(fractionToSeq(-1, 1n, 101n)).toBe(1n) // clamped
      expect(fractionToSeq(0.5, 5n, 5n)).toBe(5n) // degenerate range
    })

    it('fractionToSeq treats a NaN fraction as the top rather than throwing BigInt(NaN)', () => {
      // A 0/0 from a degenerate rail/rect height reaches here as NaN; it must not throw.
      expect(() => fractionToSeq(Number.NaN, 1n, 101n)).not.toThrow()
      expect(fractionToSeq(Number.NaN, 1n, 101n)).toBe(1n)
    })

    it('seqNumberAtFraction maps a fraction to the absolute fractional seq, failing closed', () => {
      // The shared fail-closed core: fractionToSeq rounds it; the thumb-drag maps it to content-Y.
      expect(seqNumberAtFraction(0, 1n, 101n)).toBe(1)
      expect(seqNumberAtFraction(1, 1n, 101n)).toBe(101)
      expect(seqNumberAtFraction(0.5, 1n, 101n)).toBe(51)
      expect(seqNumberAtFraction(-1, 1n, 101n)).toBe(1) // f clamped to [0,1]
      expect(seqNumberAtFraction(2, 1n, 101n)).toBe(101) // f clamped to [0,1]
      expect(seqNumberAtFraction(Number.NaN, 1n, 101n)).toBe(1) // NaN -> 0, never BigInt(NaN) downstream
      expect(seqNumberAtFraction(0.5, 5n, 5n)).toBe(5) // degenerate single-seq range: span 0
      expect(seqNumberAtFraction(0.5, 10n, 1n)).toBeNull() // inverted range
      expect(seqNumberAtFraction(0.5, 1n, BigInt(Number.MAX_SAFE_INTEGER) + 2n)).toBeNull() // unsafe span
    })

    it('fractionToSeq is seqNumberAtFraction rounded to a bigint (one shared travel core)', () => {
      for (const f of [0, 0.13, 0.5, 0.87, 1]) {
        const num = seqNumberAtFraction(f, 1n, 101n)
        expect(num).not.toBeNull()
        expect(fractionToSeq(f, 1n, 101n)).toBe(BigInt(Math.round(num!)))
      }
    })

    it('dotFraction centers a dot on its message band', () => {
      // span = (10 - 1) + 1 = 10; seq 1 -> 0.5/10, seq 10 -> 9.5/10
      expect(dotFraction(1n, { minSeq: 1n, maxSeq: 10n })).toBeCloseTo(0.05, 5)
      expect(dotFraction(10n, { minSeq: 1n, maxSeq: 10n })).toBeCloseTo(0.95, 5)
    })

    it('railYToSeq inverts a rail-relative pixel to a seq via the thumb-centre axis', () => {
      // thumbHeight 0 -> no inset, so the pixel maps over the full rail.
      expect(railYToSeq(0, 200, 0, { minSeq: 1n, maxSeq: 101n })).toBe(1n)
      expect(railYToSeq(100, 200, 0, { minSeq: 1n, maxSeq: 101n })).toBe(51n)
      expect(railYToSeq(200, 200, 0, { minSeq: 1n, maxSeq: 101n })).toBe(101n)
      // thumbHeight 24 -> the centre axis is [12, 188] over a 200px rail. y below/above it
      // clamps to the range ends; the midpoint (100) still maps to the middle seq.
      expect(railYToSeq(12, 200, 24, { minSeq: 1n, maxSeq: 101n })).toBe(1n)
      expect(railYToSeq(0, 200, 24, { minSeq: 1n, maxSeq: 101n })).toBe(1n) // above the axis -> clamped
      expect(railYToSeq(100, 200, 24, { minSeq: 1n, maxSeq: 101n })).toBe(51n)
      expect(railYToSeq(188, 200, 24, { minSeq: 1n, maxSeq: 101n })).toBe(101n)
      expect(railYToSeq(200, 200, 24, { minSeq: 1n, maxSeq: 101n })).toBe(101n) // below the axis -> clamped
    })

    it('handles huge bigint seqs by converting only deltas', () => {
      const big = 9_000_000_000n
      expect(fractionToSeq(0.5, big, big + 100n)).toBe(big + 50n)
      expect(dotFraction(big + 50n, { minSeq: big, maxSeq: big + 100n })).toBeCloseTo(0.5, 3)
    })

    it('fails closed when a fraction maps across an unsafe seq span', () => {
      const tooWide = BigInt(Number.MAX_SAFE_INTEGER) + 2n
      expect(fractionToSeq(0.5, 1n, tooWide)).toBeNull()
      expect(railYToSeq(100, 200, 0, { minSeq: 1n, maxSeq: tooWide })).toBeNull()
    })

    it('dotFraction fails CLOSED (null) on an unsafe/degenerate range, not fraction 0', () => {
      // An unsafe span or an inverted range must DROP the dot (null), never pin it to the rail
      // top (fraction 0, a valid in-band position that would mis-place the jump) -- honoring the
      // module's fail-closed contract shared with computeSeqThumb / fractionToSeq.
      const tooWide = BigInt(Number.MAX_SAFE_INTEGER) + 2n
      expect(dotFraction(5n, { minSeq: 1n, maxSeq: tooWide })).toBeNull()
      expect(dotFraction(5n, { minSeq: 10n, maxSeq: 1n })).toBeNull() // inverted range
    })
  })

  describe('seq span (shared inclusive-range metrics)', () => {
    it('returns min + inclusive band width, failing closed on degenerate/unsafe ranges', () => {
      expect(seqSpan({ minSeq: 1n, maxSeq: 10n })).toEqual({ min: 1, span: 10 })
      expect(seqSpan({ minSeq: 5n, maxSeq: 5n })).toEqual({ min: 5, span: 1 }) // single seq
      expect(seqSpan({ minSeq: 10n, maxSeq: 1n })).toBeNull() // inverted
      expect(seqSpan({ minSeq: 1n, maxSeq: BigInt(Number.MAX_SAFE_INTEGER) + 2n })).toBeNull() // unsafe span
    })
  })

  describe('center axis y / center axis fraction (thumb-centre axis)', () => {
    it('maps a fraction onto the thumb-centre travel [thumbHalf, railHeight - thumbHalf]', () => {
      // rail 400, thumb 24 -> centre travels [12, 388].
      expect(centerAxisY(0, 400, 24)).toBe(12)
      expect(centerAxisY(1, 400, 24)).toBe(388)
      expect(centerAxisY(0.5, 400, 24)).toBe(200)
      expect(centerAxisY(-1, 400, 24)).toBe(12) // clamped
    })

    it('degenerates to the rail midpoint when the thumb fills the rail (no travel)', () => {
      expect(centerAxisY(0, 400, 400)).toBe(200)
      expect(centerAxisY(1, 400, 400)).toBe(200)
      expect(centerAxisFraction(123, 400, 400)).toBe(0)
    })

    it('centerAxisFraction inverts centerAxisY', () => {
      expect(centerAxisFraction(12, 400, 24)).toBe(0)
      expect(centerAxisFraction(388, 400, 24)).toBe(1)
      expect(centerAxisFraction(200, 400, 24)).toBeCloseTo(0.5, 5)
      // Clamps outside the centre travel.
      expect(centerAxisFraction(0, 400, 24)).toBe(0)
      expect(centerAxisFraction(400, 400, 24)).toBe(1)
      // Round-trips.
      for (const p of [0, 0.25, 0.5, 0.75, 1])
        expect(centerAxisFraction(centerAxisY(p, 400, 24), 400, 24)).toBeCloseTo(p, 5)
    })
  })

  describe('project thumb px', () => {
    it('uses a fixed thumb height and projects its top over the full travel', () => {
      const bottom = projectThumbPx({ topFraction: 0.98, visibleFraction: 0.02 }, 400, 24)
      expect(bottom.heightPx).toBe(24)
      // At the very bottom, the fixed thumb still ends flush with the rail.
      expect(bottom.topPx + bottom.heightPx).toBeCloseTo(400, 3)

      const top = projectThumbPx({ topFraction: 0, visibleFraction: 0.02 }, 400, 24)
      expect(top.topPx).toBe(0)
      expect(top.heightPx).toBe(24)
    })

    it('keeps visible span out of thumb height while using it to project top', () => {
      const middle = projectThumbPx({ topFraction: 0.25, visibleFraction: 0.5 }, 400, 24)
      const bottom = projectThumbPx({ topFraction: 0.25, visibleFraction: 0.75 }, 400, 24)

      expect(middle.heightPx).toBe(24)
      expect(bottom.heightPx).toBe(24)
      expect(middle.topPx).toBeCloseTo(188, 6) // progress 0.5 over the 376px travel
      expect(bottom.topPx).toBeCloseTo(376, 6) // topFraction + visibleFraction == 1
    })

    it('caps the fixed thumb height at the rail height', () => {
      const full = projectThumbPx({ topFraction: 0, visibleFraction: 1 }, 12, 24)
      expect(full.heightPx).toBe(12)
      expect(full.topPx).toBe(0)
    })
  })

  describe('drag thumb px', () => {
    it('keeps the given thumb height and places its CENTRE on the centre axis, per fraction', () => {
      for (const f of [0, 0.25, 0.5, 0.75, 1]) {
        const { topPx, heightPx } = dragThumbPx(f, 400, 24)
        expect(heightPx).toBe(24)
        // The thumb centre lands exactly where dots at the same fraction are drawn.
        expect(topPx + heightPx / 2).toBeCloseTo(centerAxisY(f, 400, 24), 6)
      }
    })

    it('clamps an out-of-range fraction and never overruns the rail (top>=0, bottom<=rail)', () => {
      const below = dragThumbPx(-0.5, 400, 24)
      expect(below.topPx).toBe(0)
      const above = dragThumbPx(1.5, 400, 24)
      expect(above.topPx + above.heightPx).toBeCloseTo(400, 6)
    })
  })

  describe('fixed thumb height px', () => {
    it('uses the fixed thumb height and caps it at the rail height', () => {
      expect(fixedThumbHeightPx(400, 24)).toBe(24)
      expect(fixedThumbHeightPx(12, 24)).toBe(12)
    })

    it('is the exact height projectThumbPx uses, so the two cannot drift', () => {
      expect(projectThumbPx({ topFraction: 0.5, visibleFraction: 0.3 }, 400, 24).heightPx)
        .toBe(fixedThumbHeightPx(400, 24))
    })
  })

  describe('prepare geometry', () => {
    it('pairs a geometry with its rowStartSeqs, computed once, preserving the geo reference', () => {
      const geo = geoOf([10n, 11n, 12n])
      const prep = prepareGeometry(geo)
      expect(prep.geo).toBe(geo)
      expect(prep.rowSeqs).toEqual(rowStartSeqs(geo.items))
    })

    it('carries a null rowSeqs for an all-locals / empty window, so the geometry fns short-circuit', () => {
      expect(prepareGeometry(geoOf([0n, 0n])).rowSeqs).toBeNull()
      expect(prepareGeometry(geoOf([])).rowSeqs).toBeNull()
      // Every consumer then short-circuits on the single null-rowSeqs guard.
      expect(seqAtContentY(prepOf([0n, 0n]), 50)).toBeNull()
      expect(contentYForSeq(prepOf([]), 0)).toBeNull()
      expect(computeSeqThumb(prepOf([0n]), { hasMoreOlder: false, hasMoreNewer: false, distFromBottomPx: 999, scrollTop: 0, clientHeight: 100, minSeq: 1n, maxSeq: 5n })).toBeNull()
    })

    it('carries a null rowSeqs when server seqs exceed exact number conversion', () => {
      const unsafe = BigInt(Number.MAX_SAFE_INTEGER) + 1n
      expect(prepareGeometry(geoOf([unsafe])).rowSeqs).toBeNull()
      expect(seqAtContentY(prepOf([unsafe]), 0)).toBeNull()
      expect(prepareGeometry(geoOf([BigInt(Number.MAX_SAFE_INTEGER)])).rowSeqs).toBeNull()
    })
  })
})
