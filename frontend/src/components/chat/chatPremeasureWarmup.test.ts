import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatVirtualizerRange, VirtualItem } from './useChatVirtualizer'
import { createComputed, createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createPremeasureWarmup,
  WARMUP_BATCH_ROWS,
  WARMUP_IDLE_DELAY_MS,
  WARMUP_TICK_MS,
} from './chatPremeasureWarmup'

describe('chatPremeasureWarmup', () => {
  /**
   * The idle time that the fake `requestIdleCallback` grants. Each call of
   * `timeRemaining` in one idle period returns the next value, and the last value
   * once the list runs out.
   */
  const idle = {
    remaining: [50],
  }
  /** How long the fake idle callback takes to arrive, as the `requestIdle` fallback's timer does. */
  const IDLE_LATENCY_MS = 1
  /** Time from the arming effect, or from a tick, to the tick that it schedules. */
  const FIRST_TICK_MS = WARMUP_IDLE_DELAY_MS + IDLE_LATENCY_MS
  const NEXT_TICK_MS = WARMUP_TICK_MS + IDLE_LATENCY_MS

  beforeEach(() => {
    vi.useFakeTimers()
    idle.remaining = [50]
    vi.stubGlobal('requestIdleCallback', (callback: IdleRequestCallback) => setTimeout(() => {
      const values = [...idle.remaining]
      callback({
        didTimeout: false,
        timeRemaining: () => (values.length > 1
          ? values.shift()!
          : values[0]!),
      })
    }, IDLE_LATENCY_MS))
    vi.stubGlobal('cancelIdleCallback', (handle: ReturnType<typeof setTimeout>) => clearTimeout(handle))
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  function makeHarness(opts: {
    count: number
    range: ChatVirtualizerRange
    excludedBandRows?: number
    enabled?: boolean
  }) {
    const ids = Array.from({ length: opts.count }, (_, i) => `m${i}`)
    const entries = ids.map(id => ({
      message: { id },
    } as ClassifiedEntry))
    const items = ids.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` } as VirtualItem))
    const measured = new Set<string>()
    const [enabled, setEnabled] = createSignal(opts.enabled ?? true)
    const [entriesSig, setEntriesSig] = createSignal<readonly ClassifiedEntry[]>(entries)
    const [itemsSig, setItemsSig] = createSignal<readonly VirtualItem[]>(items)
    const [range, setRange] = createSignal(opts.range)
    let warmup!: ReturnType<typeof createPremeasureWarmup>
    let dispose!: () => void
    // The length of each batch that the band published, in order.
    const published: number[] = []
    createRoot((d) => {
      dispose = d
      warmup = createPremeasureWarmup({
        enabled,
        visibleEntries: entriesSig,
        virtualItems: itemsSig,
        range,
        hasMeasuredHeight: id => measured.has(id),
        excludedBandRows: opts.excludedBandRows ?? 2,
      })
      createComputed(() => published.push(warmup.warmupCandidates().length))
    })
    const currentIds = () => warmup.warmupCandidates().map(c => c.item.id)
    const measureBatch = () => {
      for (const c of warmup.warmupCandidates())
        measured.add(c.item.id)
    }
    return {
      warmup,
      published,
      currentIds,
      measureBatch,
      measured,
      setEnabled,
      setRange,
      setEntriesSig,
      setItemsSig,
      dispose,
    }
  }

  it('emits nothing until the idle delay, then the nearest rows outside the covered band', () => {
    // 20 rows, range [8, 10), band half-width 2 -> covered [6, 12).
    const h = makeHarness({
      count: 20,
      range: { start: 8, end: 10 },
    })
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(FIRST_TICK_MS - 1)
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(1)
    // Alternating outward from the band edges (above 5,4,3,2 / below 12,13,14,15),
    // capped at the batch size.
    expect(h.currentIds()).toEqual([
      'm5',
      'm12',
      'm4',
      'm13',
      'm3',
      'm14',
      'm2',
      'm15',
    ])
    expect(h.currentIds()).toHaveLength(WARMUP_BATCH_ROWS)
    h.dispose()
  })

  it('skips measured rows and drains outward over ticks until the window is dry', () => {
    const h = makeHarness({
      count: 12,
      range: { start: 4, end: 6 },
      excludedBandRows: 2,
    })
    // Covered band [2, 8); outside rows: 1,0 above and 8..11 below = 6 rows.
    vi.advanceTimersByTime(FIRST_TICK_MS)
    expect(h.currentIds()).toEqual(['m1', 'm8', 'm0', 'm9', 'm10', 'm11'])
    h.measureBatch()
    vi.advanceTimersByTime(NEXT_TICK_MS)
    // Everything measured: the band clears and the ticker parks.
    expect(h.currentIds()).toEqual([])
    const timersBefore = vi.getTimerCount()
    vi.advanceTimersByTime(10 * WARMUP_TICK_MS)
    expect(vi.getTimerCount()).toBe(timersBefore)
    h.dispose()
  })

  it('clears the band immediately when disabled and resumes after the idle delay', () => {
    const h = makeHarness({
      count: 10,
      range: { start: 0, end: 2 },
      excludedBandRows: 1,
    })
    vi.advanceTimersByTime(FIRST_TICK_MS)
    expect(h.currentIds()).not.toEqual([])

    h.setEnabled(false) // a scroll started
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(10 * WARMUP_TICK_MS)
    expect(h.currentIds()).toEqual([])

    h.setEnabled(true)
    vi.advanceTimersByTime(FIRST_TICK_MS - 1)
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(1)
    expect(h.currentIds()).not.toEqual([])
    h.dispose()
  })

  it('re-arms a parked ticker when new rows arrive', () => {
    const h = makeHarness({
      count: 4,
      range: { start: 0, end: 4 },
      excludedBandRows: 2,
    })
    vi.advanceTimersByTime(FIRST_TICK_MS + NEXT_TICK_MS)
    expect(h.currentIds()).toEqual([]) // whole window covered by the band

    const moreIds = Array.from({ length: 12 }, (_, i) => `m${i}`)
    h.setEntriesSig(moreIds.map(id => ({
      message: { id },
    } as ClassifiedEntry)))
    h.setItemsSig(moreIds.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` } as VirtualItem)))
    vi.advanceTimersByTime(FIRST_TICK_MS)
    expect(h.currentIds()).toEqual(['m6', 'm7', 'm8', 'm9', 'm10', 'm11'])
    h.dispose()
  })

  it('emits nothing for an empty window and parks the ticker', () => {
    const h = makeHarness({
      count: 0,
      range: { start: 0, end: 0 },
    })
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS + 10 * WARMUP_TICK_MS)
    expect(h.currentIds()).toEqual([])
    expect(vi.getTimerCount()).toBe(0)
    h.dispose()
  })

  it('stops all timers on dispose', () => {
    const h = makeHarness({
      count: 20,
      range: { start: 8, end: 10 },
    })
    vi.advanceTimersByTime(FIRST_TICK_MS)
    expect(h.currentIds()).not.toEqual([])
    h.dispose()
    expect(vi.getTimerCount()).toBe(0)
  })

  describe('idle deadline', () => {
    it('mounts rows one at a time, and stops when the idle period runs out', () => {
      idle.remaining = [3, 0]
      const h = makeHarness({
        count: 20,
        range: { start: 8, end: 10 },
      })
      vi.advanceTimersByTime(FIRST_TICK_MS)

      expect(h.currentIds()).toEqual(['m5', 'm12'])
      // One row per step, so the deadline check after a row saw its cost.
      expect(h.published).toEqual([0, 1, 2])
      h.dispose()
    })

    it('takes one row from an idle period that has already run out', () => {
      idle.remaining = [0]
      const h = makeHarness({
        count: 20,
        range: { start: 8, end: 10 },
      })
      vi.advanceTimersByTime(FIRST_TICK_MS)

      expect(h.currentIds()).toEqual(['m5'])
      h.dispose()
    })

    it('keeps a row that waits for its measure mounted, and adds new rows after it', () => {
      idle.remaining = [3, 0]
      const h = makeHarness({
        count: 20,
        range: { start: 8, end: 10 },
      })
      vi.advanceTimersByTime(FIRST_TICK_MS)
      const waiting = h.warmup.warmupCandidates().find(c => c.item.id === 'm12')
      expect(waiting).toBeDefined()
      h.measured.add('m5')

      idle.remaining = [50]
      vi.advanceTimersByTime(NEXT_TICK_MS)

      expect(h.currentIds()).toEqual([
        'm12',
        'm4',
        'm13',
        'm3',
        'm14',
        'm2',
        'm15',
        'm1',
      ])
      // The same candidate object, so the hidden row stays mounted.
      expect(h.warmup.warmupCandidates()[0]).toBe(waiting)
      h.dispose()
    })

    it('waits for the next idle period after the tick delay', () => {
      const h = makeHarness({
        count: 20,
        range: { start: 8, end: 10 },
      })
      vi.advanceTimersByTime(FIRST_TICK_MS)
      h.measureBatch()
      const before = h.currentIds()

      // The tick delay passes first, then the idle period arrives.
      vi.advanceTimersByTime(WARMUP_TICK_MS)
      expect(h.currentIds()).toEqual(before)
      vi.advanceTimersByTime(IDLE_LATENCY_MS)
      expect(h.currentIds()).not.toEqual(before)
      h.dispose()
    })
  })
})
