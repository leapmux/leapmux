import type { Accessor } from 'solid-js'
import type { VirtualItem } from './useChatVirtualizer'
import { batch, createRenderEffect, createRoot, createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MAX_LOADED_CHAT_MESSAGES_CEILING } from '~/stores/chat.store'
import { createManualFrames } from '~/test-support/manualFrames'
import { BAND_BORDER_PX } from './chatRowGeometry'
import { warnSlowScrollPhase } from './chatScrollGeometry'
import { MEASURE_EPSILON_PX } from './createRowMeasurer'
import { createRenderReadinessScope } from './renderReadiness'
import { HEIGHT_CACHE_MAX, sameVirtualItems, useChatVirtualizer } from './useChatVirtualizer'
import { fakeRow, makeItems, plainItems, setup } from './useChatVirtualizer.testkit'

// The virtualizer reports the time of each rebuild of its offset map to
// `warnSlowScrollPhase`, so a spy on it counts the rebuilds.
vi.mock('./chatScrollGeometry', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./chatScrollGeometry')>()
  return {
    ...actual,
    warnSlowScrollPhase: vi.fn(actual.warnSlowScrollPhase),
  }
})

/** The rebuilds of the offset map since the last `mockClear` of the spy. */
function geomRebuilds(): number {
  return vi.mocked(warnSlowScrollPhase).mock.calls.filter(([phase]) => phase === 'geomRebuild').length
}

