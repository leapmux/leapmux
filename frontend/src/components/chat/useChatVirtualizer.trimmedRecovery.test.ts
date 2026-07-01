import type { TallRowMeasureStats, ViewportUpdateStats, VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { HEIGHT_CACHE_MAX, useChatVirtualizer } from './useChatVirtualizer'
import { fakeRow, makeItems, plainItems, setup } from './useChatVirtualizer.testkit'

describe('usechatvirtualizer geometry', () => {
  describe('scrollTopNearAnchor (trimmed-row recovery)', () => {
    // 5 plain rows (100px, 20px gaps) -> offsets 0,120,240,360,480 -- plus a trailing
    // optimistic local (seq 0n) the nearest-survivor scan must SKIP.
    function seqItems(): VirtualItem[] {
      const rows = [10, 20, 30, 40, 50].map(n => ({ id: `m${n}`, hasSpanLines: false, seq: BigInt(n) }))
      return [...rows, { id: 'local-x', hasSpanLines: false, seq: 0n }]
    }

    it('returns the exact position when the anchored row still resolves', () => {
      createRoot((dispose) => {
        const { virt } = setup(seqItems())
        expect(virt.scrollTopNearAnchor({ id: 'm30', offsetWithinRow: 0, basisHeight: 100, seq: 30n })).toBe(240)
        dispose()
      })
    })

    it('lands on the nearest surviving row by seq when the anchored row was trimmed', () => {
      createRoot((dispose) => {
        const { virt } = setup(seqItems())
        // seq 35 is equidistant from 30 and 40; the scan keeps the FIRST minimum (30 -> 240).
        expect(virt.scrollTopNearAnchor({ id: 'gone', offsetWithinRow: 0, seq: 35n })).toBe(240)
        // An anchor older than the whole window lands on the oldest survivor (seq 10 -> 0).
        expect(virt.scrollTopNearAnchor({ id: 'gone', offsetWithinRow: 0, seq: 5n })).toBe(0)
        // ...and newer than the window lands on the newest server row (seq 50 -> 480).
        expect(virt.scrollTopNearAnchor({ id: 'gone', offsetWithinRow: 0, seq: 99n })).toBe(480)
        dispose()
      })
    })

    it('skips trailing optimistic locals (seq 0n) when picking the nearest survivor', () => {
      createRoot((dispose) => {
        const { virt } = setup(seqItems())
        // seq 2 is closest to the local's seq 0n, but locals are skipped -> seq 10 (offset 0).
        expect(virt.scrollTopNearAnchor({ id: 'gone', offsetWithinRow: 0, seq: 2n })).toBe(0)
        dispose()
      })
    })

    it('returns null for a trimmed anchor that carries no seq (no recovery possible)', () => {
      createRoot((dispose) => {
        const { virt } = setup(seqItems())
        expect(virt.scrollTopNearAnchor({ id: 'gone', offsetWithinRow: 0 })).toBeNull()
        dispose()
      })
    })

    it('returns null when the window holds no server row to land on', () => {
      createRoot((dispose) => {
        const { virt } = setup([{ id: 'local-only', hasSpanLines: false, seq: 0n }])
        expect(virt.scrollTopNearAnchor({ id: 'gone', offsetWithinRow: 0, seq: 5n })).toBeNull()
        dispose()
      })
    })
  })

  it('resolves an OVER-estimated row PROPORTIONALLY so a measure-smaller keeps the fraction (no yank to the bottom)', () => {
    createRoot((dispose) => {
      // Row 'a' starts with a tall fallback before it mounts.
      const items: VirtualItem[] = [
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
      ]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 400,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      // Anchor at 90% into the over-estimated row 'a' (scrollTop 360 / 400 = 0.9).
      const anchor = virt.anchorAt(360)
      expect(anchor).toEqual({ id: 'a', offsetWithinRow: 360, basisHeight: 400, gapFraction: 0 })
      expect(virt.scrollTopForAnchor(anchor!)).toBe(360)

      // Row 'a' mounts and measures much SMALLER (120). An ABSOLUTE clamp would yank
      // the pin to the row bottom (120); proportional keeps the 0.9 fraction ->
      // 0.9 * 120 = 108, so the anchored content stays at the same relative spot.
      virt.measure('a', 120)
      expect(virt.scrollTopForAnchor(anchor!)).toBe(108)
      dispose()
    })
  })

  it('falls back to absolute clamping for an anchor with no basisHeight (old persisted shape)', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3)) // rows 100px + 20px gaps
      // An anchor restored from old persistence carries no basisHeight: resolve clamps
      // absolutely against the current row height rather than scaling.
      expect(virt.scrollTopForAnchor({ id: 'm1', offsetWithinRow: 60 })).toBe(60)
      // An over-large legacy offset clamps to the row height (100), not past it.
      expect(virt.scrollTopForAnchor({ id: 'm1', offsetWithinRow: 500 })).toBe(100)
      dispose()
    })
  })

  it('clamps a negative scrollTop (rubber-band overscroll) to offsetWithinRow 0', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      // Some browsers report a transient NEGATIVE scrollTop during elastic
      // overscroll at the top. indexAtOffset floors it to row 0, so `within`
      // would go negative and store a negative offset that re-pins above the top.
      const anchor = virt.anchorAt(-40)
      expect(anchor).toEqual({ id: 'm1', offsetWithinRow: 0, basisHeight: 100, gapFraction: 0 })
      expect(virt.scrollTopForAnchor(anchor!)).toBe(0)
      dispose()
    })
  })

  it('resolves index and offset by id', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5, 10)) // ids m10..m14
      expect(virt.indexOfId('m12')).toBe(2)
      expect(virt.offsetOfId('m12')).toBe(240)
      expect(virt.indexOfId('m999')).toBe(-1)
      expect(virt.offsetOfId('m999')).toBeUndefined()
      dispose()
    })
  })

  it('computes the visible slice from scrollTop and clientHeight', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5))
      // offsets [0,120,240,360,480,580]; viewport [200,400].
      expect(virt.computeRange(200, 200)).toEqual({ start: 1, end: 4 })
      // Top of the list.
      expect(virt.computeRange(0, 150)).toEqual({ start: 0, end: 2 })
      // Bottom of the list.
      expect(virt.computeRange(580, 100).end).toBe(5)
      dispose()
    })
  })

  it('clamps a stale-high scrollTop to the scrollable range (no last-row-only flash)', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5)) // offsets [0,120,240,360,480,580]
      // After a trim shrinks the spacer, the DOM still reports the old, larger
      // scrollTop for one flush. Clamping it to maxScrollTop (totalHeight -
      // clientHeight = 380) yields the real bottom slice -- the same as scrolling
      // to the true max -- instead of collapsing to {start:4,end:5} (last row only).
      const maxScroll = 580 - 200
      expect(virt.computeRange(5000, 200)).toEqual(virt.computeRange(maxScroll, 200))
      expect(virt.computeRange(5000, 200)).toEqual({ start: 3, end: 5 })
      // A transient NEGATIVE scrollTop (rubber-band overscroll) clamps to the top.
      expect(virt.computeRange(-100, 200)).toEqual(virt.computeRange(0, 200))
      dispose()
    })
  })

  it('handles an empty list', () => {
    createRoot((dispose) => {
      const { virt } = setup([])
      expect(virt.totalHeight()).toBe(0)
      expect(virt.computeRange(0, 500)).toEqual({ start: 0, end: 0 })
      dispose()
    })
  })

  it('handles a single row', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(1))
      expect(virt.totalHeight()).toBe(100)
      expect(virt.computeRange(0, 500)).toEqual({ start: 0, end: 1 })
      dispose()
    })
  })

  it('evicts the least-recently-measured height once over the cap', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(1))
      // Measured first -> the LRU tail and first eviction target.
      virt.measure('outlier', 500)
      // HEIGHT_CACHE_MAX more distinct rows pushes the cache one past the cap, so
      // 'outlier' is evicted and its 500px is removed from the median histogram.
      for (let i = 0; i < HEIGHT_CACHE_MAX; i++)
        virt.measure(`x${i}`, 100)
      expect(virt.estimateHeight()).toBe(100)
      dispose()
    })
  })

  it('never evicts a MOUNTED row, even when it is the oldest measured', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(1)) // item m1 at index 0
      // Mounting a keyed row attaches its element, joining the protected set and
      // measuring it (333). Attached FIRST, so m1 is the oldest insertion ->
      // normally the first eviction target once the cache crosses its cap.
      virt.attachRow('m1', fakeRow(333))
      // Flood the height cache past the cap (HEIGHT_CACHE_MAX) with other, UNMOUNTED
      // rows. Without the mounted-row protection m1 (oldest) would be evicted and
      // fall back to the running-mean estimate; mounted, it keeps its 333px.
      for (let i = 0; i <= HEIGHT_CACHE_MAX; i++)
        virt.measure(`x${i}`, 100)
      expect(virt.heightOfIndex(0)).toBe(333)
      // Unmount m1 -> it leaves the protected set but keeps its cached height
      // (flash-free re-entry); a further over-cap flood may now evict it.
      dispose()
    })
  })

  it('defers visible-row attach measurements until the deferral is released', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(1))
      const row = fakeRow(333)
      document.body.append(row)

      virt.setVisibleMeasurementDeferral(true)
      virt.attachRow('m1', row)

      expect(virt.hasDeferredMeasurements()).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(100)
      expect(virt.totalHeight()).toBe(100)

      virt.setVisibleMeasurementDeferral(false)
      expect(virt.flushDeferredMeasurements()).toBe(true)
      expect(virt.hasDeferredMeasurements()).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(333)
      expect(virt.totalHeight()).toBe(333)
      row.remove()
      dispose()
    })
  })

  it('defers hidden premeasure commits until the measurement deferral is released', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))

      virt.setVisibleMeasurementDeferral(true)
      expect(virt.primeHeight('m1', 700)).toBe(false)
      expect(virt.primeHeight('m1', 852)).toBe(false)
      expect(virt.primeHeight('m1', 0)).toBe(false)

      expect(virt.hasDeferredMeasurements()).toBe(true)
      expect(virt.hasPendingPremeasuredHeight('m1')).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(100)
      expect(virt.totalHeight()).toBe(340)

      virt.setVisibleMeasurementDeferral(false)
      expect(virt.flushDeferredMeasurements()).toBe(true)

      expect(virt.hasDeferredMeasurements()).toBe(false)
      expect(virt.hasPendingPremeasuredHeight('m1')).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(852)
      expect(virt.totalHeight()).toBe(2596)
      dispose()
    })
  })

  it('queues a hidden premeasure while measurement is deferred and applies it on flush', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(2))
      virt.measure('m2', 100)
      // m1 is unmeasured -> it reserves the estimate (100) throughout the deferral.
      virt.setVisibleMeasurementDeferral(true)
      expect(virt.primeHeight('m1', 300)).toBe(false) // queued behind the deferral, not applied

      expect(virt.hasPendingPremeasuredHeight('m1')).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(100) // still the estimate -- the queued 300 hasn't applied
      expect(virt.offsetOfIndex(1)).toBe(120) // m1 estimate(100) + gap(20)
      expect(virt.totalHeight()).toBe(220) // 100 + 20 + 100

      virt.setVisibleMeasurementDeferral(false)
      expect(virt.flushDeferredMeasurements()).toBe(true)

      expect(virt.hasPendingPremeasuredHeight('m1')).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(300)
      expect(virt.offsetOfIndex(1)).toBe(320)
      expect(virt.totalHeight()).toBe(420)
      dispose()
    })
  })

  it('reflects mounted rows in mountedIds: attachRow adds an id, detachRow removes it', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3)) // m1, m2, m3
      expect(virt.mountedIds.size).toBe(0)
      // attachRow joins the protected set, keyed by the SAME element instance that
      // detachRow later resolves back to an id through the reverse elToId map.
      const r1 = fakeRow(100)
      const r2 = fakeRow(120)
      virt.attachRow('m1', r1)
      virt.attachRow('m2', r2)
      expect(virt.mountedIds.has('m1')).toBe(true)
      expect(virt.mountedIds.has('m2')).toBe(true)
      expect(virt.mountedIds.size).toBe(2)
      // Detaching one element drops only its id; the other stays mounted (and
      // protected). mountedIds is the live set, so the getter reflects the change.
      virt.detachRow(r1)
      expect(virt.mountedIds.has('m1')).toBe(false)
      expect(virt.mountedIds.has('m2')).toBe(true)
      expect(virt.mountedIds.size).toBe(1)
      dispose()
    })
  })

  it('keeps a measured height when the row leaves and re-enters the list', () => {
    createRoot((dispose) => {
      const { virt, setList } = setup(plainItems(3)) // m1,m2,m3
      virt.measure('m2', 300)
      expect(virt.heightOfIndex(1)).toBe(300)
      // m2 trimmed out of the window...
      setList(makeItems([{ seq: 1 }, { seq: 3 }]))
      expect(virt.indexOfId('m2')).toBe(-1)
      // ...then re-fetched. Its cached height is reused (no flash to estimate).
      setList(plainItems(3))
      expect(virt.heightOfIndex(1)).toBe(300)
      dispose()
    })
  })

  it('extends the rendered slice ahead in the fling direction (render-ahead overscan)', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(50)) // 100px rows, 20px gaps -> offset[i] = i*120
      // Mid-list viewport, 2 rows tall at scrollTop 2400. The setup's 0 base overscan
      // keeps the slice tight, so a lead extends ONLY the side it points at.
      expect(virt.computeRange(2400, 240)).toEqual({ start: 20, end: 22 })
      // Flinging UP ('older') renders earlier rows ahead (start drops by 600/120 = 5);
      // the trailing edge is untouched.
      expect(virt.computeRange(2400, 240, { dir: 'older', px: 600 })).toEqual({ start: 15, end: 22 })
      // Flinging DOWN ('newer') renders later rows ahead (end rises by 5); start untouched.
      expect(virt.computeRange(2400, 240, { dir: 'newer', px: 600 })).toEqual({ start: 20, end: 27 })
      // A non-positive lead is the symmetric overscan (no extension).
      expect(virt.computeRange(2400, 240, { dir: 'older', px: 0 })).toEqual({ start: 20, end: 22 })
      dispose()
    })
  })

  it('render-ahead covers the rows the next fling frame lands on (no unrendered flash)', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(50))
      // A hard up-fling jumps the viewport 600px per frame. The render-ahead for THIS
      // frame must already include the top row the NEXT frame will show, so the
      // compositor never paints a row this slice hasn't mounted yet.
      const perFrameJump = 600
      const thisFrame = virt.computeRange(3000, 240, { dir: 'older', px: perFrameJump })
      const nextFrameTopRow = virt.computeRange(3000 - perFrameJump, 240).start
      expect(thisFrame.start).toBeLessThanOrEqual(nextFrameTopRow)
      dispose()
    })
  })

  it('keeps adjacent rows mounted when one row is taller than the pixel overscan band', () => {
    createRoot((dispose) => {
      const [list] = createSignal(plainItems(5))
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 1200,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })
      // Row m2 is taller than viewport + top/bottom overscan, so the pure pixel
      // intersection in the middle of the row would collapse the slice to just m2.
      // Keep its immediate neighbors mounted anyway: crossing a tall-row boundary
      // should have overlapping DOM rather than replacing every rendered row at once.
      expect(virt.measure('m2', 5000)).toBe(true)
      expect(virt.heightOfIndex(0)).toBe(100)
      expect(virt.heightOfIndex(1)).toBe(5000)

      // Offsets: m1 [0,100), gap, m2 [120,5120). At scrollTop 2500, the 1200px
      // overscan band [1300,4200] sits wholly inside m2.
      expect(virt.computeRange(2500, 500)).toEqual({ start: 0, end: 3 })
      dispose()
    })
  })

  it('reports range diagnostics when the overscan band fits inside a tall row', () => {
    createRoot((dispose) => {
      const [list] = createSignal(plainItems(5))
      let reported: ViewportUpdateStats | undefined
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 1200,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        shouldReportPerf: () => true,
        onViewportUpdate: stats => reported = stats,
      })

      expect(virt.measure('m2', 5000)).toBe(true)
      virt.updateViewport(2500, 500)

      expect(reported?.nextStart).toBe(0)
      expect(reported?.nextEnd).toBe(3)
      expect(reported?.tallRow).toMatchObject({
        reason: 'single-row-window',
        rowCount: 5,
        totalHeight: 5480,
        maxScrollTop: 4980,
        clampedScrollTop: 2500,
        scrollTopWasClamped: false,
        overscanPx: 1200,
        overTop: 1200,
        overBottom: 1200,
        guardBandPx: 1200,
        overscanTop: 1300,
        overscanBottom: 4200,
        rawStart: 1,
        rawEnd: 2,
        expandedForTallRow: true,
        tallRowIndex: 1,
        tallRowId: 'm2',
        tallRowHeight: 5000,
        tallRowHeightSource: 'measured',
        tallRowTop: 120,
        tallRowBottom: 5120,
        viewportTopOffsetInTallRow: 2380,
        viewportBottomOffsetInTallRow: 2880,
      })
      dispose()
    })
  })

  it('reports tall-row measurement diagnostics without changing the fallback estimate', () => {
    createRoot((dispose) => {
      const [list] = createSignal(plainItems(5))
      let reported: TallRowMeasureStats | undefined
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 1200,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        shouldReportPerf: () => true,
        onTallRowMeasure: stats => reported = stats,
      })

      expect(virt.measure('m2', 5000)).toBe(true)

      expect(reported).toMatchObject({
        id: 'm2',
        source: 'visible',
        height: 5000,
        firstMeasure: true,
        fallbackExcluded: true,
        previousFallbackExcluded: false,
        fallbackEstimateBefore: 100,
        fallbackEstimateAfter: 100,
        geometryVersionBefore: 0,
        geometryVersionAfter: 1,
        indexBefore: 1,
        indexAfter: 1,
        rowTopBefore: 120,
        rowTopAfter: 120,
        totalHeightBefore: 580,
        totalHeightAfter: 5480,
      })
      dispose()
    })
  })

  it('updates fallback contribution when an epsilon re-measure crosses the outlier threshold', () => {
    createRoot((dispose) => {
      const [list] = createSignal(plainItems(2))
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
      })

      expect(virt.measure('m1', 1200.2)).toBe(true)
      expect(virt.estimateHeight()).toBe(100)
      expect(virt.totalHeight()).toBeCloseTo(1320.2)

      expect(virt.measure('m1', 1199.9)).toBe(true)
      expect(virt.estimateHeight()).toBe(1199.9)
      expect(virt.totalHeight()).toBeCloseTo(2419.8)

      expect(virt.measure('m1', 1200.2)).toBe(true)
      expect(virt.estimateHeight()).toBe(100)
      expect(virt.totalHeight()).toBeCloseTo(1320.2)
      dispose()
    })
  })

  it('clamps the render-ahead to the list bounds (no negative start / past-end overflow)', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(50)) // offset[i] = i*120, n = 50
      // A huge older-lead near the TOP would push `top` far negative; start clamps at
      // 0 rather than underflowing (without the lead this viewport starts at row 5).
      expect(virt.computeRange(600, 240).start).toBe(5)
      expect(virt.computeRange(600, 240, { dir: 'older', px: 10000 }).start).toBe(0)
      // A huge newer-lead near the BOTTOM clamps end at n (50), never past the array.
      expect(virt.computeRange(5000, 240, { dir: 'newer', px: 10000 }).end).toBe(50)
      dispose()
    })
  })

  it('updateViewport only re-emits range when the slice actually changes', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(5))
      virt.updateViewport(200, 200)
      const first = virt.range()
      expect(first).toEqual({ start: 1, end: 4 })
      // A tiny scroll that doesn't change the slice keeps the same object.
      virt.updateViewport(205, 200)
      expect(virt.range()).toBe(first)
      dispose()
    })
  })

  it('reports viewport update stats for changed and unchanged slices', () => {
    createRoot((dispose) => {
      const [list] = createSignal(plainItems(5))
      const updates: ViewportUpdateStats[] = []
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        onViewportUpdate: stats => updates.push(stats),
      })

      virt.updateViewport(200, 200)
      expect(updates[0]).toMatchObject({
        scrollTop: 200,
        clientHeight: 200,
        leadDir: undefined,
        leadPx: 0,
        previousStart: 0,
        previousEnd: 0,
        nextStart: 1,
        nextEnd: 4,
        previousRows: 0,
        nextRows: 3,
        addedRows: 3,
        removedRows: 0,
        rangeChanged: true,
      })
      expect(updates[0].computeMs).toBeGreaterThanOrEqual(0)
      expect(updates[0].totalMs).toBeGreaterThanOrEqual(updates[0].computeMs)

      virt.updateViewport(205, 200, { dir: 'newer', px: 50 })
      expect(updates[1]).toMatchObject({
        scrollTop: 205,
        clientHeight: 200,
        leadDir: 'newer',
        leadPx: 50,
        previousStart: 1,
        previousEnd: 4,
        nextStart: 1,
        nextEnd: 4,
        previousRows: 3,
        nextRows: 3,
        addedRows: 0,
        removedRows: 0,
        rangeChanged: false,
      })
      expect(virt.range()).toEqual({ start: 1, end: 4 })
      dispose()
    })
  })

  it('does not collect perf hook stats when the runtime gate is closed', () => {
    createRoot((dispose) => {
      const [list] = createSignal(plainItems(5))
      const viewportUpdates: ViewportUpdateStats[] = []
      const attachStats: unknown[] = []
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        shouldReportPerf: () => false,
        onViewportUpdate: stats => viewportUpdates.push(stats),
        onRowAttachMeasure: stats => attachStats.push(stats),
      })

      virt.updateViewport(200, 200)
      virt.attachRow('m1', fakeRow(120))

      expect(viewportUpdates).toEqual([])
      expect(attachStats).toEqual([])
      expect(virt.range()).toEqual({ start: 1, end: 4 })
      expect(virt.heightOfIndex(0)).toBe(120)
      dispose()
    })
  })
})
