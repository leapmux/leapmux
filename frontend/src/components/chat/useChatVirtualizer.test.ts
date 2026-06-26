import type { VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { ESTIMATE_CACHE_MAX, HEIGHT_CACHE_MAX, sameVirtualItems, useChatVirtualizer } from './useChatVirtualizer'

function makeItems(specs: Array<{ seq: number, span?: boolean }>): VirtualItem[] {
  // `seq` is only a convenient way to derive a unique row id in these specs; the
  // virtualizer itself keys everything by `id`.
  return specs.map(s => ({ id: `m${s.seq}`, hasSpanLines: !!s.span }))
}

function plainItems(count: number, startSeq = 1): VirtualItem[] {
  return makeItems(Array.from({ length: count }, (_, i) => ({ seq: startSeq + i })))
}

/** A detached DOM row whose measured height is `h` (jsdom reports 0 otherwise). */
function fakeRow(h: number): HTMLElement {
  const el = document.createElement('div')
  el.getBoundingClientRect = () => ({ height: h }) as DOMRect
  return el
}

/** Build a virtualizer with deterministic geometry for math assertions. */
function setup(items: VirtualItem[]) {
  const [list, setList] = createSignal(items)
  const virt = useChatVirtualizer({
    items: list,
    overscanPx: 0,
    estimateHeight: 100,
    gapSmallPx: 10,
    gapLargePx: 20,
  })
  return { virt, setList }
}

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

  it('ignores a zero-height measurement (hidden tab) and leaves the estimate intact', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)
      expect(virt.estimateHeight()).toBe(200)
      // A row in a display:none tab reports height 0 — must be ignored so it
      // doesn't poison the cache or drag the running-mean estimate to ~0.
      expect(virt.measure('m1', 0)).toBe(false)
      expect(virt.heightOfIndex(0)).toBe(200)
      expect(virt.estimateHeight()).toBe(200)
      dispose()
    })
  })

  it('ignores a NaN-height measurement so it cannot poison the running-mean estimate', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)
      expect(virt.estimateHeight()).toBe(200)
      // A stray NaN height (`NaN <= 0` is false, so the old non-positive guard
      // let it through) would flow into the running mean and turn estimateHeight — and
      // thus the whole offset map — into NaN. It must be rejected like a zero.
      expect(virt.measure('m2', Number.NaN)).toBe(false)
      expect(virt.estimateHeight()).toBe(200)
      expect(Number.isFinite(virt.totalHeight())).toBe(true)
      dispose()
    })
  })

  it('ignores an Infinity-height measurement so it cannot poison the running-mean estimate', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(3))
      virt.measure('m1', 200)
      expect(virt.estimateHeight()).toBe(200)
      // A stray Infinity height passes a bare `height > 0` test (`Infinity > 0` is
      // true), so it would flow into the running mean and turn estimateHeight — and the
      // whole offset map — into NaN/Infinity. The finite-positive guard rejects it.
      expect(virt.measure('m2', Number.POSITIVE_INFINITY)).toBe(false)
      expect(virt.estimateHeight()).toBe(200)
      expect(Number.isFinite(virt.totalHeight())).toBe(true)
      dispose()
    })
  })

  it('estimate is the running mean of measured rows', () => {
    createRoot((dispose) => {
      const { virt } = setup(plainItems(4))
      virt.measure('m1', 200)
      virt.measure('m2', 100)
      expect(virt.estimateHeight()).toBe(150) // (200+100)/2
      // Unmeasured m3/m4 now use 150.
      expect(virt.heightOfIndex(2)).toBe(150)
      dispose()
    })
  })

  it('uses the injected per-item estimate for an unmeasured row', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
      ]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        // Per-item analytical estimate: 'a' is a tall diff, 'b' a short header.
        // Unmeasured rows use this, NOT the flat seed/mean.
        estimate: item => (item.id === 'a' ? 500 : 30),
      })
      expect(virt.heightOfIndex(0)).toBe(500)
      expect(virt.heightOfIndex(1)).toBe(30)
      expect(virt.offsetOfIndex(1)).toBe(520) // 500 + large gap (20)
      dispose()
    })
  })

  it('falls back to the running mean when the estimator THROWS (a malformed payload must not blank the list)', () => {
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
        // The estimator throws for 'a' (a provider heightMetrics hook faulting on a
        // malformed payload). It runs inside geom's createMemo, which has no error
        // boundary, so an uncaught throw would blank the ENTIRE list. It must be
        // contained to the running-mean fallback for just that row.
        estimate: (item) => {
          if (item.id === 'a')
            throw new Error('malformed payload')
          return 30
        },
        estimateEpoch: () => 800, // exercise the memoized path ChatView uses
      })
      expect(() => virt.totalHeight()).not.toThrow()
      expect(virt.heightOfIndex(0)).toBe(100) // 'a' threw -> running-mean seed
      expect(virt.heightOfIndex(1)).toBe(30) // 'b' estimates normally
      dispose()
    })
  })

  it('memoizes per-row estimates across measurements and re-estimates on a width-epoch change', () => {
    createRoot((dispose) => {
      const [list] = createSignal(makeItems([{ seq: 1 }, { seq: 2 }, { seq: 3 }]))
      const [width, setWidth] = createSignal(800)
      const calls = new Map<string, number>()
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        // A width-dependent estimate, so a width change MUST re-estimate.
        estimate: (item) => {
          calls.set(item.id, (calls.get(item.id) ?? 0) + 1)
          return width() / 8
        },
        estimateEpoch: width,
      })
      // The initial geom scan estimates each row exactly once.
      expect(virt.totalHeight()).toBeGreaterThan(0)
      expect([calls.get('m1'), calls.get('m2'), calls.get('m3')]).toEqual([1, 1, 1])

      // Measuring m1 bumps geomVersion -> geom recomputes, but m2/m3 reuse their
      // cached estimates (the estimator is NOT re-run for them).
      virt.measure('m1', 200)
      virt.totalHeight()
      expect([calls.get('m2'), calls.get('m3')]).toEqual([1, 1])

      // A width change invalidates the cache -> every still-unmeasured row is
      // re-estimated at the new width; the measured m1 stays on heightCache.
      setWidth(1600)
      virt.totalHeight()
      expect([calls.get('m2'), calls.get('m3')]).toEqual([2, 2])
      expect(calls.get('m1')).toBe(1)
      expect(virt.heightOfIndex(1)).toBe(200) // 1600 / 8
      dispose()
    })
  })

  it('prunes the estimate cache when a row leaves the window, so re-adding it re-estimates', () => {
    createRoot((dispose) => {
      const [list, setList] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, estimateKey: 's1' },
        { id: 'b', hasSpanLines: false, estimateKey: 's1' },
        { id: 'c', hasSpanLines: false, estimateKey: 's1' },
      ])
      const calls = new Map<string, number>()
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: (item) => {
          calls.set(item.id, (calls.get(item.id) ?? 0) + 1)
          return 50
        },
        estimateEpoch: () => '800', // constant epoch: never a wholesale clear
      })
      virt.totalHeight()
      expect([calls.get('a'), calls.get('b'), calls.get('c')]).toEqual([1, 1, 1])

      // 'b' leaves the loaded window (a trim). geom's retain drops its cached
      // estimate; the still-resident rows keep theirs (not re-estimated).
      setList([
        { id: 'a', hasSpanLines: false, estimateKey: 's1' },
        { id: 'c', hasSpanLines: false, estimateKey: 's1' },
      ])
      virt.totalHeight()
      expect([calls.get('a'), calls.get('c')]).toEqual([1, 1])

      // 'b' returns under the SAME id + estimateKey. Because its entry was pruned on
      // leaving (not merely aged behind the backstop cap), it re-estimates rather
      // than handing back a value that's no longer guaranteed current.
      setList([
        { id: 'a', hasSpanLines: false, estimateKey: 's1' },
        { id: 'b', hasSpanLines: false, estimateKey: 's1' },
        { id: 'c', hasSpanLines: false, estimateKey: 's1' },
      ])
      virt.totalHeight()
      expect(calls.get('b')).toBe(2)
      expect([calls.get('a'), calls.get('c')]).toEqual([1, 1])
      dispose()
    })
  })

  it('re-estimates an unmeasured row whose estimateKey (content version) changed in place at a constant epoch', () => {
    createRoot((dispose) => {
      // An off-screen row's content changes in place under a STABLE id (a reseq /
      // notification consolidation): same id, new estimateKey, unchanged epoch and
      // never measured. Keyed by id alone the estimate cache would hand back the
      // pre-change height; the per-row content token must bust it.
      const [list, setList] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, estimateKey: 's1' },
        { id: 'b', hasSpanLines: false, estimateKey: 's1' },
      ])
      const width = 800
      const heights: Record<string, number> = { a: 50, b: 50 }
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: item => heights[item.id],
        estimateEpoch: () => `${width}`, // constant: a content change is NOT a global epoch change
      })
      expect(virt.heightOfIndex(0)).toBe(50)

      // Row 'a' grows in place (reseq): bump ONLY its estimateKey + its estimate.
      heights.a = 300
      setList([
        { id: 'a', hasSpanLines: false, estimateKey: 's2' },
        { id: 'b', hasSpanLines: false, estimateKey: 's1' },
      ])
      // 'a' re-estimates (its key changed); 'b' keeps its cached estimate.
      expect(virt.heightOfIndex(0)).toBe(300)
      expect(virt.heightOfIndex(1)).toBe(50)
      dispose()
    })
  })

  it('reuses the cached estimate across a measurement-only recompute when estimateKey is unchanged', () => {
    createRoot((dispose) => {
      const [list] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, estimateKey: 's1' },
        { id: 'b', hasSpanLines: false, estimateKey: 's1' },
      ])
      const calls = new Map<string, number>()
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: (item) => {
          calls.set(item.id, (calls.get(item.id) ?? 0) + 1)
          return 60
        },
        estimateEpoch: () => 'const',
      })
      virt.totalHeight()
      expect([calls.get('a'), calls.get('b')]).toEqual([1, 1])
      // Measuring 'a' bumps geomVersion; with an unchanged estimateKey, 'b' must
      // NOT be re-estimated (the memoization win the content token must preserve).
      virt.measure('a', 200)
      virt.totalHeight()
      expect(calls.get('b')).toBe(1)
      dispose()
    })
  })

  it('caches a poison marker for an unusable estimate so a malformed row is not re-parsed every geom pass, yet still tracks the live running mean', () => {
    createRoot((dispose) => {
      const [list] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, estimateKey: 's1' }, // estimator always unusable
        { id: 'b', hasSpanLines: false, estimateKey: 's1' },
        { id: 'c', hasSpanLines: false, estimateKey: 's1' },
      ])
      const calls = new Map<string, number>()
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        // 'a' is a malformed payload: the estimator returns NaN every time. Its
        // estimateKey never changes and it never measures while off-screen, so
        // without a poison marker it would re-parse on EVERY geom recompute.
        estimate: (item) => {
          calls.set(item.id, (calls.get(item.id) ?? 0) + 1)
          return item.id === 'a' ? Number.NaN : 60
        },
        estimateEpoch: () => 'const',
      })
      // Initial geom scan runs the estimator once per row; 'a' is unusable and gets
      // a poison marker, falling back to the running-mean seed (no row measured yet).
      virt.totalHeight()
      expect(calls.get('a')).toBe(1)
      expect(virt.heightOfIndex(0)).toBe(100)

      // Two measurement-only recomputes (each bumps geomVersion -> geom re-runs).
      // 'a' stays an unmeasured estimate; the poison marker holds its call count at
      // 1 instead of re-parsing the malformed payload on each pass.
      virt.measure('b', 200)
      virt.totalHeight()
      virt.measure('c', 200)
      virt.totalHeight()
      expect(calls.get('a')).toBe(1)

      // A poison hit returns the LIVE running mean, not a frozen seed: with b and c
      // measured at 200, 'a' now falls back to 200 rather than the original 100.
      expect(virt.heightOfIndex(0)).toBe(200)
      expect(calls.get('a')).toBe(1) // reading the fallback must not re-run the estimator
      dispose()
    })
  })

  it('re-runs the estimator for a poisoned row only after its estimateKey changes (the in-place fix lands)', () => {
    createRoot((dispose) => {
      const [list, setList] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false, estimateKey: 's1' },
      ])
      const calls = new Map<string, number>()
      let usable = false
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: (item) => {
          calls.set(item.id, (calls.get(item.id) ?? 0) + 1)
          return usable ? 300 : Number.NaN
        },
        estimateEpoch: () => 'const',
      })
      virt.totalHeight()
      expect(calls.get('a')).toBe(1)
      expect(virt.heightOfIndex(0)).toBe(100) // poison -> running-mean seed

      // The row's content is fixed in place (a reseq lands valid metadata): its
      // estimateKey changes, which must bust the poison marker and re-estimate.
      usable = true
      setList([{ id: 'a', hasSpanLines: false, estimateKey: 's2' }])
      expect(virt.heightOfIndex(0)).toBe(300)
      expect(calls.get('a')).toBe(2)
      dispose()
    })
  })

  it('keeps both cache bounds above the chat window ceiling', async () => {
    // The height cache is LRU-capped with mountedIds protected, so its cap must
    // exceed the largest in-memory window or it would evict in-window rows. The
    // estimate cache is bounded by window liveness (geom's retain), so its cap is a
    // pure backstop -- but it's kept above the ceiling too so the backstop never
    // becomes a hot eviction path. Pin the margin so raising the window ceiling past
    // either cap can't silently regress it.
    const { MAX_LOADED_CHAT_MESSAGES_CEILING } = await import('~/stores/chat.store')
    expect(ESTIMATE_CACHE_MAX).toBeGreaterThan(MAX_LOADED_CHAT_MESSAGES_CEILING)
    expect(HEIGHT_CACHE_MAX).toBeGreaterThan(MAX_LOADED_CHAT_MESSAGES_CEILING)
  })

  it('re-estimates unmeasured rows when the epoch changes at CONSTANT width (a global UI toggle)', () => {
    createRoot((dispose) => {
      // Models ChatView's composite epoch: the estimate output depends on a
      // global pref (e.g. expandAgentThoughts) as well as the content width, so
      // the epoch must fold the pref in. Width is held constant here; only the
      // pref flips. A stale estimate-cache (keyed on width alone) would hand back
      // the pre-toggle height for every off-screen row -- the exact bug this guards.
      const [list] = createSignal(makeItems([{ seq: 1 }, { seq: 2 }]))
      const [expanded, setExpanded] = createSignal(false)
      const width = 800
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: () => (expanded() ? 400 : 50),
        // Epoch folds in the non-width input -> flipping it busts the cache.
        estimateEpoch: () => `${width}|${expanded() ? 1 : 0}`,
      })
      expect(virt.heightOfIndex(0)).toBe(50)
      expect(virt.totalHeight()).toBe(120) // 50 + 20 + 50
      setExpanded(true)
      expect(virt.heightOfIndex(0)).toBe(400) // re-estimated, not the stale 50
      expect(virt.totalHeight()).toBe(820) // 400 + 20 + 400
      dispose()
    })
  })

  it('without an estimate epoch, re-estimates every unmeasured row on each measurement (un-memoized path)', () => {
    createRoot((dispose) => {
      const [list] = createSignal(makeItems([{ seq: 1 }, { seq: 2 }]))
      let m2Calls = 0
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: (item) => {
          if (item.id === 'm2')
            m2Calls += 1
          return 100
        },
        // No estimateEpoch -> the prior, un-memoized behavior.
      })
      virt.totalHeight()
      expect(m2Calls).toBe(1)
      virt.measure('m1', 200)
      virt.totalHeight()
      expect(m2Calls).toBe(2) // re-estimated each scan, no cache
      dispose()
    })
  })

  it('falls back to the running mean when the injected estimate is non-finite, keeping the offset map sane', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
        { id: 'c', hasSpanLines: false },
      ]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        // A misbehaving estimator: NaN for 'b' (e.g. malformed diff metadata),
        // Infinity for 'c'. Feeding either straight into the cumulative offset
        // map would turn every later offset into NaN/Infinity and blank the list.
        estimate: item => (item.id === 'a' ? 200 : item.id === 'b' ? Number.NaN : Number.POSITIVE_INFINITY),
      })
      // 'a' is fine; 'b'/'c' fall back to the seed running-mean (100).
      expect(virt.heightOfIndex(0)).toBe(200)
      expect(virt.heightOfIndex(1)).toBe(100)
      expect(virt.heightOfIndex(2)).toBe(100)
      // The whole offset map stays finite: 200 + 20 + 100 + 20 + 100.
      expect(virt.offsetOfIndex(2)).toBe(340)
      expect(Number.isFinite(virt.totalHeight())).toBe(true)
      expect(virt.totalHeight()).toBe(440)
      dispose()
    })
  })

  it('lets a measured height override the injected estimate', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [{ id: 'a', hasSpanLines: false }]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: () => 500,
      })
      expect(virt.heightOfIndex(0)).toBe(500) // estimate first
      virt.measure('a', 333)
      expect(virt.heightOfIndex(0)).toBe(333) // measured wins, authoritative
      dispose()
    })
  })

  it('recomputes offsets when the estimate\'s reactive dependency changes', () => {
    createRoot((dispose) => {
      // Models the content-width signal: the injected estimate reads it, so geom
      // tracks it and re-estimates unmeasured rows when it changes (the mechanism
      // behind a viewport resize re-wrapping prose for off-screen rows).
      const [width, setWidth] = createSignal(100)
      const [list] = createSignal<VirtualItem[]>([{ id: 'a', hasSpanLines: false }])
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: () => width(),
      })
      expect(virt.heightOfIndex(0)).toBe(100)
      expect(virt.totalHeight()).toBe(100)
      setWidth(400)
      expect(virt.heightOfIndex(0)).toBe(400)
      expect(virt.totalHeight()).toBe(400)
      dispose()
    })
  })

  it('fires onFirstMeasure once on first measurement, not on re-measure', () => {
    createRoot((dispose) => {
      const items: VirtualItem[] = [{ id: 'a', hasSpanLines: false }]
      const [list] = createSignal(items)
      const calls: Array<[string, number]> = []
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        onFirstMeasure: (id, h) => calls.push([id, h]),
      })
      virt.measure('a', 200)
      expect(calls).toEqual([['a', 200]]) // first measurement reported
      virt.measure('a', 260) // re-measure (async growth) — carries no fresh estimate
      expect(calls).toEqual([['a', 200]]) // not reported again
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
      // Row 'a' is UNDER-estimated: estimate 80, but it will measure 600.
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: item => (item.id === 'a' ? 80 : 100),
      })
      // Anchor at the MIDDLE of the 80px estimate (scrollTop 40 -> fraction 0.5).
      const anchor = virt.anchorAt(40)
      expect(anchor).toEqual({ id: 'a', offsetWithinRow: 40, basisHeight: 80, gapFraction: 0 })
      // Before measure, resolve returns the captured 40 (basis == current estimate).
      expect(virt.scrollTopForAnchor(anchor!)).toBe(40)
      // Row 'a' measures 600. The 0.5 fraction maps to 0.5 * 600 = 300 -- the pin
      // tracks the row's growth instead of staying at a stale 40px.
      virt.measure('a', 600)
      expect(virt.scrollTopForAnchor(anchor!)).toBe(300)
      dispose()
    })
  })

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
      // Row 'a' is estimated tall (400) before it mounts; estimates bias UP.
      const items: VirtualItem[] = [
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
      ]
      const [list] = createSignal(items)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: item => (item.id === 'a' ? 400 : 100),
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

  it('resolves PROPORTIONALLY when an UNMEASURED row\'s estimate shrinks before it mounts (S1: no truncation)', () => {
    createRoot((dispose) => {
      const [list] = createSignal<VirtualItem[]>([
        { id: 'a', hasSpanLines: false },
        { id: 'b', hasSpanLines: false },
      ])
      const [estH, setEstH] = createSignal(400)
      const virt = useChatVirtualizer({
        items: list,
        overscanPx: 0,
        estimateHeight: 100,
        gapSmallPx: 10,
        gapLargePx: 20,
        estimate: item => (item.id === 'a' ? estH() : 100),
        estimateEpoch: estH, // a change re-runs the estimator for unmeasured rows
      })
      // Anchor at 90% into the 400px estimate, still unmeasured.
      const anchor = virt.anchorAt(360)
      expect(anchor).toEqual({ id: 'a', offsetWithinRow: 360, basisHeight: 400, gapFraction: 0 })
      expect(virt.scrollTopForAnchor(anchor!)).toBe(360)

      // The estimate SHRINKS 400 -> 200 (a width / prefs re-estimate) while the row is
      // STILL unmeasured. The old absolute clamp truncated the 360 offset to the new
      // 200 -- yanking the pin to the bottom; proportional keeps 0.9 -> 0.9*200 = 180.
      setEstH(200)
      expect(virt.scrollTopForAnchor(anchor!)).toBe(180)
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
      // 'outlier' is evicted and its 500px no longer skews the running mean.
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

  describe('heightDebugOfId (raw-JSON debug surface)', () => {
    function withEstimate(estimate: (item: VirtualItem) => number, items: VirtualItem[]) {
      const [list] = createSignal(items)
      return useChatVirtualizer({ items: list, overscanPx: 0, estimateHeight: 100, gapSmallPx: 10, gapLargePx: 20, estimate })
    }

    it('returns the analytical estimate with measured undefined before the row is measured', () => {
      createRoot((dispose) => {
        const virt = withEstimate(() => 500, [{ id: 'a', hasSpanLines: false }])
        expect(virt.heightDebugOfId('a')).toEqual({ estimated: 500, measured: undefined })
        dispose()
      })
    })

    it('keeps BOTH the estimate and the measured height once measured (heightOfIndex collapses them)', () => {
      createRoot((dispose) => {
        const virt = withEstimate(() => 500, [{ id: 'a', hasSpanLines: false }])
        virt.measure('a', 333)
        // heightOfIndex resolves to the measured value; heightDebugOfId surfaces both.
        expect(virt.heightOfIndex(0)).toBe(333)
        expect(virt.heightDebugOfId('a')).toEqual({ estimated: 500, measured: 333 })
        dispose()
      })
    })

    it('reports estimated undefined when the estimator throws or returns an unusable value, but still surfaces a measurement', () => {
      createRoot((dispose) => {
        const virt = withEstimate(
          item => (item.id === 'thrower' ? (() => { throw new Error('malformed') })() : Number.NaN),
          [{ id: 'thrower', hasSpanLines: false }, { id: 'nan', hasSpanLines: false }],
        )
        expect(virt.heightDebugOfId('thrower').estimated).toBeUndefined()
        expect(virt.heightDebugOfId('nan').estimated).toBeUndefined()
        virt.measure('nan', 210)
        expect(virt.heightDebugOfId('nan')).toEqual({ estimated: undefined, measured: 210 })
        dispose()
      })
    })

    it('swallows a throwing estimateBreakdown after the estimate succeeded (no escape to the debug surface)', () => {
      createRoot((dispose) => {
        // runEstimate succeeds (est !== null), so the breakdown branch runs. estimateBreakdown
        // RE-EVALUATES features(); if that second eval throws (payload/state changed between the
        // two calls), heightDebugOfId must swallow it like runEstimate does -- not let it escape
        // into the raw-JSON debug/copy surface. The estimate still surfaces; breakdown is undefined.
        const [list] = createSignal<VirtualItem[]>([{ id: 'a', hasSpanLines: false }])
        const virt = useChatVirtualizer({
          items: list,
          overscanPx: 0,
          estimateHeight: 100,
          gapSmallPx: 10,
          gapLargePx: 20,
          estimate: () => 500,
          estimateBreakdown: () => { throw new Error('features re-eval threw') },
        })
        // Calling heightDebugOfId would error the test if the breakdown throw escaped;
        // the guard swallows it, so the call returns with the estimate and no breakdown.
        const info = virt.heightDebugOfId('a')
        expect(info.estimated).toBe(500)
        expect(info.breakdown).toBeUndefined()
        dispose()
      })
    })

    it('returns both fields undefined for an unknown id', () => {
      createRoot((dispose) => {
        const { virt } = setup(plainItems(2))
        const info = virt.heightDebugOfId('nope')
        expect(info.estimated).toBeUndefined()
        expect(info.measured).toBeUndefined()
        dispose()
      })
    })

    it('reports estimated undefined when no per-item estimator is wired, yet still surfaces a measurement', () => {
      createRoot((dispose) => {
        // setup() injects no `estimate`, so runEstimate has nothing to call and the
        // analytical estimate is unavailable -- but a measured row still reports.
        const { virt } = setup(plainItems(2)) // ids m1, m2
        expect(virt.heightDebugOfId('m1')).toEqual({ estimated: undefined, measured: undefined })
        virt.measure('m1', 250)
        expect(virt.heightDebugOfId('m1')).toEqual({ estimated: undefined, measured: 250 })
        dispose()
      })
    })

    it('surfaces the estimate breakdown from estimateBreakdown when wired', () => {
      createRoot((dispose) => {
        const [list] = createSignal<VirtualItem[]>([{ id: 'a', hasSpanLines: false }])
        const breakdown = { kind: 'x', total: 500, terms: [], metrics: {} }
        const virt = useChatVirtualizer({
          items: list,
          overscanPx: 0,
          estimateHeight: 100,
          gapSmallPx: 10,
          gapLargePx: 20,
          estimate: () => 500,
          estimateBreakdown: () => breakdown,
        })
        expect(virt.heightDebugOfId('a')).toEqual({ estimated: 500, measured: undefined, breakdown })
        dispose()
      })
    })

    it('omits the breakdown when no estimateBreakdown is wired', () => {
      createRoot((dispose) => {
        const virt = withEstimate(() => 500, [{ id: 'a', hasSpanLines: false }])
        expect(virt.heightDebugOfId('a').breakdown).toBeUndefined()
        dispose()
      })
    })
  })
})

describe('samevirtualitems geometry equality', () => {
  const base: VirtualItem = { id: 'm1', hasSpanLines: false, estimateKey: 'k1', seq: 1n, features: () => ({ kind: 'prose', hasSpanLines: false }) }

  it('is true for arrays equal on the geometry fields', () => {
    expect(sameVirtualItems([{ ...base }], [{ ...base }])).toBe(true)
  })

  it('ignores non-geometry fields (seq / features) when they differ', () => {
    // seq is an anchor label and features is a lazy estimate thunk -- neither feeds the
    // offset map, so a row that differs ONLY in those must still compare equal (else the
    // memo would needlessly rebuild geometry on every streaming delta).
    const other: VirtualItem = { ...base, seq: 999n, features: () => ({ kind: 'tool_result', hasSpanLines: false }) }
    expect(sameVirtualItems([base], [other])).toBe(true)
  })

  it('is false when any geometry field differs', () => {
    expect(sameVirtualItems([base], [{ ...base, id: 'm2' }])).toBe(false)
    expect(sameVirtualItems([base], [{ ...base, hasSpanLines: true }])).toBe(false)
    expect(sameVirtualItems([base], [{ ...base, estimateKey: 'k2' }])).toBe(false)
  })

  it('is false for different lengths and true for the same reference', () => {
    expect(sameVirtualItems([base], [base, base])).toBe(false)
    const arr = [base]
    expect(sameVirtualItems(arr, arr)).toBe(true)
  })
})