describe('useChatVirtualizer geometry', () => {
  it('rebuilds the offset map once for several height commits in one batch', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5))
      vi.mocked(warnSlowScrollPhase).mockClear()

      // The row measurer commits the reads of one flush in one batch, as here.
      batch(() => {
        virt.measure('m1', 110)
        virt.measure('m2', 120)
        virt.measure('m3', 130)
      })

      // The two unmeasured rows take the median of the three measures, 120.
      expect(virt.totalHeight()).toBe(110 + 120 + 130 + 2 * 120 + 4 * 20)
      expect(geomRebuilds()).toBe(1)
      dispose()
    })
  })

  it('completes an empty row without changing the positive estimate', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 120)
      expect(virt.measure('m2', 0)).toBe(true)
      expect(virt.hasMeasuredHeight('m2')).toBe(true)
      expect(virt.heightOfIndex(1)).toBe(0)
      expect(virt.estimateHeight()).toBe(120)
      expect(virt.offsetOfIndex(2)).toBe(160)
      expect(virt.totalHeight()).toBe(280)
      expect(virt.snapshotHeights()).toContainEqual({ id: 'm2', heightKey: undefined, height: 0 })
      dispose()
    })
  })

  it('hydrates cached zero and invalidates it when the layout key changes', () => {
    createRoot((dispose) => {
      try {
        const { virt, setList } = setup([
          { id: 'empty', heightKey: 'old', hasSpanLines: false },
        ])
        expect(virt.primeHeights([
          { id: 'empty', heightKey: 'old', height: 0 },
        ])).toBe(1)
        expect(virt.hasMeasuredHeight('empty')).toBe(true)
        expect(virt.totalHeight()).toBe(0)
        expect(virt.estimateHeight()).toBe(100)
        expect(virt.measure('empty', 0.2)).toBe(true)
        expect(virt.heightOfIndex(0)).toBe(0.2)
        expect(virt.measure('empty', 0)).toBe(true)
        expect(virt.heightOfIndex(0)).toBe(0)
        setList([
          { id: 'empty', heightKey: 'new', hasSpanLines: false },
        ])
        expect(virt.hasMeasuredHeight('empty')).toBe(false)
        expect(virt.primeHeight('empty', 0, 'old')).toBe(false)
        expect(virt.primeHeight('missing', 0)).toBe(false)
        expect(virt.measure('missing', 0)).toBe(false)
        expect(virt.heightOfIndex(0)).toBe(100)
      }
      finally {
        dispose()
      }
    })
  })

  it('keeps band offsets nondecreasing for zero and fractional heights', () => {
    createRoot((dispose) => {
      const { virt } = setup(makeItems([
        { seq: 1, kind: 'assistant_text' },
        { seq: 2, kind: 'assistant_thinking' },
        { seq: 3, kind: 'assistant_text' },
      ]))
      virt.measure('m1', 0)
      virt.measure('m2', 0.2)
      virt.measure('m3', 0.4)
      expect(virt.hasMeasuredHeight('m1')).toBe(true)
      expect(virt.offsetOfIndex(1)).toBe(0)
      expect(virt.offsetOfIndex(2)).toBe(0)
      expect(virt.totalHeight()).toBe(0.4)
      dispose()
    })
  })

  it('keeps an anchor at the first empty band and preserves an offset inside the later body', () => {
    createRoot((dispose) => {
      try {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'assistant_thinking' },
          { seq: 3, kind: 'assistant_text' },
        ]))
        virt.measure('m1', 0)
        virt.measure('m2', 0)
        virt.measure('m3', 40)
        const atStart = virt.anchorAt(0)!
        const insideBody = virt.anchorAt(10)!
        expect(atStart).toEqual({
          id: 'm1',
          offsetWithinRow: 0,
          basisHeight: 0,
          gapFraction: 0,
        })
        expect(insideBody.id).toBe('m3')
        expect(virt.scrollTopForAnchor(insideBody)).toBe(10)
        virt.measure('m1', 20)
        expect(virt.scrollTopForAnchor(atStart)).toBe(0)
        expect(virt.scrollTopForAnchor(insideBody)).toBe(30)
        expect(virt.gapAboveOf('m2') === 0).toBe(true)
        virt.measure('m2', 10)
        expect(virt.gapAboveOf('m2')).toBe(-BAND_BORDER_PX)
        expect(virt.gapAboveOf('m3')).toBe(-BAND_BORDER_PX)
      }
      finally {
        dispose()
      }
    })
  })

  it('computes estimate-only offsets and total height', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5))
      // 5 rows of 100px with 20px gaps between them.
      expect(virt.offsetOfIndex(0)).toBe(0)
      expect(virt.offsetOfIndex(1)).toBe(120)
      expect(virt.offsetOfIndex(2)).toBe(240)
      expect(virt.offsetOfIndex(4)).toBe(480)
      expect(virt.totalHeight()).toBe(580) // 5*100 + 4*20
      dispose()
    })
  })

  it('returns the measured height of a row, and the estimate for an index outside the list', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(2))
      virt.measure('m1', 140)
      virt.measure('m2', 60)
      // The estimate is the lower median of the two measures.
      expect(virt.estimateHeight()).toBe(60)
      expect(virt.heightOfIndex(0)).toBe(140)
      expect(virt.heightOfIndex(1)).toBe(60)
      expect(virt.heightOfIndex(-1)).toBe(60)
      expect(virt.heightOfIndex(2)).toBe(60)
      expect(virt.heightOfId('m1')).toBe(140)
      expect(virt.heightOfId('missing')).toBe(60)
      dispose()
    })
  })

  it('uses the small gap when the lower row has span lines', () => {
    createRoot((dispose) => {
      // rows: plain, span, plain -> gap(0)=small (row1 has span), gap(1)=large (row2 plain)
      const { virt } = setup(makeItems([
        { seq: 1 },
        { seq: 2, span: true },
        { seq: 3 },
      ]))
      expect(virt.offsetOfIndex(1)).toBe(110) // 100 + small gap (lower row has span)
      expect(virt.offsetOfIndex(2)).toBe(230) // +100 + large gap (lower row plain)
      expect(virt.totalHeight()).toBe(330) // +100
      dispose()
    })
  })

  describe('adjacent band rows', () => {
    // A band paints a full-bleed strip with a line on its top and bottom edge. Two
    // adjacent bands overlap by one border width so the pair shows ONE line.
    it('overlaps two adjacent bands by the band border width', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'assistant_thinking' },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(100 - BAND_BORDER_PX)
        expect(virt.totalHeight()).toBe(200 - BAND_BORDER_PX)
        dispose()
      })
    })

    it('overlaps once per pair across a run of bands', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_thinking' },
          { seq: 2, kind: 'assistant_text' },
          { seq: 3, kind: 'assistant_thinking' },
          { seq: 4, kind: 'assistant_text' },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(100 - BAND_BORDER_PX)
        expect(virt.offsetOfIndex(2)).toBe(200 - 2 * BAND_BORDER_PX)
        expect(virt.offsetOfIndex(3)).toBe(300 - 3 * BAND_BORDER_PX)
        expect(virt.totalHeight()).toBe(400 - 3 * BAND_BORDER_PX)
        dispose()
      })
    })

    it('keeps the normal gap when only one side of the pair is a band', () => {
      createRoot((dispose) => {
        // band -> tool, then tool -> band. Neither pair merges, so both keep the
        // large gap: a band next to a non-band still draws both of its own lines.
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'tool_use' },
          { seq: 3, kind: 'assistant_thinking' },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(120)
        expect(virt.offsetOfIndex(2)).toBe(240)
        expect(virt.totalHeight()).toBe(340)
        dispose()
      })
    })

    it('overlaps a band pair even when the lower band has span lines', () => {
      createRoot((dispose) => {
        // The band rule wins over the small-gap rule. The rails then meet at the
        // band's own shared border rather than across a gap, and the bridge across
        // that seam collapses -- it sizes itself from gapAboveOf, asserted below.
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text', span: true },
          { seq: 2, kind: 'assistant_thinking', span: true },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(100 - BAND_BORDER_PX)
        dispose()
      })
    })

    it('still uses the small gap when a span row below a band is not a band', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'tool_use', span: true },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(110)
        dispose()
      })
    })

    it('adds no gap after the last row, band or not', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
        ]))
        expect(virt.totalHeight()).toBe(100)
        dispose()
      })
    })

    it('reports the gap above each row, which is what sizes the span-line bridge', () => {
      // The bridge overlay reads this instead of restating the gap as a token of its
      // own, so a merged band seam -- where there IS no gap -- collapses its segment
      // rather than drawing one for a gap that does not exist.
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1, kind: 'assistant_text' },
          { seq: 2, kind: 'assistant_thinking' },
          { seq: 3, kind: 'tool_use', span: true },
          { seq: 4, kind: 'tool_result' },
        ]))
        expect(virt.gapAboveOf('m1')).toBe(0) // nothing above the first row
        expect(virt.gapAboveOf('m2')).toBe(-BAND_BORDER_PX) // merged band pair
        expect(virt.gapAboveOf('m3')).toBe(10) // small gap: the lower row has rails
        expect(virt.gapAboveOf('m4')).toBe(20) // large gap
        expect(virt.gapAboveOf('nope')).toBe(0) // an absent row bridges nothing
        dispose()
      })
    })

    it('treats a row with no kind as a non-band', () => {
      createRoot((dispose) => {
        const { virt } = setup(makeItems([
          { seq: 1 },
          { seq: 2 },
        ]))
        expect(virt.offsetOfIndex(1)).toBe(120)
        dispose()
      })
    })
  })

  it('measured heights override the estimate and shift later offsets', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      expect(virt.measure('m1', 200)).toBe(true)
      expect(virt.offsetOfIndex(1)).toBe(220) // 200 + 20
      // Re-measuring within epsilon is a no-op.
      expect(virt.measure('m1', 200.2)).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(200)
      dispose()
    })
  })

  it('keeps the cached height below MEASURE_EPSILON_PX and commits a re-measure of MEASURE_EPSILON_PX', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)

      expect(virt.measure('m1', 200 + MEASURE_EPSILON_PX - 0.01)).toBe(false)
      expect(virt.measure('m1', 200 - MEASURE_EPSILON_PX + 0.01)).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(200)

      expect(virt.measure('m1', 200 - MEASURE_EPSILON_PX)).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(200 - MEASURE_EPSILON_PX)
      dispose()
    })
  })

  it('reserves the estimate for an unmeasured row -- no collapse-to-0 (offset continuity)', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 100)
      virt.measure('m3', 100)
      // m2 is unmeasured. It reserves the median estimate (100, from the two 100px
      // measurements), NOT 0 -- the same
      // geometry it has whether or not it is in the rendered window. So scrolling m2 into
      // the window causes no offset change; only its later measurement shifts geometry.
      // (Previously an in-window unmeasured row collapsed to 0, which is what made content
      // above the anchor shrink and drift when scrolling up.)
      expect(virt.heightOfIndex(1)).toBe(100)
      expect(virt.offsetOfIndex(1)).toBe(120) // m1(100) + large gap(20)
      expect(virt.offsetOfIndex(2)).toBe(240) // + m2 estimate(100) + gap(20)
      expect(virt.totalHeight()).toBe(340) // 3*100 + 2*20

      // Measuring m2 taller is the only shift -- an estimate->measured correction.
      expect(virt.primeHeight('m2', 250)).toBe(true)
      expect(virt.heightOfIndex(1)).toBe(250)
      expect(virt.offsetOfIndex(1)).toBe(120)
      expect(virt.offsetOfIndex(2)).toBe(390) // 100 + 20 + 250 + 20
      expect(virt.totalHeight()).toBe(490)
      dispose()
    })
  })

  it('rejects a negative height and keeps the prior measurement', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)
      expect(virt.estimateHeight()).toBe(200)
      expect(virt.measure('m1', -1)).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(200)
      expect(virt.estimateHeight()).toBe(200)
      dispose()
    })
  })

  it('ignores a NaN-height measurement so it cannot poison the median estimate', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)
      expect(virt.estimateHeight()).toBe(200)
      // A stray NaN height (`NaN <= 0` is false, so the old non-positive guard
      // let it through) would flow into the median histogram and turn estimateHeight — and
      // thus the whole offset map — into NaN. The engine must reject it.
      expect(virt.measure('m2', Number.NaN)).toBe(false)
      expect(virt.estimateHeight()).toBe(200)
      expect(Number.isFinite(virt.totalHeight())).toBe(true)
      dispose()
    })
  })

  it('ignores an Infinity-height measurement so it cannot poison the median estimate', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)
      expect(virt.estimateHeight()).toBe(200)
      // A stray Infinity height passes a bare `height > 0` test (`Infinity > 0` is
      // true), so it would flow into the median histogram and turn estimateHeight — and the
      // whole offset map — into NaN/Infinity. The finite-height guard rejects it.
      expect(virt.measure('m2', Number.POSITIVE_INFINITY)).toBe(false)
      expect(virt.estimateHeight()).toBe(200)
      expect(Number.isFinite(virt.totalHeight())).toBe(true)
      dispose()
    })
  })

  it('estimate is the median of measured rows -- robust to a tall outlier a mean would chase', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5))
      virt.measure('m1', 100)
      virt.measure('m2', 120)
      // A tall code/diff row (still within the outlier band, so it counts): a MEAN would
      // be dragged to ~473 and over-reserve every unmeasured row, shrinking content above
      // the anchor when the real (shorter) height lands. The median ignores its magnitude.
      virt.measure('m3', 1200)
      expect(virt.estimateHeight()).toBe(120) // median of (100,120,1200), not the 473 mean
      // Unmeasured m4/m5 now use 120.
      expect(virt.heightOfIndex(3)).toBe(120)
      dispose()
    })
  })

  it('estimates a row from its OWN kind, falling back to the global median for an unseen kind', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [
        { id: 'u1', hasSpanLines: false, kind: 'user_text' },
        { id: 'u2', hasSpanLines: false, kind: 'user_text' },
        { id: 't1', hasSpanLines: false, kind: 'tool_result' },
        { id: 't2', hasSpanLines: false, kind: 'tool_result' },
        { id: 'u3', hasSpanLines: false, kind: 'user_text' }, // unmeasured
        { id: 't3', hasSpanLines: false, kind: 'tool_result' }, // unmeasured
        { id: 'x1', hasSpanLines: false, kind: 'assistant_text' }, // unmeasured, unseen kind
      ]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 999, // deliberately far off, to prove estimates aren't seed-derived
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      // Short user rows, tall tool rows.
      virt.measure('u1', 40)
      virt.measure('u2', 60)
      virt.measure('t1', 400)
      virt.measure('t2', 600)
      // The unmeasured user row estimates from the USER median (40), not the ~275 mean a
      // single global estimate blends across kinds -- the over-estimate that drifts the pin.
      expect(virt.heightOfIndex(4)).toBe(40) // u3: lower median of (40,60)
      // The unmeasured tool row estimates from the TOOL median (400).
      expect(virt.heightOfIndex(5)).toBe(400) // t3: lower median of (400,600)
      // A kind with no measurements falls back to the GLOBAL median across every row.
      expect(virt.heightOfIndex(6)).toBe(60) // x1: lower median of (40,60,400,600)
      dispose()
    })
  })

  it('re-routes a re-measure contribution to the right kind bucket when kind flips but heightKey does not', () => {
    createRoot((dispose) => {
      // A reclassification normally bumps a heightKey input too, so this is defensive -- but the
      // per-kind median must survive a kind flip that leaves heightKey unchanged: the previous
      // contribution has to LEAVE its old bucket and the new one ENTER the current bucket, or a
      // phantom count strands in the old bucket and skews every future estimate for that kind.
      const [list, setList] = createSignal<VirtualItem[]>([
        {
          id: 'a',
          hasSpanLines: false,
          heightKey: 'k1',
          kind: 'user_text',
        },
        { id: 'probeUser', hasSpanLines: false, kind: 'user_text' }, // unmeasured
        { id: 'probeTool', hasSpanLines: false, kind: 'tool_result' }, // unmeasured
      ])
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 999, // far off, so a phantom bucket entry would be plainly visible
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      // 'a' measured as user_text: 40 enters the user_text bucket (and the global median).
      virt.measure('a', 40)
      expect(virt.heightOfIndex(1)).toBe(40) // probeUser estimates from the user_text bucket

      // Flip 'a' to tool_result but keep heightKey 'k1', so the cache entry survives the
      // stale-key delete and the commit hits the re-measure branch with
      // prevEntry.kind ('user_text') != kind ('tool_result').
      setList([
        {
          id: 'a',
          hasSpanLines: false,
          heightKey: 'k1',
          kind: 'tool_result',
        },
        { id: 'probeUser', hasSpanLines: false, kind: 'user_text' },
        { id: 'probeTool', hasSpanLines: false, kind: 'tool_result' },
      ])
      virt.measure('a', 400)

      // The 40 left the user_text bucket (now empty -> probeUser falls back to the global
      // median, whose sole remaining sample is 400). The old replace(kind, 40, 400) removed 40
      // from tool_result (a no-op) and stranded it in user_text, so probeUser would read 40.
      expect(virt.heightOfIndex(1)).toBe(400) // probeUser: user_text empty -> global median 400
      // The 400 entered the tool_result bucket.
      expect(virt.heightOfIndex(2)).toBe(400) // probeTool: tool_result median 400
      dispose()
    })
  })

  it('records the last commit for drift attribution -- first measure vs re-measure', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [
        { id: 'a', hasSpanLines: false, kind: 'user_text' },
        { id: 'b', hasSpanLines: false, kind: 'user_text' },
      ]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      expect(virt.lastMeasurement()).toBeUndefined()

      // First measure: the map assumed the estimate (seed 100 -- no user_text measured yet),
      // so delta = 40 - 100 = -60 -- content shrank vs what was reserved (a firstMeasure
      // drift is the estimate being off / the row outrunning premeasure).
      virt.measure('a', 40)
      expect(virt.lastMeasurement()).toMatchObject({
        id: 'a',
        kind: 'user_text',
        source: 'visible',
        firstMeasure: true,
        assumedHeight: 100,
        newHeight: 40,
        delta: -60,
      })
      const seqAfterFirst = virt.lastMeasurement()?.commitSeq ?? 0

      // Re-measure: the map assumed the cached 40, so delta = 55 - 40 = 15 -- a re-measure
      // drift points at a premeasured-vs-visible mismatch or a chrome/content change.
      virt.measure('a', 55)
      const reMeasure = virt.lastMeasurement()
      expect(reMeasure).toMatchObject({
        id: 'a',
        firstMeasure: false,
        assumedHeight: 40,
        newHeight: 55,
        delta: 15,
      })
      // commitSeq advances, so two consecutive drift WARNs reveal commits between them.
      expect(reMeasure?.commitSeq ?? 0).toBeGreaterThan(seqAfterFirst)
      dispose()
    })
  })

  it('keeps the measured-height cache bound above the chat window ceiling', () => {
    // The height cache is LRU-capped with mountedIds protected, so its cap must
    // exceed the largest in-memory window or it would evict in-window rows.
    expect(HEIGHT_CACHE_MAX).toBeGreaterThan(MAX_LOADED_CHAT_MESSAGES_CEILING)
  })

  it('records firstMeasure on the first commit only, not on a re-measure', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [
        { id: 'a', hasSpanLines: false },
      ]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      virt.measure('a', 200)
      // First DOM height for the row: the commit info attributes it as a first
      // measure whose assumed height was the estimate (the drift WARN reads this).
      expect(virt.lastMeasurement()).toMatchObject({
        id: 'a',
        source: 'visible',
        firstMeasure: true,
        assumedHeight: 100,
        newHeight: 200,
        delta: 100,
      })
      virt.measure('a', 260) // re-measure (async growth) — the prior height is the baseline
      expect(virt.lastMeasurement()).toMatchObject({
        id: 'a',
        firstMeasure: false,
        assumedHeight: 200,
        newHeight: 260,
        delta: 60,
      })
      dispose()
    })
  })

  it('primes a measured height and records it as a premeasure commit', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [
        { id: 'a', hasSpanLines: false, heightKey: 'k1' },
      ]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      expect(virt.primeHeight('a', 275, 'k1')).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(275)
      expect(virt.hasMeasuredHeight('a')).toBe(true)
      // Hidden premeasurement is cache warm-up, not a visible mount — the commit is
      // attributed to 'premeasure' so drift diagnostics can tell the sources apart.
      expect(virt.lastMeasurement()).toMatchObject({ id: 'a', source: 'premeasure', firstMeasure: true })
      dispose()
    })
  })

  it('does not let hidden premeasure overwrite an already measured mounted row', () => {
    createRoot((dispose) => {
      try {
        const [list] = createSignal<VirtualItem[]>([
          { id: 'a', hasSpanLines: false, heightKey: 'k1' },
        ])
        const virt = useChatVirtualizer({
          items: list,
          overscanPx: 0,
          estimateHeight: 100,
          gapSmallPx: 10,
          gapLargePx: 20,
        })
        virt.attachRow('a', fakeRow(320))

        expect(virt.heightOfIndex(0)).toBe(320)
        expect(virt.primeHeight('a', 120, 'k1')).toBe(false)
        expect(virt.heightOfIndex(0)).toBe(320)
      }
      finally {
        dispose()
      }
    })
  })

  it('ignores a pre-measured height when the row heightKey changed before commit', () => {
    createRoot((dispose) => {
      const [list, setList] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, heightKey: 'old' },
      ])
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      setList([
        { id: 'a', hasSpanLines: false, heightKey: 'new' },
      ])
      expect(virt.primeHeight('a', 275, 'old')).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(100)
      expect(virt.hasMeasuredHeight('a')).toBe(false)
      dispose()
    })
  })

  it('falls back instead of using a stale measured height when heightKey changes', () => {
    createRoot((dispose) => {
      const [list, setList] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, heightKey: 'old' },
      ])
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      expect(virt.primeHeight('a', 275, 'old')).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(275)
      setList([
        { id: 'a', hasSpanLines: false, heightKey: 'new' },
      ])
      expect(virt.heightOfIndex(0)).toBe(100)
      expect(virt.hasMeasuredHeight('a')).toBe(false)
      dispose()
    })
  })

  describe('a height key change of a mounted row', () => {
    const keyedItems = (key: string): VirtualItem[] => [
      { id: 'a', hasSpanLines: false, heightKey: key },
      { id: 'b', hasSpanLines: false, heightKey: 'b' },
    ]

    /**
     * The height of row `a` that a snapshot saves, or undefined while no measure under the
     * current key of the row produced its height: a snapshot leaves a provisional height out.
     */
    const savedHeightOfA = (virt: ReturnType<typeof useChatVirtualizer>) =>
      virt.snapshotHeights().find(height => height.id === 'a')

    it('keeps the height that the row measured as its slot, with no commit, so the row needs no hidden copy', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup(keyedItems('old'))
        virt.attachRow('a', fakeRow(320))
        expect(virt.heightOfIndex(0)).toBe(320)
        expect(savedHeightOfA(virt)).toEqual({ id: 'a', heightKey: 'old', height: 320 })
        const commitSeq = virt.lastMeasurement()?.commitSeq

        setList(keyedItems('new'))

        expect(virt.heightOfIndex(0)).toBe(320)
        expect(virt.hasMeasuredHeight('a')).toBe(true)
        // No measure under the new key produced the height yet.
        expect(savedHeightOfA(virt)).toBeUndefined()
        expect(virt.lastMeasurement()?.commitSeq).toBe(commitSeq)
        // The visible copy is the authority on what the row shows.
        expect(virt.primeHeight('a', 120, 'new')).toBe(false)
        expect(virt.heightOfIndex(0)).toBe(320)
        dispose()
      })
    })

    it('replaces the kept height with the next measure of the row, as a measure again of the same row', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup(keyedItems('old'))
        virt.attachRow('a', fakeRow(320))
        setList(keyedItems('new'))

        expect(virt.measure('a', 200)).toBe(true)

        expect(virt.heightOfIndex(0)).toBe(200)
        expect(savedHeightOfA(virt)).toEqual({ id: 'a', heightKey: 'new', height: 200 })
        expect(virt.lastMeasurement()).toMatchObject({
          id: 'a',
          source: 'visible',
          firstMeasure: false,
          assumedHeight: 320,
          newHeight: 200,
        })
        expect(virt.estimateHeight()).toBe(200)
        dispose()
      })
    })

    it('drops the kept height when the row unmounts before it measures under its new key', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup(keyedItems('old'))
        const row = fakeRow(320)
        virt.attachRow('a', row)
        setList(keyedItems('new'))
        expect(virt.totalHeight()).toBe(320 + 20 + 320)

        virt.detachRow(row)

        expect(virt.hasMeasuredHeight('a')).toBe(false)
        // The row's measure leaves the estimate too, so both rows estimate at the seed.
        expect(virt.estimateHeight()).toBe(100)
        expect(virt.heightOfIndex(0)).toBe(100)
        expect(virt.totalHeight()).toBe(100 + 20 + 100)
        dispose()
      })
    })

    it('keeps the height after the row unmounts once a measure of the same height confirms it under the new key', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup(keyedItems('old'))
        const row = fakeRow(320)
        virt.attachRow('a', row)
        setList(keyedItems('new'))

        expect(virt.measure('a', 320)).toBe(false)
        expect(savedHeightOfA(virt)).toEqual({ id: 'a', heightKey: 'new', height: 320 })
        virt.detachRow(row)

        expect(virt.hasMeasuredHeight('a')).toBe(true)
        expect(virt.heightOfIndex(0)).toBe(320)
        dispose()
      })
    })

    it('keeps the height while a newer element of the row holds it, and drops it when the last element unmounts', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup(keyedItems('old'))
        const first = fakeRow(320)
        virt.attachRow('a', first)
        setList(keyedItems('new'))
        // A copy with no layout yet reads nothing, so its attach confirms no height.
        const second = fakeRow(0)
        second.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0)
        virt.attachRow('a', second)

        virt.detachRow(first)
        expect(virt.hasMeasuredHeight('a')).toBe(true)
        expect(virt.heightOfIndex(0)).toBe(320)

        virt.detachRow(second)
        expect(virt.hasMeasuredHeight('a')).toBe(false)
        expect(virt.heightOfIndex(0)).toBe(100)
        dispose()
      })
    })

    it('saves no kept height in a snapshot until a measure confirms it under the new key', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup(keyedItems('old'))
        virt.attachRow('a', fakeRow(320))
        setList(keyedItems('new'))

        expect(virt.snapshotHeights()).toEqual([])

        virt.measure('a', 320)
        expect(virt.snapshotHeights()).toEqual([
          { id: 'a', heightKey: 'new', height: 320 },
        ])
        dispose()
      })
    })

    it('confirms the height again when the key changes back to the key that measured it', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup(keyedItems('old'))
        const row = fakeRow(320)
        virt.attachRow('a', row)
        setList(keyedItems('new'))
        expect(savedHeightOfA(virt)).toBeUndefined()

        setList(keyedItems('old'))

        expect(savedHeightOfA(virt)).toEqual({ id: 'a', heightKey: 'old', height: 320 })
        virt.detachRow(row)
        expect(virt.heightOfIndex(0)).toBe(320)
        dispose()
      })
    })

    it('reads the row again at the next flush, and confirms a height that holds under the new key, whether or not the row resizes', async () => {
      let virt!: ReturnType<typeof useChatVirtualizer>
      let setList!: (items: VirtualItem[]) => void
      const dispose = createRoot((disposeRoot) => {
        ({ virt, setList } = setup(keyedItems('old')))
        virt.attachRow('a', fakeRow(320))
        return disposeRoot
      })
      try {
        setList(keyedItems('new'))
        const version = virt.geometryVersion()
        expect(savedHeightOfA(virt)).toBeUndefined()

        await Promise.resolve()

        expect(savedHeightOfA(virt)).toEqual({ id: 'a', heightKey: 'new', height: 320 })
        expect(virt.heightOfIndex(0)).toBe(320)
        // The confirmation moves no row, but it bumps the geometry version: the persistence
        // saves the snapshot on that bump, and the snapshot now holds the height.
        expect(virt.geometryVersion()).toBeGreaterThan(version)
      }
      finally {
        dispose()
      }
    })

    it('commits the height that the row reads at the next flush after its key changes', async () => {
      let virt!: ReturnType<typeof useChatVirtualizer>
      let setList!: (items: VirtualItem[]) => void
      let row!: HTMLElement
      const dispose = createRoot((disposeRoot) => {
        ({ virt, setList } = setup(keyedItems('old')))
        row = fakeRow(320)
        virt.attachRow('a', row)
        return disposeRoot
      })
      try {
        // The new content renders within the update that changes the key.
        row.getBoundingClientRect = () => new DOMRect(0, 0, 400, 200)
        setList(keyedItems('new'))

        await Promise.resolve()

        expect(virt.heightOfIndex(0)).toBe(200)
        expect(savedHeightOfA(virt)).toEqual({ id: 'a', heightKey: 'new', height: 200 })
      }
      finally {
        dispose()
      }
    })

    it('reads nothing from a row copy that replaces its content behind what it shows, and reads it once the copy shows the new content', async () => {
      let virt!: ReturnType<typeof useChatVirtualizer>
      let setList!: (items: VirtualItem[]) => void
      let row!: HTMLElement
      const [replacing, setReplacing] = createSignal(false)
      const dispose = createRoot((disposeRoot) => {
        ({ virt, setList } = setup(keyedItems('old')))
        const readiness = createRenderReadinessScope()
        readiness.trackReplacing(replacing)
        row = fakeRow(320)
        virt.attachRow('a', row, readiness)
        return disposeRoot
      })
      try {
        expect(savedHeightOfA(virt)).toEqual({ id: 'a', heightKey: 'old', height: 320 })
        // A diff view switches its layout: the old one stays in view while the new one mounts.
        setReplacing(true)
        setList(keyedItems('new'))
        await Promise.resolve()
        expect(savedHeightOfA(virt)).toBeUndefined()
        expect(virt.heightOfIndex(0)).toBe(320)

        row.getBoundingClientRect = () => new DOMRect(0, 0, 400, 200)
        setReplacing(false)
        await Promise.resolve()

        expect(virt.heightOfIndex(0)).toBe(200)
        expect(savedHeightOfA(virt)).toEqual({ id: 'a', heightKey: 'new', height: 200 })
      }
      finally {
        dispose()
      }
    })

    it('drops the height of a row that is not mounted, as before', () => {
      createRoot((dispose) => {
        const { virt, setList } = setup(keyedItems('old'))
        const row = fakeRow(320)
        virt.attachRow('a', row)
        virt.detachRow(row)

        setList(keyedItems('new'))

        expect(virt.hasMeasuredHeight('a')).toBe(false)
        expect(virt.heightOfIndex(0)).toBe(100)
        dispose()
      })
    })
  })

  it('measures a mounted row only once its content is ready, through its readiness scope', async () => {
    // A row that waits for content has a height that is wrong by that content, so
    // the visible measurer must read nothing from it. Its content can land without
    // a resize, so the scope's turn to ready alone must measure it then.
    let virt!: ReturnType<typeof useChatVirtualizer>
    let setBodyPending!: (pending: boolean) => void
    const dispose = createRoot((disposeRoot) => {
      const [list] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, heightKey: 'k1' },
      ])
      virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      const [bodyPending, setPending] = createSignal(true)
      setBodyPending = setPending
      const readiness = createRenderReadinessScope()
      readiness.track(bodyPending)
      virt.attachRow('a', fakeRow(320), readiness)
      return disposeRoot
    })
    try {
      expect(virt.hasMeasuredHeight('a')).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(100)

      setBodyPending(false)
      // The measurer commits in its next flush, a microtask later.
      expect(virt.hasMeasuredHeight('a')).toBe(false)
      await Promise.resolve()
      expect(virt.hasMeasuredHeight('a')).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(320)
    }
    finally {
      dispose()
    }
  })

  it('exposes the fast-scroll flag reactively for the fling-skeleton gate', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(1))
      expect(virt.fastScrollActive()).toBe(false)
      virt.setFastScrollActive(true)
      expect(virt.fastScrollActive()).toBe(true)
      virt.setFastScrollActive(false)
      expect(virt.fastScrollActive()).toBe(false)
      dispose()
    })
  })

  it('drops a stale-keyed height\'s median contribution on an items change alone (no commit)', () => {
    createRoot((dispose) => {
      // The prune now runs in the items-keyed rowIndex memo, not per geometry
      // commit. A heightKey change with NO subsequent measurement must still
      // remove the stale row's contribution from the estimate median -- if the
      // prune stopped firing on items changes, the dead 300px measurement would
      // keep inflating every unmeasured row's estimate.
      const [list, setList] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, heightKey: 'old' },
        { id: 'b', hasSpanLines: false },
      ])
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      expect(virt.primeHeight('a', 300, 'old')).toBe(true)
      expect(virt.estimateHeight()).toBe(300) // sole contribution -> median 300
      expect(virt.heightOfIndex(1)).toBe(300) // unmeasured b estimates off it
      setList([
        { id: 'a', hasSpanLines: false, heightKey: 'new' },
        { id: 'b', hasSpanLines: false },
      ])
      expect(virt.heightOfIndex(0)).toBe(100) // stale height not served...
      expect(virt.estimateHeight()).toBe(100) // ...and its contribution pruned
      expect(virt.heightOfIndex(1)).toBe(100)
      dispose()
    })
  })

  it('preserves an in-gap viewport position as a gap FRACTION, re-applied against the current gap', () => {
    createRoot((dispose) => {
      const [list, setList] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false }, // gap(a->b) = large (20)
        { id: 'c', hasSpanLines: false },
      ])
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      // Row 'a' is an UNMEASURED 100px estimate; the 20px gap below it spans [100,120).
      // A scrollTop within the row body has no gap fraction.
      expect(virt.anchorAt(60)).toEqual({
        id: 'a',
        offsetWithinRow: 60,
        basisHeight: 100,
        gapFraction: 0,
      })
      // ...a scrollTop of 110 lands 10px into the 20px gap = HALFWAY (fraction 0.5).
      // offsetWithinRow clamps to the row bottom; gapFraction carries the in-gap part.
      const anchor = virt.anchorAt(110)
      expect(anchor).toEqual({
        id: 'a',
        offsetWithinRow: 100,
        basisHeight: 100,
        gapFraction: 0.5,
      })
      // Resolve reproduces the exact in-gap position: 100 (row bottom) + 0.5 * 20.
      expect(virt.scrollTopForAnchor(anchor!)).toBe(110)

      // Flip the next row's span lines so gap(a->b) shrinks 20 -> 10. The FRACTION is
      // re-applied to the new gap, so the pin stays halfway into it: 100 + 0.5 * 10.
      setList([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: true },
        { id: 'c', hasSpanLines: false },
      ])
      expect(virt.scrollTopForAnchor(anchor!)).toBe(105)
      dispose()
    })
  })

  it('falls back to the row bottom for an anchor with no gapFraction (pre-field / persisted)', () => {
    createRoot((dispose) => {
      const [list] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
      ])
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      // An anchor restored from old persistence carries no gapFraction -> the prior
      // gap-independent pin-to-row-bottom behavior (no NaN from a missing field).
      expect(virt.scrollTopForAnchor({ id: 'a', offsetWithinRow: 100, basisHeight: 100 })).toBe(100)
      dispose()
    })
  })

  it('resolves an UNDER-estimated row PROPORTIONALLY so the fraction into the row is preserved when it measures taller', () => {
    createRoot((dispose) => {
      const [list] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
      ])
      // Row 'a' is UNDER-estimated by the fallback: 100, but it will measure 600.
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      // Anchor at the MIDDLE of the 100px fallback (scrollTop 50 -> fraction 0.5).
      const anchor = virt.anchorAt(50)
      expect(anchor).toEqual({
        id: 'a',
        offsetWithinRow: 50,
        basisHeight: 100,
        gapFraction: 0,
      })
      // Before measure, resolve returns the captured 50 (basis == current fallback).
      expect(virt.scrollTopForAnchor(anchor!)).toBe(50)
      // Row 'a' measures 600. The 0.5 fraction maps to 0.5 * 600 = 300 -- the pin
      // tracks the row's growth instead of staying at a stale 40px.
      virt.measure('a', 600)
      expect(virt.scrollTopForAnchor(anchor!)).toBe(300)
      dispose()
    })
  })

  describe('heightDebugOfId (raw-json debug surface)', () => {
    it('returns measured undefined before the row is measured', () => {
      createRoot((dispose) => {
        const { virt } = setup(plainItems(2))
        expect(virt.heightDebugOfId('m1')).toEqual({ measured: undefined })
        dispose()
      })
    })

    it('surfaces the measured height once measured', () => {
      createRoot((dispose) => {
        const { virt } = setup(plainItems(2))
        virt.measure('m1', 250)
        expect(virt.heightDebugOfId('m1')).toEqual({ measured: 250 })
        dispose()
      })
    })

    it('omits stale measured height when the height key changes', () => {
      createRoot((dispose) => {
        const [list, setList] = createSignal<VirtualItem[]>([
          { id: 'a', hasSpanLines: false, heightKey: 'old' },
        ])
        const virt = useChatVirtualizer({
          items: list,
          overscanPx: 0,
          estimateHeight: 100,
          gapSmallPx: 10,
          gapLargePx: 20,
        })
        expect(virt.measure('a', 250)).toBe(true)
        expect(virt.heightDebugOfId('a')).toEqual({ measured: 250 })
        setList([
          { id: 'a', hasSpanLines: false, heightKey: 'new' },
        ])
        expect(virt.heightDebugOfId('a')).toEqual({ measured: undefined })
        dispose()
      })
    })

    it('returns measured undefined for an unknown id', () => {
      createRoot((dispose) => {
        const { virt } = setup(plainItems(2))
        expect(virt.heightDebugOfId('nope')).toEqual({ measured: undefined })
        dispose()
      })
    })
  })
})

