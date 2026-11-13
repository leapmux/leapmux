import type { Accessor } from 'solid-js'
import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatVirtualizerRange, VirtualItem } from './useChatVirtualizer'
import { createComputed, createMemo, createRoot, createSignal, mapArray, onCleanup } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPremeasureQueue } from './chatPremeasureQueue'
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

  /**
   * The warm-up band of 20 rows (range [8, 10), covered band [6, 12)) behind the
   * real premeasure queue. A keyed row list stands in for ChatHiddenPremeasure's
   * `<For>`: like the `<For>`, it mounts one row for each candidate object.
   */
  function makeQueuedHarness() {
    const ids = Array.from({ length: 20 }, (_, i) => `m${i}`)
    const entries = ids.map(id => ({
      message: { id },
    } as ClassifiedEntry))
    const items = ids.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` } as VirtualItem))
    const entryById = new Map(entries.map(entry => [entry.message.id, entry]))
    const itemById = new Map(items.map(item => [item.id, item]))
    const measured = new Set<string>()
    const mounts: string[] = []
    const disposes: string[] = []
    const [enabled, setEnabled] = createSignal(true)
    let warmup!: ReturnType<typeof createPremeasureWarmup>
    let queue!: ReturnType<typeof createPremeasureQueue>
    let rows!: Accessor<string[]>
    const dispose = createRoot((disposeRoot) => {
      warmup = createPremeasureWarmup({
        enabled,
        visibleEntries: () => entries,
        virtualItems: () => items,
        range: () => ({ start: 8, end: 10 }),
        hasMeasuredHeight: id => measured.has(id),
        excludedBandRows: 2,
      })
      queue = createPremeasureQueue({
        virt: {
          hasMeasuredHeight: id => measured.has(id),
          hasPendingPremeasuredHeight: () => false,
          primeHeight: (id) => {
            measured.add(id)
            return true
          },
        },
        visibleEntryById: () => entryById,
        virtualItemById: () => itemById,
        virtualItems: () => items,
        rangedCandidates: () => [],
        lookAheadCandidates: () => [],
        warmupCandidates: warmup.warmupCandidates,
      })
      rows = createMemo(mapArray(queue.premeasureCandidates, (candidate) => {
        mounts.push(candidate.item.id)
        onCleanup(() => disposes.push(candidate.item.id))
        return candidate.item.id
      }))
      return disposeRoot
    })
    return {
      mounts,
      disposes,
      setEnabled,
      mountedIds: () => rows(),
      warmupIds: () => warmup.warmupCandidates().map(c => c.item.id),
      measure: (id: string) => {
        queue.onMeasure(id, 10, `k-${id}`, 0, true)
      },
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

  it('keeps one scheduled tick when new rows arrive before the tick runs', () => {
    const h = makeHarness({
      count: 20,
      range: { start: 8, end: 10 },
    })
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS - 100)
    expect(vi.getTimerCount()).toBe(1)

    // The arming effect re-fires, and its new delay replaces the old one.
    h.setItemsSig(prev => [...prev])
    expect(vi.getTimerCount()).toBe(1)
    vi.advanceTimersByTime(FIRST_TICK_MS - 1)
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(1)
    expect(h.currentIds()).toHaveLength(WARMUP_BATCH_ROWS)
    expect(vi.getTimerCount()).toBe(1)
    h.dispose()
  })

  // A scheduled tick waits in two stages: its delay timer, then its request for an
  // idle period. The second stage must be cancellable too.
  describe('a tick that waits for its idle period', () => {
    it('is cancelled when the band is disabled', () => {
      const h = makeHarness({
        count: 20,
        range: { start: 8, end: 10 },
      })
      vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS)
      // The delay ran out: only the idle request waits now.
      expect(vi.getTimerCount()).toBe(1)

      h.setEnabled(false)

      expect(vi.getTimerCount()).toBe(0)
      vi.advanceTimersByTime(IDLE_LATENCY_MS + WARMUP_TICK_MS)
      expect(h.currentIds()).toEqual([])
      h.dispose()
    })

    it('is cancelled on dispose', () => {
      const h = makeHarness({
        count: 20,
        range: { start: 8, end: 10 },
      })
      vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS)
      expect(vi.getTimerCount()).toBe(1)

      h.dispose()

      expect(vi.getTimerCount()).toBe(0)
    })

    it('gives way to the tick that new rows schedule, so one tick waits', () => {
      const h = makeHarness({
        count: 20,
        range: { start: 8, end: 10 },
      })
      vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS)

      h.setItemsSig(prev => [...prev])

      expect(vi.getTimerCount()).toBe(1)
      // The new tick waits for the whole delay again.
      vi.advanceTimersByTime(FIRST_TICK_MS - 1)
      expect(h.currentIds()).toEqual([])
      vi.advanceTimersByTime(1)
      expect(h.currentIds()).toHaveLength(WARMUP_BATCH_ROWS)
      expect(vi.getTimerCount()).toBe(1)
      h.dispose()
    })
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

    it('mounts each row of a tick once, behind the premeasure queue', () => {
      // A tick publishes the band once per row, so that the deadline check sees each
      // row's mount. Each publish must mount only the row that it adds.
      const q = makeQueuedHarness()
      vi.advanceTimersByTime(FIRST_TICK_MS)

      expect(q.mountedIds()).toEqual([
        'm2',
        'm3',
        'm4',
        'm5',
        'm12',
        'm13',
        'm14',
        'm15',
      ])
      expect(q.mounts).toEqual([
        'm5',
        'm12',
        'm4',
        'm13',
        'm3',
        'm14',
        'm2',
        'm15',
      ])
      expect(q.disposes).toEqual([])
      q.dispose()
    })

    it('keeps a row that waits for its measure mounted while later ticks add rows', () => {
      idle.remaining = [3, 0]
      const q = makeQueuedHarness()
      vi.advanceTimersByTime(FIRST_TICK_MS)
      expect(q.mounts).toEqual(['m5', 'm12'])

      // m5 measures; m12 still waits for its measure.
      q.measure('m5')
      expect(q.disposes).toEqual(['m5'])
      idle.remaining = [50]
      vi.advanceTimersByTime(NEXT_TICK_MS)

      expect(q.mountedIds()).toEqual([
        'm1',
        'm2',
        'm3',
        'm4',
        'm12',
        'm13',
        'm14',
        'm15',
      ])
      // Only the new rows mount; m12 stays mounted.
      expect(q.mounts).toEqual([
        'm5',
        'm12',
        'm4',
        'm13',
        'm3',
        'm14',
        'm2',
        'm15',
        'm1',
      ])
      expect(q.disposes).toEqual(['m5'])
      q.dispose()
    })

    it('leaves a row mounted until it measures when the band empties', () => {
      // Membership in the queue is sticky: an empty band adds nothing, and the
      // queue drops a row only once it measures or leaves the window.
      const q = makeQueuedHarness()
      vi.advanceTimersByTime(FIRST_TICK_MS)
      q.setEnabled(false)

      expect(q.warmupIds()).toEqual([])
      expect(q.mountedIds()).toHaveLength(WARMUP_BATCH_ROWS)
      expect(q.disposes).toEqual([])
      q.measure('m5')
      expect(q.disposes).toEqual(['m5'])
      q.dispose()
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
