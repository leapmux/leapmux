import type { VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { MAX_LOADED_CHAT_MESSAGES_CEILING } from '~/stores/chat.store'
import { HEIGHT_CACHE_MAX, sameVirtualItems, useChatVirtualizer } from './useChatVirtualizer'
import { fakeRow, makeItems, plainItems, setup } from './useChatVirtualizer.testkit'

describe('usechatvirtualizer geometry', () => {
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

  it('uses the small gap when the lower row has span lines', () => {
    createRoot((dispose) => {
      // rows: plain, span, plain -> gap(0)=small (row1 has span), gap(1)=large (row2 plain)
      const { virt } = setup(makeItems([{ seq: 1 }, { seq: 2, span: true }, { seq: 3 }]))
      expect(virt.offsetOfIndex(1)).toBe(110) // 100 + small gap (lower row has span)
      expect(virt.offsetOfIndex(2)).toBe(230) // +100 + large gap (lower row plain)
      expect(virt.totalHeight()).toBe(330) // +100
      dispose()
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

  it('ignores a zero-height measurement (hidden tab) and leaves the estimate intact', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)
      expect(virt.estimateHeight()).toBe(200)
      // A row in a display:none tab reports height 0 — must be ignored so it
      // doesn't poison the cache or drag the median estimate to ~0.
      expect(virt.measure('m1', 0)).toBe(false)
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
      // thus the whole offset map — into NaN. It must be rejected like a zero.
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
      // whole offset map — into NaN/Infinity. The finite-positive guard rejects it.
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
        { id: 'a', hasSpanLines: false, heightKey: 'k1', kind: 'user_text' },
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
        { id: 'a', hasSpanLines: false, heightKey: 'k1', kind: 'tool_result' },
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
      const items: VirtualItem[] = [{ id: 'a', hasSpanLines: false }]
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
      const items: VirtualItem[] = [{ id: 'a', hasSpanLines: false, heightKey: 'k1' }]
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
      dispose()
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
      setList([{ id: 'a', hasSpanLines: false, heightKey: 'new' }])
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
      setList([{ id: 'a', hasSpanLines: false, heightKey: 'new' }])
      expect(virt.heightOfIndex(0)).toBe(100)
      expect(virt.hasMeasuredHeight('a')).toBe(false)
      dispose()
    })
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

  it('keeps stacked same-seq optimistic locals distinct (keyed by id, not seq)', () => {
    createRoot((dispose) => {
      // Two unsent locals (both seq 0n on the wire) plus a server row. The
      // virtualizer keys by id, so each local gets its own offset and the
      // anchor round-trips to the right row — keying by seq would collapse
      // both locals onto one offset.
      const items: VirtualItem[] = [
        { id: 'server', hasSpanLines: false },
        { id: 'local-a', hasSpanLines: false },
        { id: 'local-b', hasSpanLines: false },
      ]
      const { virt } = setup(items)
      expect(virt.offsetOfId('server')).toBe(0)
      expect(virt.offsetOfId('local-a')).toBe(120)
      expect(virt.offsetOfId('local-b')).toBe(240)
      // Anchor at the top of the FIRST local and confirm it resolves back to
      // that local, not the last one.
      const anchor = virt.anchorAt(120)
      expect(anchor).toEqual({ id: 'local-a', offsetWithinRow: 0, basisHeight: 100, gapFraction: 0 })
      expect(virt.scrollTopForAnchor(anchor!)).toBe(120)
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
      const virt = useChatVirtualizer({ items: list, overscanPx: 0, estimateHeight: 100, gapSmallPx: 10, gapLargePx: 20 })
      // Row 'a' is an UNMEASURED 100px estimate; the 20px gap below it spans [100,120).
      // A scrollTop within the row body has no gap fraction.
      expect(virt.anchorAt(60)).toEqual({ id: 'a', offsetWithinRow: 60, basisHeight: 100, gapFraction: 0 })
      // ...a scrollTop of 110 lands 10px into the 20px gap = HALFWAY (fraction 0.5).
      // offsetWithinRow clamps to the row bottom; gapFraction carries the in-gap part.
      const anchor = virt.anchorAt(110)
      expect(anchor).toEqual({ id: 'a', offsetWithinRow: 100, basisHeight: 100, gapFraction: 0.5 })
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
      const virt = useChatVirtualizer({ items: list, overscanPx: 0, estimateHeight: 100, gapSmallPx: 10, gapLargePx: 20 })
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
      expect(anchor).toEqual({ id: 'a', offsetWithinRow: 50, basisHeight: 100, gapFraction: 0 })
      // Before measure, resolve returns the captured 50 (basis == current fallback).
      expect(virt.scrollTopForAnchor(anchor!)).toBe(50)
      // Row 'a' measures 600. The 0.5 fraction maps to 0.5 * 600 = 300 -- the pin
      // tracks the row's growth instead of staying at a stale 40px.
      virt.measure('a', 600)
      expect(virt.scrollTopForAnchor(anchor!)).toBe(300)
      dispose()
    })
  })

  describe('heightDebugOfId (raw-JSON debug surface)', () => {
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
        const [list, setList] = createSignal<VirtualItem[]>([{ id: 'a', hasSpanLines: false, heightKey: 'old' }])
        const virt = useChatVirtualizer({ items: list, overscanPx: 0, estimateHeight: 100, gapSmallPx: 10, gapLargePx: 20 })
        expect(virt.measure('a', 250)).toBe(true)
        expect(virt.heightDebugOfId('a')).toEqual({ measured: 250 })
        setList([{ id: 'a', hasSpanLines: false, heightKey: 'new' }])
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

describe('samevirtualitems geometry equality', () => {
  const base: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1', seq: 1n, kind: 'user_text' }

  it('is true for arrays equal on the geometry fields', () => {
    expect(sameVirtualItems([{ ...base }], [{ ...base }])).toBe(true)
  })

  it('ignores non-geometry fields (seq) when they differ', () => {
    // seq is an anchor label, not an offset input, so a row that differs ONLY there
    // must still compare equal (else the memo would needlessly rebuild geometry on
    // every streaming delta).
    const other: VirtualItem = { ...base, seq: 999n }
    expect(sameVirtualItems([base], [other])).toBe(true)
  })

  it('is false when any geometry field differs', () => {
    expect(sameVirtualItems([base], [{ ...base, id: 'm2' }])).toBe(false)
    expect(sameVirtualItems([base], [{ ...base, hasSpanLines: true }])).toBe(false)
    expect(sameVirtualItems([base], [{ ...base, heightKey: 'k2' }])).toBe(false)
    // kind feeds the per-kind estimate bucket, so a kind change is geometry-relevant.
    expect(sameVirtualItems([base], [{ ...base, kind: 'tool_result' }])).toBe(false)
  })

  it('is false for different lengths and true for the same reference', () => {
    expect(sameVirtualItems([base], [base, base])).toBe(false)
    const arr = [base]
    expect(sameVirtualItems(arr, arr)).toBe(true)
  })
})

describe('primeheights bulk hydration and snapshotheights', () => {
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
        { id: 'm2', heightKey: 'k-live', height: 0 }, // unusable height
      ])
      expect(adopted).toBe(0)
      expect(virt.geometryVersion()).toBe(before)
      expect(virt.hasMeasuredHeight('m1')).toBe(false)
      dispose()
    })
  })

  it('never overwrites a mounted row\'s live visible measurement', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(2))
      virt.attachRow('m1', fakeRow(150))
      virt.measure('m1', 150)
      const adopted = virt.primeHeights([{ id: 'm1', heightKey: undefined, height: 999 }])
      expect(adopted).toBe(0)
      expect(virt.heightOfIndex(0)).toBe(150)
      dispose()
    })
  })

  it('queues behind the momentum-scroll deferral gate instead of committing', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(2))
      virt.setVisibleMeasurementDeferral(true)
      expect(virt.primeHeights([{ id: 'm1', heightKey: undefined, height: 220 }])).toBe(0)
      expect(virt.hasMeasuredHeight('m1')).toBe(false)
      expect(virt.hasPendingPremeasuredHeight('m1')).toBe(true)
      virt.setVisibleMeasurementDeferral(false)
      expect(virt.flushDeferredMeasurements()).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(220)
      dispose()
    })
  })

  it('snapshotHeights exports the cache in LRU order (oldest measurement first)', () => {
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