describe('sameVirtualItems geometry equality', () => {
  const base: VirtualItem = {
    id: 'm1',
    hasSpanLines: false,
    heightKey: 'k1',
    seq: 1n,
    kind: 'user_text',
  }

  it('is true for arrays equal on the geometry fields', () => {
    expect(sameVirtualItems([
      { ...base },
    ], [
      { ...base },
    ])).toBe(true)
  })

  it('ignores non-geometry fields (seq) when they differ', () => {
    // seq is an anchor label, not an offset input, so a row that differs ONLY there
    // must still compare equal (else the memo would needlessly rebuild geometry on
    // every streaming delta).
    const other: VirtualItem = { ...base, seq: 999n }
    expect(sameVirtualItems([base], [other])).toBe(true)
  })

  it('is false when any geometry field differs', () => {
    expect(sameVirtualItems([base], [
      { ...base, id: 'm2' },
    ])).toBe(false)
    expect(sameVirtualItems([base], [
      { ...base, hasSpanLines: true },
    ])).toBe(false)
    expect(sameVirtualItems([base], [
      { ...base, heightKey: 'k2' },
    ])).toBe(false)
    // kind feeds the per-kind estimate bucket, so a kind change is geometry-relevant.
    expect(sameVirtualItems([base], [
      { ...base, kind: 'tool_result' },
    ])).toBe(false)
  })

  it('is false for different lengths and true for the same reference', () => {
    expect(sameVirtualItems([base], [base, base])).toBe(false)
    const arr = [base]
    expect(sameVirtualItems(arr, arr)).toBe(true)
  })
})

