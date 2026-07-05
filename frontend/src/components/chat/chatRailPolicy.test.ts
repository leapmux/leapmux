import type { DotCluster } from './chatRailPolicy'
import type { VirtualItem } from './useChatVirtualizer'
import { describe, expect, it } from 'vitest'
import { MarkType } from '~/generated/leapmux/v1/agent_pb'
import { canRenderSeqRailThumb, clusterMarks, dotClustersEqual, nearestDotWithin, resolveScrollbarOwner } from './chatRailPolicy'
import { rowStartSeqs } from './chatScrollRailGeometry'

describe('chatrailpolicy', () => {
  describe('cluster marks', () => {
    it('places one dot per spread-out mark, centered on its band on the thumb-centre axis', () => {
      // rail 400, fixed thumb 24 -> centre travels [12, 388] (travel 376).
      // dotFraction(2,{1,5})=0.3 -> 12+0.3*376=124.8; dotFraction(4)=0.7 -> 275.2.
      const dots = clusterMarks(
        [{ seq: 2n, type: MarkType.USER_MESSAGE }, { seq: 4n, type: MarkType.CONTROL_RESPONSE }],
        { minSeq: 1n, maxSeq: 5n },
        400,
        24,
      )
      expect(dots).toEqual([
        { seq: 2n, topPx: 125, type: MarkType.USER_MESSAGE, count: 1 },
        { seq: 4n, topPx: 275, type: MarkType.CONTROL_RESPONSE, count: 1 },
      ])
    })

    it('collapses marks that round to the same pixel into ONE dot whose rep is nearest the pixel centre', () => {
      // seqs 500..502 of a huge history all round to px 14 on the [12,388] centre axis; 502's
      // exact position is nearest that pixel centre, so it is the cluster's representative.
      const dots = clusterMarks(
        [
          { seq: 500n, type: MarkType.USER_MESSAGE },
          { seq: 501n, type: MarkType.USER_MESSAGE },
          { seq: 502n, type: MarkType.USER_MESSAGE },
        ],
        { minSeq: 1n, maxSeq: 100_000n },
        400,
        24,
      )
      expect(dots).toEqual([{ seq: 502n, topPx: 14, type: MarkType.USER_MESSAGE, count: 3 }])
    })

    it('drops marks outside [minSeq, maxSeq]', () => {
      const dots = clusterMarks(
        [
          { seq: 1n, type: MarkType.USER_MESSAGE }, // below range
          { seq: 3n, type: MarkType.USER_MESSAGE }, // in range
          { seq: 9n, type: MarkType.USER_MESSAGE }, // above range
        ],
        { minSeq: 2n, maxSeq: 5n },
        400,
        0,
      )
      expect(dots.map(d => d.seq)).toEqual([3n])
    })

    it('skips marks when the range is unsafe (dotFraction fails closed) instead of piling them at fraction 0', () => {
      // A whole-range overflow makes every dotFraction null; clusterMarks must produce NO dots
      // rather than clustering them all at the rail top (fraction 0) -- the fail-closed contract.
      const tooWide = BigInt(Number.MAX_SAFE_INTEGER) + 2n
      const dots = clusterMarks(
        [{ seq: 5n, type: MarkType.USER_MESSAGE }, { seq: 9n, type: MarkType.CONTROL_RESPONSE }],
        { minSeq: 1n, maxSeq: tooWide },
        400,
        24,
      )
      expect(dots).toEqual([])
    })

    it('returns [] for a zero-height rail (nothing to place)', () => {
      expect(clusterMarks([{ seq: 2n, type: MarkType.USER_MESSAGE }], { minSeq: 1n, maxSeq: 5n }, 0, 0)).toEqual([])
    })

    it('returns [] for no marks', () => {
      expect(clusterMarks([], { minSeq: 1n, maxSeq: 5n }, 400, 0)).toEqual([])
    })

    it('includes marks sitting exactly on the range boundary (minSeq and maxSeq are inclusive)', () => {
      // thumb 0 -> centerAxisY(f,400,0) = f*400. span = (5-2)+1 = 4.
      // dotFraction(2)=0.5/4=0.125 -> 50; dotFraction(5)=3.5/4=0.875 -> 350.
      const dots = clusterMarks(
        [{ seq: 2n, type: MarkType.USER_MESSAGE }, { seq: 5n, type: MarkType.CONTROL_RESPONSE }],
        { minSeq: 2n, maxSeq: 5n },
        400,
        0,
      )
      expect(dots).toEqual([
        { seq: 2n, topPx: 50, type: MarkType.USER_MESSAGE, count: 1 },
        { seq: 5n, topPx: 350, type: MarkType.CONTROL_RESPONSE, count: 1 },
      ])
    })
  })

  describe('dot clusters equal', () => {
    const base: DotCluster[] = [
      { seq: 2n, topPx: 184, type: MarkType.USER_MESSAGE, count: 1 },
      { seq: 4n, topPx: 216, type: MarkType.CONTROL_RESPONSE, count: 3 },
    ]
    it('is true for content-equal arrays (so a +1 seq bump that keeps every pixel reuses the reference)', () => {
      expect(dotClustersEqual(base, base.map(d => ({ ...d })))).toBe(true)
    })
    it('is false when any dot field or the length differs', () => {
      expect(dotClustersEqual(base, [{ ...base[0] }, { ...base[1], topPx: 217 }])).toBe(false)
      expect(dotClustersEqual(base, [{ ...base[0] }, { ...base[1], count: 4 }])).toBe(false)
      expect(dotClustersEqual(base, [{ ...base[0] }, { ...base[1], seq: 5n }])).toBe(false)
      expect(dotClustersEqual(base, [base[0]])).toBe(false)
    })
  })

  describe('nearest dot within', () => {
    const dots: DotCluster[] = [
      { seq: 1n, topPx: 20, type: MarkType.USER_MESSAGE, count: 1 },
      { seq: 2n, topPx: 60, type: MarkType.USER_MESSAGE, count: 1 },
      { seq: 3n, topPx: 100, type: MarkType.CONTROL_RESPONSE, count: 2 },
    ]
    it('returns the dot within range nearest y (checking the lower-bound dot and its predecessor)', () => {
      expect(nearestDotWithin(dots, 58, 12)?.seq).toBe(2n) // 2px from the 60 dot
      expect(nearestDotWithin(dots, 100, 12)?.seq).toBe(3n) // exactly on the 100 dot
      expect(nearestDotWithin(dots, 12, 12)?.seq).toBe(1n) // 8px from the 20 dot (predecessor of idx 0 is absent)
    })
    it('returns null when no dot is within range', () => {
      expect(nearestDotWithin(dots, 40, 12)).toBeNull() // 20px from either neighbour, range 12
      expect(nearestDotWithin([], 40, 12)).toBeNull()
    })
    it('is inclusive at exactly rangePx and prefers the dot at/after y on a tie', () => {
      expect(nearestDotWithin(dots, 8, 12)?.seq).toBe(1n) // exactly 12px from the 20 dot
      // y midway (40) between the 20 and 60 dots: both are 20px away; range 20 admits both,
      // and the dot at/after y (60) wins the tie (checked second under `<=`).
      expect(nearestDotWithin(dots, 40, 20)?.seq).toBe(2n)
    })
  })

  describe('resolvescrollbarowner', () => {
    const items = (seqs: bigint[]): VirtualItem[] => seqs.map((seq, i) => ({ id: `m${i}`, hasSpanLines: false, seq }))
    // The window-shape half of ScrollbarOwnerInputs (itemCount + precomputed rowSeqs), so a test
    // states a window as its seqs and mirrors how ChatView memoizes rowStartSeqs once per commit.
    const window = (seqs: bigint[]) => ({ itemCount: seqs.length, rowSeqs: rowStartSeqs(items(seqs)) })
    const base = {
      loaded: true,
      ...window([1n, 2n, 3n]),
      range: { minSeq: 1n, maxSeq: 3n },
      hasMoreOlder: false,
      hasMoreNewer: false,
      totalHeight: 5000,
      viewportHeight: 500,
    }

    it('an unseeded rail leaves the native scrollbar in charge', () => {
      expect(resolveScrollbarOwner({ ...base, loaded: false })).toBe('native')
    })

    it('an empty window scrolls with neither bar', () => {
      expect(resolveScrollbarOwner({ ...base, ...window([]), range: { minSeq: 0n, maxSeq: 0n } })).toBe('none')
    })

    it('the rail owns scrolling for a multi-seq history that overflows', () => {
      expect(resolveScrollbarOwner(base)).toBe('rail')
    })

    it('neither bar shows when the whole history is loaded and fits', () => {
      expect(resolveScrollbarOwner({ ...base, totalHeight: 400 })).toBe('none')
    })

    it('the passed viewportHeight is the overflow measure (ChatView passes the content-box height)', () => {
      // The single caller (ChatView) passes the content-box viewportHeight -- the SAME coordinate
      // space totalHeight lives in -- so `totalHeight <= viewportHeight + tolerance` is the true
      // overflow test. Content overflowing it past the tolerance keeps the rail as owner; if a
      // caller instead passed the padding-box clientHeight (content-box + the container's ~32px
      // vertical padding), a conversation overflowing by up to that padding would resolve 'none'
      // and hide the rail over overflowing content -- the zero-scrollbar strand this design
      // forecloses by resolving ownership ONCE from ChatView's content-box height.
      expect(resolveScrollbarOwner({ ...base, totalHeight: 502, viewportHeight: 500 })).toBe('rail')
      expect(resolveScrollbarOwner({ ...base, totalHeight: 532, viewportHeight: 500 })).toBe('rail')
      // Same overflow (532) but a padding-box-sized height (532) would (wrongly) read as fitting.
      expect(resolveScrollbarOwner({ ...base, totalHeight: 532, viewportHeight: 532 })).toBe('none')
      expect(resolveScrollbarOwner({ ...base, totalHeight: 500, viewportHeight: 500 })).toBe('none')
    })

    it('the rail still owns scrolling when the loaded content fits but more history is off-window', () => {
      expect(resolveScrollbarOwner({ ...base, totalHeight: 400, hasMoreNewer: true })).toBe('rail')
    })

    it('hands a single-seq overflowing history back to the NATIVE bar (the strand fix)', () => {
      // A whole history of one distinct seq collapses the seq-space thumb-drag to a point, so a
      // lone message taller than the viewport must keep the native scrollbar rather than strand
      // behind a frozen rail. canRenderSeqRailThumb alone would (wrongly) report the rail usable.
      expect(canRenderSeqRailThumb(rowStartSeqs(items([7n])), { minSeq: 7n, maxSeq: 7n })).toBe(true)
      expect(resolveScrollbarOwner({
        ...base,
        ...window([7n]),
        range: { minSeq: 7n, maxSeq: 7n },
      })).toBe('native')
    })

    it('hands an all-optimistic-local overflowing window to the native bar (no server anchor)', () => {
      expect(resolveScrollbarOwner({
        ...base,
        ...window([0n, 0n]),
        range: { minSeq: 1n, maxSeq: 3n },
      })).toBe('native')
    })
  })
})