describe('primeHeights bulk hydration and snapshotheights', () => {
  it('commits a whole batch with ONE geometryVersion bump', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(4))
      const before = virt.geometryVersion()
      const adopted = virt.primeHeights([
        { id: 'm1', heightKey: undefined, height: 50 },
        { id: 'm2', heightKey: undefined, height: 60 },
        { id: 'm3', heightKey: undefined, height: 70 },
      ])
      expect(adopted).toBe(3)
      expect(virt.geometryVersion()).toBe(before + 1)
      expect(virt.heightOfIndex(0)).toBe(50)
      expect(virt.heightOfIndex(1)).toBe(60)
      expect(virt.heightOfIndex(2)).toBe(70)
      // m4 stays on the estimate (median of 50/60/70 = 60).
      expect(virt.heightOfIndex(3)).toBe(60)
      dispose()
    })
  })

  it('skips unknown ids, stale keys, and unusable heights without bumping geometry', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [
        { id: 'm1', hasSpanLines: false, heightKey: 'k-live' },
        { id: 'm2', hasSpanLines: false, heightKey: 'k-live' },
      ]
      const { virt } = setup(items)
      const before = virt.geometryVersion()
      const adopted = virt.primeHeights([
        { id: 'ghost', heightKey: 'k-live', height: 50 }, // not in the list
        { id: 'm1', heightKey: 'k-stale', height: 50 }, // wrong layout epoch
        { id: 'm2', heightKey: 'k-live', height: -1 }, // unusable height
      ])
      expect(adopted).toBe(0)
      expect(virt.geometryVersion()).toBe(before)
      expect(virt.hasMeasuredHeight('m1')).toBe(false)
      dispose()
    })
  })

  it('never overwrites a mounted row\'s live visible measurement', () => {
    createRoot((dispose) => {
      try {
        const { virt } = setup(plainItems(2))
        virt.attachRow('m1', fakeRow(150))
        virt.measure('m1', 150)
        const adopted = virt.primeHeights([
          { id: 'm1', heightKey: undefined, height: 999 },
        ])
        expect(adopted).toBe(0)
        expect(virt.heightOfIndex(0)).toBe(150)
      }
      finally {
        dispose()
      }
    })
  })

  it('exports the cache in LRU order (oldest measurement first) through snapshotHeights', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 100)
      virt.measure('m2', 110)
      virt.measure('m1', 120) // re-measure refreshes m1 to most-recent
      expect(virt.snapshotHeights()).toEqual([
        { id: 'm2', heightKey: undefined, height: 110 },
        { id: 'm1', heightKey: undefined, height: 120 },
      ])
      dispose()
    })
  })
})

describe('useChatVirtualizer range', () => {
  // `setup` estimates every row at 100px with a gap of 20px, so row i starts at 120 * i,
  // and it renders no overscan.
  const rangeIds = (virt: ReturnType<typeof setup>['virt'], list: readonly VirtualItem[]) => {
    const { start, end } = virt.range()
    return { first: list[start]?.id, last: list[end - 1]?.id }
  }

  it('moves the range with its rows when rows are prepended', () => {
    createRoot((dispose) => {
      const newest = plainItems(100, 51)
      const { virt, setList } = setup(newest)
      virt.updateViewport(5000, 500)
      const before = virt.range()
      expect(before).toEqual({ start: 41, end: 46 })
      const idsBefore = rangeIds(virt, newest)

      const prepended = [
        ...plainItems(50, 1),
        ...newest,
      ]
      setList(prepended)

      expect(virt.range()).toEqual({ start: before.start + 50, end: before.end + 50 })
      expect(rangeIds(virt, prepended)).toEqual(idsBefore)
      dispose()
    })
  })

  it('keeps the range when its first row is trimmed away', () => {
    createRoot((dispose) => {
      const items = plainItems(100)
      const { virt, setList } = setup(items)
      virt.updateViewport(1200, 2000)
      expect(virt.range()).toEqual({ start: 10, end: 27 })
      const lastId = items[26]?.id

      const trimmed = items.slice(20)
      setList(trimmed)

      // The last row is now at index 6. The length of 17 rows ends before index 0,
      // so the start clamps to the list.
      expect(virt.range()).toEqual({ start: 0, end: 7 })
      expect(trimmed[virt.range().end - 1]?.id).toBe(lastId)
      dispose()
    })
  })

  it('keeps the length when only the first row remains', () => {
    createRoot((dispose) => {
      const items = plainItems(100)
      const { virt, setList } = setup(items)
      virt.updateViewport(1200, 500)
      expect(virt.range()).toEqual({ start: 10, end: 15 })

      // Rows above the slice and its last row leave: the first row moves to index 2.
      setList([
        ...items.slice(8, 14),
        ...items.slice(15),
      ])

      expect(virt.range()).toEqual({ start: 2, end: 7 })
      dispose()
    })
  })

  it('keeps the stored indices when both edge rows left the list', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup(plainItems(100))
      virt.updateViewport(1200, 500)
      expect(virt.range()).toEqual({ start: 10, end: 15 })

      setList(plainItems(100, 1000))

      expect(virt.range()).toEqual({ start: 10, end: 15 })
      dispose()
    })
  })

  it('commits the current edge ids when only the ids changed', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup(plainItems(100))
      virt.updateViewport(1200, 500)
      // A replaced window keeps the stored indices, and the next commit must store the
      // ids of the new rows. A later prepend then moves the slice with them.
      const replaced = plainItems(100, 1000)
      setList(replaced)
      virt.updateViewport(1200, 500)
      expect(virt.range()).toEqual({ start: 10, end: 15 })

      setList([
        ...plainItems(5, 500),
        ...replaced,
      ])

      expect(virt.range()).toEqual({ start: 15, end: 20 })
      dispose()
    })
  })

  it('keeps one neighbor on each side of a row that holds the whole view and its overscan', () => {
    createRoot((dispose) => {
      const [list] = createSignal(plainItems(10))
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 200,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      for (let i = 1; i <= 10; i++) {
        virt.measure(`m${i}`, i === 5
          ? 2000
          : 100)
      }

      // Row 4 spans 480 to 2480, and holds the view at 1000 with 200px of overscan on each side.
      expect(virt.computeRange(1000, 500)).toEqual({ start: 3, end: 6 })
      // Rows of a normal height keep the slice of the view and its overscan.
      expect(virt.computeRange(0, 300)).toEqual({ start: 0, end: 5 })
      dispose()
    })
  })

  it('returns an empty range for an empty list', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup(plainItems(10))
      virt.updateViewport(0, 500)
      expect(virt.range()).toEqual({ start: 0, end: 5 })

      setList([])
      expect(virt.range()).toEqual({ start: 0, end: 0 })

      setList(plainItems(1))
      expect(virt.range()).toEqual({ start: 0, end: 1 })
      dispose()
    })
  })

  it('renders no row before the first commit', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(10))
      expect(virt.range()).toEqual({ start: 0, end: 0 })

      virt.updateViewport(0, 500)
      expect(virt.range()).toEqual({ start: 0, end: 5 })
      dispose()
    })
  })

  it('keeps an empty commit empty when rows arrive before the next commit', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup([])
      virt.updateViewport(0, 500)
      expect(virt.range()).toEqual({ start: 0, end: 0 })

      setList(plainItems(3))
      expect(virt.range()).toEqual({ start: 0, end: 0 })

      virt.updateViewport(0, 500)
      expect(virt.range()).toEqual({ start: 0, end: 3 })
      dispose()
    })
  })

  it('keeps the same range object while the indices hold', () => {
    createRoot((dispose) => {
      const items = plainItems(100)
      const { virt, setList } = setup(items)
      virt.updateViewport(1200, 500)
      const first = virt.range()

      // An append below the slice moves no index of it.
      setList([
        ...items,
        ...plainItems(3, 500),
      ])

      expect(virt.range()).toBe(first)
      dispose()
    })
  })
})

describe('useChatVirtualizer layoutShift', () => {
  // `setup` estimates every row at 100px with a gap of 20px, so row i starts at 120 * i.
  // Each test measures the 20 rows as 100px, so every row is measured. Row 10 is `m11`.
  const ROW_10 = 'm11'

  /**
   * A scroll element whose geometry the test sets, and a spacer and a layer for
   * `attachSpacer` and `attachLayer`. Each records its writes in `writes`, in order. The scroll element
   * follows the inline height of the spacer once the spacer has one, and its `scrollTop`
   * stays inside the scroll range, as a browser keeps it. With `roundScrollTop`, it also
   * stores each written `scrollTop` rounded to a whole pixel, as a browser rounds it to a
   * device pixel.
   */
  function fakeLayout(init: { clientHeight?: number, scrollHeight?: number, scrollTop?: number, roundScrollTop?: boolean } = {}) {
    const state = {
      clientHeight: init.clientHeight ?? 500,
      scrollHeight: init.scrollHeight ?? 2380,
      scrollTop: init.scrollTop ?? 0,
      spacerHeight: '',
      layerTransform: '',
    }
    const writes: string[] = []
    const scrollHeight = () => (state.spacerHeight === ''
      ? state.scrollHeight
      : Number.parseFloat(state.spacerHeight))
    const maxTop = () => Math.max(0, scrollHeight() - state.clientHeight)
    const scroller = document.createElement('div')
    Object.defineProperty(scroller, 'clientHeight', {
      configurable: true,
      get: () => state.clientHeight,
    })
    Object.defineProperty(scroller, 'scrollHeight', { configurable: true, get: scrollHeight })
    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      get: () => Math.min(state.scrollTop, maxTop()),
      set: (top: number) => {
        writes.push('scrollTop')
        const clamped = Math.max(0, Math.min(top, maxTop()))
        state.scrollTop = init.roundScrollTop === true
          ? Math.round(clamped)
          : clamped
      },
    })
    const spacer = document.createElement('div')
    Object.defineProperty(spacer, 'style', {
      configurable: true,
      value: {
        get height() {
          return state.spacerHeight
        },
        set height(value: string) {
          writes.push('spacer')
          state.spacerHeight = value
        },
      },
    })
    const layer = document.createElement('div')
    Object.defineProperty(layer, 'style', {
      configurable: true,
      value: {
        get transform() {
          return state.layerTransform
        },
        set transform(value: string) {
          writes.push('layer')
          state.layerTransform = value
        },
      },
    })
    return {
      state,
      writes,
      scroller,
      spacer,
      layer,
    }
  }

  /**
   * Mount a virtualizer over 20 measured rows. The test continues outside the root, so a
   * signal write runs its effects at once, as a browser event does. `scrollElement` gives
   * an accessor that can return no element, in place of the fixed `scroller`. Each row of
   * `premeasuredIds` takes its 100px from a hidden copy (`primeHeight`), so no read of a
   * mounted copy gave it: the reader did not see the row at that height.
   */
  function mount(opts: {
    scroller?: HTMLElement
    scrollElement?: () => HTMLElement | undefined
    pinBottom?: () => boolean
    rowCount?: number
    spacerInsetPx?: Accessor<number> | number
    premeasuredIds?: readonly string[]
  } = {}) {
    let dispose!: () => void
    const mounted = createRoot((d) => {
      dispose = d
      const scroller = opts.scroller
      const scrollElement = opts.scrollElement ?? (scroller === undefined
        ? undefined
        : () => scroller)
      return setup(plainItems(opts.rowCount ?? 20), {
        ...(scrollElement === undefined
          ? {}
          : { scrollElement }),
        ...(opts.pinBottom === undefined
          ? {}
          : { pinBottom: opts.pinBottom }),
        ...(opts.spacerInsetPx === undefined
          ? {}
          : { spacerInsetPx: opts.spacerInsetPx }),
      })
    })
    for (let i = 1; i <= (opts.rowCount ?? 20); i++) {
      if (opts.premeasuredIds?.includes(`m${i}`) === true)
        mounted.virt.primeHeight(`m${i}`, 100)
      else
        mounted.virt.measure(`m${i}`, 100)
    }
    return { ...mounted, dispose }
  }

  /** A manual animation frame queue in place of the global one, until `afterEach` removes it. */
  function manualFrames() {
    const frames = createManualFrames()
    vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
    vi.stubGlobal('cancelAnimationFrame', frames.cancelFrame)
    return frames
  }

  /**
   * Mount the row `id` as an element with one child, both with stubbed rectangles. The
   * scroll element is at client top 0, so the row starts at `rowScrollTop - scrollTop` in
   * client coordinates, where `rowScrollTop` is the scroll coordinate of the row top.
   * `rowHeight` and `childTop` (from the row top) are live, and the attach reads
   * `rowHeight`. `element` is the row element, for a detach.
   */
  function mountRow(virt: ReturnType<typeof setup>['virt'], id: string, rowScrollTop: number, scrollTop: number, rowHeight = 100) {
    const rowTop = rowScrollTop - scrollTop
    const row = document.createElement('div')
    const geometry = { rowHeight, childTop: 20, element: row }
    row.getBoundingClientRect = () => new DOMRect(0, rowTop, 400, geometry.rowHeight)
    const child = document.createElement('div')
    child.getBoundingClientRect = () => new DOMRect(0, rowTop + geometry.childTop, 400, 70)
    row.append(child)
    document.body.append(row)
    virt.attachRow(id, row)
    return geometry
  }

  /** Mount row 10, which starts at scroll coordinate 1200, as `mountRow` does. */
  function mountRow10(virt: ReturnType<typeof setup>['virt'], scrollTop: number, rowHeight = 100) {
    return mountRow(virt, ROW_10, 1200, scrollTop, rowHeight)
  }

  afterEach(() => {
    vi.unstubAllGlobals()
    document.body.replaceChildren()
  })

  it('stays 0 without a scroll element', () => {
    const { virt, dispose } = mount()
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(0)
    expect(virt.spacerHeight()).toBe(virt.totalHeight())
    dispose()
  })

  it('stays 0 for a hidden element', () => {
    const { scroller } = fakeLayout({ clientHeight: 0, scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(0)
    dispose()
  })

  /**
   * A scroll element that the test can hide (a `clientHeight` of 0) or detach (the accessor
   * returns no element), with the layout of `fakeLayout`.
   */
  function leavingLayout(init: Parameters<typeof fakeLayout>[0]) {
    const layout = fakeLayout(init)
    const element = { attached: true }
    return {
      ...layout,
      scrollElement: () => (element.attached
        ? layout.scroller
        : undefined),
      leave: (how: 'hides' | 'detaches') => {
        if (how === 'hides')
          layout.state.clientHeight = 0
        else
          element.attached = false
      },
    }
  }

  it.each(['hides', 'detaches'] as const)('drops the shift to 0 at a geometry change once the scroll element %s', (how) => {
    const layout = leavingLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scrollElement: layout.scrollElement })
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(-60)

    layout.leave(how)
    virt.measure('m5', 130)

    expect(virt.layoutShift()).toBe(0)
    expect(virt.spacerHeight()).toBe(virt.totalHeight())
    dispose()
  })

  it('keeps the row at the viewport top in place when a row above measures', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    const spacer = virt.spacerHeight()
    expect(virt.offsetOfId(ROW_10)! + virt.layoutShift()).toBe(1200)

    virt.measure('m4', 160)

    expect(virt.offsetOfId(ROW_10)! + virt.layoutShift()).toBe(1200)
    expect(virt.layoutShift()).toBe(-60)
    expect(virt.spacerHeight()).toBe(spacer)
    dispose()
  })

  it('keeps the rows in place across a prepend', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, setList, dispose } = mount({ scroller })

    setList([
      ...plainItems(5, 100),
      ...plainItems(20),
    ])

    expect(virt.offsetOfId(ROW_10)! + virt.layoutShift()).toBe(1200)
    expect(virt.layoutShift()).toBe(-600)
    dispose()
  })

  /**
   * The shift after `measuredId` measures 160px, on a scroll element whose `scrollTop`
   * reads `scrollTop` with no clamp. A rubber-band overscroll in Safari or WebKit can
   * report a `scrollTop` outside the scroll range, above the maximum at the bottom edge.
   * The virtualizer therefore passes `clampScrollTop` of the value to the shift rule.
   * The scroll range is 2380 - 500, so the maximum `scrollTop` is 1880.
   */
  function shiftAtUnclampedScrollTop(scrollTop: number, measuredId: string): number {
    const { scroller } = fakeLayout({ scrollHeight: 2380 })
    Object.defineProperty(scroller, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
    })
    const { virt, dispose } = mount({ scroller })
    virt.measure(measuredId, 160)
    const shift = virt.layoutShift()
    dispose()
    return shift
  }

  it('clamps a scrollTop above the maximum', () => {
    const shiftAt = (scrollTop: number) => shiftAtUnclampedScrollTop(scrollTop, 'm4')
    expect(shiftAt(99_999)).toBe(shiftAt(1880))
    expect(shiftAt(1880)).toBe(-60)
  })

  it('holds the row under the clamped viewport top for a scrollTop above the maximum', () => {
    // Row 15 (`m16`) spans 1800 to 1900, so it holds the clamped viewport top at 1880. It
    // grows below that point, so the shift is 0. An unclamped 99999 lies below the last
    // row instead, which holds its bottom edge and moves the rows up by the 60px growth.
    const shiftAt = (scrollTop: number) => shiftAtUnclampedScrollTop(scrollTop, 'm16')
    expect(shiftAt(1880)).toBe(0)
    expect(shiftAt(99_999)).toBe(shiftAt(1880))
  })

  it('keeps the content bottom while pinBottom is true', () => {
    const { scroller } = fakeLayout({ scrollTop: 1880 })
    const { virt, setList, dispose } = mount({
      scroller,
      pinBottom: () => true,
    })
    const spacer = virt.spacerHeight()

    setList(plainItems(21))

    expect(virt.spacerHeight()).toBe(spacer)
    expect(virt.layoutShift()).toBe(-120)
    dispose()
  })

  it('adds the displacement of the pivot element', () => {
    const frames = manualFrames()
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    const row = mountRow10(virt, 1250)
    virt.updateViewport(1250, 500)
    frames.runFrames()

    row.childTop = 50
    virt.measure(ROW_10, 130)

    expect(virt.layoutShift()).toBe(-30)
    dispose()
  })

  describe('a height that the reader did not see', () => {
    // Row 10 spans the scroll coordinates 1200 to 1300, and row 11 (`m12`) starts at 1320.
    // The viewport top is at 1250, inside row 10, as after an upward scroll into the row.
    const ROW_11 = 'm12'

    it('holds the bottom edge of a premeasured row when its mounted copy reads another height', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })
      const spacer = virt.spacerHeight()
      expect(virt.hasMeasuredHeight(ROW_10)).toBe(true)

      // The copy is 30px taller than the hidden copy measured. Its attach reads it.
      mountRow10(virt, 1250, 130)

      expect(virt.heightOfId(ROW_10)).toBe(130)
      // The rows below row 10 keep their scroll coordinates: the row grows above the
      // reader. The spacer keeps its height, so the scroll range does not move either.
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1320)
      expect(virt.layoutShift()).toBe(-30)
      expect(virt.spacerHeight()).toBe(spacer)
      dispose()
    })

    it('holds the bottom edge when the first read of the copy gives a smaller height', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })

      mountRow10(virt, 1250, 70)

      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1320)
      expect(virt.layoutShift()).toBe(30)
      dispose()
    })

    it('holds the distance from the top edge at a later read of the same copy', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })
      mountRow10(virt, 1250, 130)
      expect(virt.layoutShift()).toBe(-30)

      // The reader saw the row at 130px, so a later growth is a change of content that the
      // reader saw. No frame recorded a pivot element, so the content at the viewport top
      // keeps its distance from the row top, and the rows below move down.
      virt.measure(ROW_10, 170)

      expect(virt.layoutShift()).toBe(-30)
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1360)
      dispose()
    })

    it('holds the distance from the top edge after a first read that confirmed the height', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: [ROW_10],
      })
      // The first read gives the premeasured height, so nothing moves, and the reader sees
      // the row at 100px from then on.
      mountRow10(virt, 1250, 100)
      expect(virt.layoutShift()).toBe(0)

      virt.measure(ROW_10, 140)

      expect(virt.layoutShift()).toBe(0)
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1360)
      dispose()
    })

    it('holds the bottom edge of a mounted row that a hidden copy measured before its first read', () => {
      // The row mounts with an estimate and waits for its content, so no read measures it.
      // A hidden copy then gives it 100px, and its own first read gives 130px.
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      let dispose!: () => void
      const { virt } = createRoot((d) => {
        dispose = d
        return setup(plainItems(20), {
          scrollElement: () => scroller,
        })
      })
      for (let i = 1; i <= 20; i++) {
        if (`m${i}` !== ROW_10)
          virt.measure(`m${i}`, 100)
      }
      const waiting = createRoot((disposeRow) => {
        const [pending, setPending] = createSignal(true)
        const readiness = createRenderReadinessScope()
        readiness.track(pending)
        const row = document.createElement('div')
        row.getBoundingClientRect = () => new DOMRect(0, -50, 400, 130)
        document.body.append(row)
        virt.attachRow(ROW_10, row, readiness)
        return { setPending, disposeRow }
      })
      expect(virt.showsHeight(ROW_10)).toBe(false)
      expect(virt.primeHeight(ROW_10, 100)).toBe(true)
      expect(virt.showsHeight(ROW_10)).toBe(false)
      const shiftBeforeRead = virt.layoutShift()
      const belowBeforeRead = virt.offsetOfId(ROW_11)! + shiftBeforeRead

      // The content lands: the copy turns measurable, and the next flush reads it.
      waiting.setPending(false)
      return Promise.resolve().then(() => {
        expect(virt.heightOfId(ROW_10)).toBe(130)
        expect(virt.showsHeight(ROW_10)).toBe(true)
        expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(belowBeforeRead)
        expect(virt.layoutShift()).toBe(shiftBeforeRead - 30)
        waiting.disposeRow()
        dispose()
      })
    })

    it('holds the bottom edge again for the first read of a copy that mounts after the row unmounted', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({ scroller })
      const first = mountRow10(virt, 1250)
      virt.detachRow(first.element)
      expect(virt.hasMeasuredHeight(ROW_10)).toBe(true)

      // The new copy stands for a fling skeleton that gives way to the row: it shows no
      // content at the cached 100px, and its first read gives 140px.
      mountRow10(virt, 1250, 140)

      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1320)
      expect(virt.layoutShift()).toBe(-40)
      dispose()
    })

    it('keeps the reader\'s view of a row whose new element takes over before the old one detaches', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({ scroller })
      const first = mountRow10(virt, 1250)

      // The row stays mounted across the two elements, so the reader saw its 100px. The
      // new element reads 140px: a change of content that the reader saw.
      mountRow10(virt, 1250, 140)
      virt.detachRow(first.element)

      expect(virt.layoutShift()).toBe(0)
      expect(virt.offsetOfId(ROW_11)! + virt.layoutShift()).toBe(1360)
      dispose()
    })

    describe('showsHeight', () => {
      it('is false for a row with an estimate, and for a row that only a hidden copy measured', () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        let dispose!: () => void
        const { virt } = createRoot((d) => {
          dispose = d
          return setup(plainItems(3), {
            scrollElement: () => scroller,
          })
        })
        expect(virt.showsHeight('m1')).toBe(false)

        virt.primeHeight('m1', 100)

        expect(virt.hasMeasuredHeight('m1')).toBe(true)
        expect(virt.showsHeight('m1')).toBe(false)
        expect(virt.showsHeight('not-in-the-list')).toBe(false)
        dispose()
      })

      it('turns true at the first read of the mounted row, and false again when the row unmounts', () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        const { virt, dispose } = mount({
          scroller,
          premeasuredIds: [ROW_10],
        })
        expect(virt.showsHeight(ROW_10)).toBe(false)

        const copy = mountRow10(virt, 1250, 130)
        expect(virt.showsHeight(ROW_10)).toBe(true)

        virt.detachRow(copy.element)
        expect(virt.hasMeasuredHeight(ROW_10)).toBe(true)
        expect(virt.showsHeight(ROW_10)).toBe(false)
        dispose()
      })

      it('stays true while a new element takes the row over before the old one detaches', () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        const { virt, dispose } = mount({
          scroller,
          premeasuredIds: [ROW_10],
        })
        const first = mountRow10(virt, 1250)
        const second = mountRow10(virt, 1250)

        virt.detachRow(first.element)
        expect(virt.showsHeight(ROW_10)).toBe(true)

        virt.detachRow(second.element)
        expect(virt.showsHeight(ROW_10)).toBe(false)
        dispose()
      })

      it('stays true when the height key of the mounted row changes', () => {
        const { scroller } = fakeLayout({ scrollTop: 1250 })
        let dispose!: () => void
        const { virt, setList } = createRoot((d) => {
          dispose = d
          return setup([
            { id: 'a', heightKey: 'old', hasSpanLines: false },
          ], {
            scrollElement: () => scroller,
          })
        })
        const row = document.createElement('div')
        row.getBoundingClientRect = () => new DOMRect(0, 0, 400, 100)
        document.body.append(row)
        virt.attachRow('a', row)
        expect(virt.showsHeight('a')).toBe(true)

        // The element still shows its 100px, which the row keeps under the new key.
        setList([
          { id: 'a', heightKey: 'new', hasSpanLines: false },
        ])

        expect(virt.showsHeight('a')).toBe(true)
        dispose()
      })
    })

    it('holds the row at the viewport top when a premeasured row above it reads another height', () => {
      const { scroller } = fakeLayout({ scrollTop: 1250 })
      const { virt, dispose } = mount({
        scroller,
        premeasuredIds: ['m4'],
      })

      mountRow(virt, 'm4', 360, 1250, 160)

      expect(virt.offsetOfId(ROW_10)! + virt.layoutShift()).toBe(1200)
      expect(virt.layoutShift()).toBe(-60)
      dispose()
    })
  })

  it('requests no pivot frame while pinBottom is true, and requests one again once it is false', () => {
    const frames = manualFrames()
    let pinned = true
    const { scroller } = fakeLayout({ scrollTop: 1880 })
    const { virt, setList, dispose } = mount({
      scroller,
      pinBottom: () => pinned,
    })

    virt.updateViewport(1880, 500)
    setList(plainItems(21))
    expect(frames.pendingFrames()).toBe(0)

    pinned = false
    virt.updateViewport(1880, 500)
    expect(frames.pendingFrames()).toBe(1)
    dispose()
  })

  it('reads no pivot record from before a commit that pinBottom held', () => {
    const frames = manualFrames()
    let pinned = false
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({
      scroller,
      pinBottom: () => pinned,
    })
    const row = mountRow10(virt, 1250)
    virt.updateViewport(1250, 500)
    frames.runFrames()

    // A commit in follow mode holds the content bottom: row 10 grows by 30px above the
    // child. The rectangle height of the row stays at the last read of the measurer, as
    // after a ResizeObserver delivery, so a frame finds no uncommitted height.
    pinned = true
    row.childTop = 50
    virt.measure(ROW_10, 130)
    expect(virt.layoutShift()).toBe(-30)
    frames.runFrames()

    // Follow mode ends with no scroll, and the next commit holds the viewport top. Follow
    // mode keeps no record, so the content at the viewport top keeps its distance from the
    // row top: the shift stays -30. A record from before follow mode would add the 30px move
    // that the screen already showed, and give -70.
    pinned = false
    row.childTop = 60
    virt.measure(ROW_10, 140)
    expect(virt.layoutShift()).toBe(-30)
    dispose()
  })

  it('records the pivot element once a fast scroll stops, before the fast scroll ends', () => {
    const frames = manualFrames()
    const { scroller } = fakeLayout({ scrollTop: 1200 })
    const { virt, dispose } = mount({ scroller })
    const row = mountRow10(virt, 1250)
    // A fast scroll moves the view to 1250. Its frame sees a new position, so it drops the
    // record and asks for one more frame.
    virt.setFastScrollActive(true)
    scroller.scrollTop = 1250
    virt.updateViewport(1250, 500)
    frames.runFrames()

    // The scroll stopped at 1250, and the fast scroll has not ended: the next frame sees
    // the same position and records.
    frames.runFrames()
    row.childTop = 50
    virt.measure(ROW_10, 130)

    expect(virt.layoutShift()).toBe(-30)
    dispose()
  })

  it('takes the displacement at each step of a repeated resize', () => {
    const frames = manualFrames()
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    const row = mountRow10(virt, 1250)
    virt.updateViewport(1250, 500)
    frames.runFrames()

    // The direct `measure` call leaves the last read of the measurer at 100, so the measurer
    // reports an uncommitted height in both frames, as it does in a browser before the
    // ResizeObserver delivery.
    row.childTop = 50
    row.rowHeight = 130
    virt.updateViewport(1250, 500)
    frames.runFrames()
    virt.measure(ROW_10, 130)
    expect(virt.layoutShift()).toBe(-30)

    row.childTop = 90
    row.rowHeight = 170
    frames.runFrames()
    virt.measure(ROW_10, 170)
    expect(virt.layoutShift()).toBe(-70)
    dispose()
  })

  it('removes the shift on rebase', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(-60)

    expect(virt.rebase()).toBe(-60)
    expect(virt.layoutShift()).toBe(0)
    expect(layout.scroller.scrollTop).toBe(1310)
    layout.writes.length = 0
    expect(virt.rebase()).toBe(0)
    expect(layout.writes).toEqual([])
    dispose()
  })

  it('writes the scroll position first for a positive shift', () => {
    const layout = fakeLayout({ clientHeight: 0, scrollTop: 0 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    // Row 3 measures 300px while the pane is hidden, so no shift comes of it.
    virt.measure('m4', 300)
    expect(virt.layoutShift()).toBe(0)
    layout.state.clientHeight = 500
    layout.state.spacerHeight = `${virt.spacerHeight()}px`
    layout.scroller.scrollTop = 99_999
    expect(layout.scroller.scrollTop).toBe(2080)

    virt.measure('m4', 100)
    expect(virt.layoutShift()).toBe(200)
    layout.writes.length = 0

    expect(virt.rebase()).toBe(200)
    expect(layout.writes).toEqual(['scrollTop', 'spacer', 'layer'])
    expect(layout.scroller.scrollTop).toBe(1880)
    expect(layout.state.spacerHeight).toBe('2380px')
    expect(layout.state.layerTransform).toBe('translateY(0px)')
    dispose()
  })

  it('writes the layout first for a negative shift', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m1', 300)
    expect(virt.layoutShift()).toBe(-200)
    layout.state.spacerHeight = `${virt.spacerHeight()}px`
    layout.scroller.scrollTop = 0
    layout.writes.length = 0

    expect(virt.rebase()).toBe(-200)
    expect(layout.writes).toEqual(['spacer', 'layer', 'scrollTop'])
    expect(layout.scroller.scrollTop).toBe(200)
    expect(layout.state.spacerHeight).toBe('2580px')
    dispose()
  })

  it('rebases without attached layout elements', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.measure('m1', 300)
    expect(virt.layoutShift()).toBe(-200)
    // The scroll range holds the target with no spacer: the content stands for it.
    layout.state.scrollHeight = 2580

    expect(virt.rebase()).toBe(-200)
    expect(layout.scroller.scrollTop).toBe(1450)
    expect(virt.layoutShift()).toBe(0)
    dispose()
  })

  it.each(['hides', 'detaches'] as const)('writes nothing and keeps the shift once the scroll element %s', (how) => {
    const layout = leavingLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scrollElement: layout.scrollElement })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 160)
    expect(virt.layoutShift()).toBe(-60)
    layout.leave(how)
    layout.writes.length = 0

    expect(virt.rebase()).toBe(0)
    expect(layout.writes).toEqual([])
    // The rows keep their place: a shift of 0 with no `scrollTop` write would move them.
    expect(virt.layoutShift()).toBe(-60)
    dispose()
  })

  it('subscribes an effect that rebases to no signal', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 160)
    let runs = 0
    const disposeEffect = createRoot((d) => {
      createRenderEffect(() => {
        runs += 1
        virt.rebase()
      })
      return d
    })
    expect(runs).toBe(1)
    expect(virt.layoutShift()).toBe(0)

    // A new shift and a new total height. An effect that read either one would run again,
    // and its rebase would remove the new shift.
    virt.measure('m5', 130)

    expect(virt.layoutShift()).toBe(-30)
    expect(runs).toBe(1)
    disposeEffect()
    dispose()
  })

  /**
   * The distance (px) from the viewport top to the top of row 10: its scroll coordinate
   * minus `scrollTop`. It stays the same while the content stays in place on screen.
   */
  function row10OnScreen(virt: ReturnType<typeof setup>['virt'], scroller: HTMLElement): number {
    return virt.scrollTopOf(virt.offsetOfId(ROW_10)!) - scroller.scrollTop
  }

  it('keeps the content in place when the browser rounds the written position', () => {
    const layout = fakeLayout({ scrollTop: 1250, roundScrollTop: true })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 160.37)
    expect(row10OnScreen(virt, layout.scroller)).toBeCloseTo(-50, 9)

    // The browser stores the target 1310.37 as 1310, so the shift keeps the 0.37px that the
    // write could not remove.
    expect(virt.rebase()).toBeCloseTo(-60, 9)

    expect(layout.scroller.scrollTop).toBe(1310)
    expect(virt.layoutShift()).toBeCloseTo(-0.37, 9)
    expect(row10OnScreen(virt, layout.scroller)).toBeCloseTo(-50, 9)
    expect(layout.state.layerTransform).toBe(`translateY(${virt.layoutShift()}px)`)
    expect(layout.state.spacerHeight).toBe(`${virt.spacerHeight()}px`)
    dispose()
  })

  it('keeps the content in place across consecutive rebases', () => {
    const layout = fakeLayout({ scrollTop: 1250, roundScrollTop: true })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)

    // Each row above row 10 changes by more than one pixel plus a fraction, so each rebase
    // writes, and the browser rounds each target.
    for (const [id, height] of [
      ['m2', 101.37],
      ['m3', 102.74],
      ['m4', 98.39],
      ['m5', 103.29],
      ['m6', 101.83],
    ] as const) {
      virt.measure(id, height)
      expect(virt.rebase()).not.toBe(0)
      expect(Number.isInteger(layout.scroller.scrollTop)).toBe(true)
      expect(Math.abs(virt.layoutShift())).toBeLessThan(1)
      expect(row10OnScreen(virt, layout.scroller)).toBeCloseTo(-50, 9)
    }
    dispose()
  })

  it('leaves a shift below one pixel in place', () => {
    const layout = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    virt.measure('m4', 100.6)
    expect(virt.layoutShift()).toBeCloseTo(-0.6, 9)
    layout.writes.length = 0

    expect(virt.rebase()).toBe(0)

    expect(layout.writes).toEqual([])
    expect(virt.layoutShift()).toBeCloseTo(-0.6, 9)
    dispose()
  })

  it('drops a remainder of a clamp', () => {
    const layout = fakeLayout({ clientHeight: 0, scrollTop: 0 })
    const { virt, dispose } = mount({ scroller: layout.scroller })
    virt.attachSpacer(layout.spacer)
    virt.attachLayer(layout.layer)
    // Row 3 measures 300px while the pane is hidden, so no shift comes of it. At the bottom,
    // its shrink back to 100px gives a shift of 200.
    virt.measure('m4', 300)
    layout.state.clientHeight = 500
    layout.state.spacerHeight = `${virt.spacerHeight()}px`
    layout.scroller.scrollTop = 99_999
    virt.measure('m4', 100)
    expect(virt.layoutShift()).toBe(200)
    // The reader then scrolls into the blank band above the first row. The target -100 lies
    // outside the scroll range, so the browser clamps it to 0.
    layout.scroller.scrollTop = 100

    expect(virt.rebase()).toBe(200)

    expect(layout.scroller.scrollTop).toBe(0)
    expect(virt.layoutShift()).toBe(0)
    expect(layout.state.spacerHeight).toBe('2380px')
    expect(layout.state.layerTransform).toBe('translateY(0px)')
    dispose()
  })

  it('converts scroll coordinates', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller })
    virt.measure('m4', 200)
    expect(virt.layoutShift()).toBe(-100)
    // The same geometry with no scroll element, so its shift is 0.
    const reference = mount()
    reference.virt.measure('m4', 200)

    expect(virt.contentTopOf(500)).toBe(600)
    expect(virt.scrollTopOf(600)).toBe(500)
    const anchor = virt.anchorAt(500)
    expect(anchor).toEqual(reference.virt.anchorAt(600))
    expect(virt.scrollTopForAnchor(anchor!)).toBe(reference.virt.scrollTopForAnchor(anchor!)! - 100)
    expect(virt.scrollTopNearAnchor(anchor!)).toBe(reference.virt.scrollTopNearAnchor(anchor!)! - 100)
    expect(virt.computeRange(500, 500)).toEqual(reference.virt.computeRange(600, 500))
    reference.dispose()
    dispose()
  })

  it('converts scroll coordinates across the inset of the spacer', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })
    virt.measure('m4', 200)
    const shift = virt.layoutShift()
    expect(shift).toBe(-100)

    expect(virt.contentTopOf(1250)).toBe(1222 - shift)
    expect(virt.scrollTopOf(1222 - shift)).toBe(1250)
    // The anchor of the viewport top lies in row 10 (`m11`), at content 1322.
    expect(virt.anchorAt(1250)).toMatchObject({ id: ROW_10, offsetWithinRow: 22 })
    // Scroll 1210 is content 1282, in the gap below row 9, which spans 1180 to 1280 after row
    // 3 grows. So the slice starts at row 9. Without the inset, content 1310 lies in row 10.
    const reference = mount()
    reference.virt.measure('m4', 200)
    expect(virt.computeRange(1210, 500)).toEqual(reference.virt.computeRange(1282, 500))
    expect(virt.computeRange(1210, 500).start).toBe(9)
    reference.dispose()
    dispose()
  })

  it('places the top of the first row at the top of the scroller, above the inset of the spacer', () => {
    const { scroller } = fakeLayout({ scrollTop: 0 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })
    expect(virt.layoutShift()).toBe(0)

    // A reader at the top of the history sees the inset, which no anchor captures: the anchor
    // floors it to the top of the first row. That anchor resolves back to the top of the
    // scroller, not to the top of the row 28px below it.
    const atTop = virt.anchorAt(0)
    expect(atTop).toMatchObject({ id: 'm1', offsetWithinRow: 0 })
    expect(virt.scrollTopForAnchor(atTop!)).toBe(0)
    expect(virt.scrollTopNearAnchor(atTop!)).toBe(0)
    // A seek lands its row 8px below the viewport top, which lies above the content top for
    // the first row; the scrub's top end is the content top itself.
    expect(virt.placementScrollTopOf(-8)).toBe(0)
    expect(virt.placementScrollTopOf(0)).toBe(0)
    // Below the content top, a placement converts exactly.
    expect(virt.placementScrollTopOf(12)).toBe(40)
    expect(virt.scrollTopForAnchor({ id: 'm1', offsetWithinRow: 12 })).toBe(40)
    expect(virt.scrollTopForAnchor({ id: 'm2', offsetWithinRow: 0 })).toBe(virt.scrollTopOf(virt.offsetOfId('m2')!))
    dispose()
  })

  it('resolves anchors in content coordinates with no conversion', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })
    virt.measure('m4', 200.37)
    expect(virt.layoutShift()).toBeCloseTo(-100.37, 9)
    // The same geometry with no scroll element, so its scroll coordinates are content ones.
    const reference = mount()
    reference.virt.measure('m4', 200.37)

    for (const contentTop of [0, 499.99, 1234.56, 1900]) {
      const anchor = virt.anchorAtContent(contentTop)
      expect(anchor).not.toBeNull()
      expect(anchor).toEqual(reference.virt.anchorAt(contentTop))
      expect(virt.contentTopForAnchor(anchor!)).toBe(reference.virt.scrollTopForAnchor(anchor!))
      // The scroll resolvers agree through the conversions. A resolved anchor is a placement,
      // so it converts as placementScrollTopOf does: the content top resolves to the top of
      // the scroller (see "places the top of the first row at the top of the scroller").
      const viaScroll = virt.anchorAt(virt.scrollTopOf(contentTop))
      expect(viaScroll?.id).toBe(anchor!.id)
      expect(viaScroll?.offsetWithinRow).toBeCloseTo(anchor!.offsetWithinRow, 9)
      expect(virt.scrollTopForAnchor(anchor!)).toBeCloseTo(virt.placementScrollTopOf(virt.contentTopForAnchor(anchor!)!), 9)
    }
    expect(virt.contentTopForAnchor({ id: 'gone', offsetWithinRow: 0 })).toBeNull()
    reference.dispose()
    dispose()
  })

  it('holds the content at the viewport top below the inset of the spacer', () => {
    // With the inset 28, scroll 1188 is content 1160, inside the body of row 9 (1080 to
    // 1180). Without the inset, 1188 would lie in the gap below row 9, where the rule holds
    // the bottom edge of the row.
    const { scroller } = fakeLayout({ scrollTop: 1188 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })

    // Row 9 grows at its bottom, below the viewport top, so the content at the viewport top
    // keeps its place in the row, and nothing above it moves.
    virt.measure('m10', 130)

    expect(virt.layoutShift()).toBe(0)
    dispose()
  })

  it('records the pivot element in the row that the viewport top cuts below the inset of the spacer', () => {
    const frames = manualFrames()
    // With the inset 28, scroll 1188 is content 1160, inside the body of row 9 (`m10`, 1080
    // to 1180), whose top is at scroll 1108. Without the inset, 1188 would lie in the gap
    // below row 9, where no pivot element is recorded.
    const { scroller } = fakeLayout({ scrollTop: 1188 })
    const { virt, dispose } = mount({ scroller, spacerInsetPx: 28 })
    const row = mountRow(virt, 'm10', 1108, 1188)
    virt.updateViewport(1188, 500)
    frames.runFrames()

    // Row 9 grows by 30px above the child, so the content at the viewport top moves 30px
    // down inside the row, and the rows move up by 30px to keep it in place.
    row.childTop = 50
    virt.measure('m10', 130)

    expect(virt.layoutShift()).toBe(-30)
    dispose()
  })

  it('follows a change of the inset of the spacer that an accessor gives', () => {
    // ChatView measures the inset after its first layout, so the inset changes after the
    // mount. Scroll 1188 lies in the gap below row 9 with the inset 0, and inside its body
    // with the inset 28.
    const { scroller } = fakeLayout({ scrollTop: 1188 })
    const [inset, setInset] = createSignal(0)
    const { virt, dispose } = mount({ scroller, spacerInsetPx: inset })
    expect(virt.contentTopOf(1188)).toBe(1188)

    setInset(28)

    expect(virt.contentTopOf(1188)).toBe(1160)
    expect(virt.scrollTopOf(1160)).toBe(1188)
    expect(virt.placementScrollTopOf(12)).toBe(40)
    // The shift rule reads the new inset too: row 9 grows below the viewport top, inside
    // its body, so nothing moves. With the inset 0, the rule would hold the bottom edge of
    // the row and move the rows up by 30px.
    virt.measure('m10', 130)
    expect(virt.layoutShift()).toBe(0)
    dispose()
  })

  it('changes the shift in the same update as the offsets', () => {
    const { scroller } = fakeLayout({ scrollTop: 1250 })
    const records: Array<[number | undefined, number]> = []
    let dispose!: () => void
    const { virt } = createRoot((d) => {
      dispose = d
      const mounted = setup(plainItems(20), {
        scrollElement: () => scroller,
      })
      for (let i = 1; i <= 20; i++)
        mounted.virt.measure(`m${i}`, 100)
      createRenderEffect(() => {
        records.push([
          mounted.virt.offsetOfId(ROW_10),
          mounted.virt.layoutShift(),
        ])
      })
      return mounted
    })
    expect(records).toEqual([
      [1200, 0],
    ])

    virt.measure('m4', 160)
    virt.measure('m5', 130)

    expect(records).toEqual([
      [1200, 0],
      [1260, -60],
      [1290, -90],
    ])
    dispose()
  })
})
