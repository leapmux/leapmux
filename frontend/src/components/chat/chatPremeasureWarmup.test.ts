import type { ClassifiedEntry } from './chatEntryCache'
import type { ChatVirtualizerRange, VirtualItem } from './useChatVirtualizer'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createPremeasureWarmup,
  WARMUP_BATCH_ROWS,
  WARMUP_IDLE_DELAY_MS,
  WARMUP_TICK_MS,
} from './chatPremeasureWarmup'

describe('chatpremeasurewarmup', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  function makeHarness(opts: {
    count: number
    range: ChatVirtualizerRange
    excludedBandRows?: number
    enabled?: boolean
  }) {
    const ids = Array.from({ length: opts.count }, (_, i) => `m${i}`)
    const entries = ids.map(id => ({ msg: { id } } as ClassifiedEntry))
    const items = ids.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` } as VirtualItem))
    const measured = new Set<string>()
    const [enabled, setEnabled] = createSignal(opts.enabled ?? true)
    const [entriesSig, setEntriesSig] = createSignal<readonly ClassifiedEntry[]>(entries)
    const [itemsSig, setItemsSig] = createSignal<readonly VirtualItem[]>(items)
    const [range, setRange] = createSignal(opts.range)
    let warmup!: ReturnType<typeof createPremeasureWarmup>
    let dispose!: () => void
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
    })
    const currentIds = () => warmup.warmupCandidates().map(c => c.item.id)
    const measureBatch = () => {
      for (const c of warmup.warmupCandidates())
        measured.add(c.item.id)
    }
    return { warmup, currentIds, measureBatch, measured, setEnabled, setRange, setEntriesSig, setItemsSig, dispose }
  }

  it('emits nothing until the idle delay, then the nearest rows outside the covered band', () => {
    // 20 rows, range [8, 10), band half-width 2 -> covered [6, 12).
    const h = makeHarness({ count: 20, range: { start: 8, end: 10 } })
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS - 1)
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(1)
    // Alternating outward from the band edges (above 5,4,3,2 / below 12,13,14,15),
    // capped at the batch size.
    expect(h.currentIds()).toEqual(['m5', 'm12', 'm4', 'm13', 'm3', 'm14', 'm2', 'm15'])
    expect(h.currentIds()).toHaveLength(WARMUP_BATCH_ROWS)
    h.dispose()
  })

  it('skips measured rows and drains outward over ticks until the window is dry', () => {
    const h = makeHarness({ count: 12, range: { start: 4, end: 6 }, excludedBandRows: 2 })
    // Covered band [2, 8); outside rows: 1,0 above and 8..11 below = 6 rows.
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS)
    expect(h.currentIds()).toEqual(['m1', 'm8', 'm0', 'm9', 'm10', 'm11'])
    h.measureBatch()
    vi.advanceTimersByTime(WARMUP_TICK_MS)
    // Everything measured: the band clears and the ticker parks.
    expect(h.currentIds()).toEqual([])
    const timersBefore = vi.getTimerCount()
    vi.advanceTimersByTime(10 * WARMUP_TICK_MS)
    expect(vi.getTimerCount()).toBe(timersBefore)
    h.dispose()
  })

  it('clears the band immediately when disabled and resumes after the idle delay', () => {
    const h = makeHarness({ count: 10, range: { start: 0, end: 2 }, excludedBandRows: 1 })
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS)
    expect(h.currentIds()).not.toEqual([])

    h.setEnabled(false) // a scroll started
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(10 * WARMUP_TICK_MS)
    expect(h.currentIds()).toEqual([])

    h.setEnabled(true)
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS - 1)
    expect(h.currentIds()).toEqual([])
    vi.advanceTimersByTime(1)
    expect(h.currentIds()).not.toEqual([])
    h.dispose()
  })

  it('re-arms a parked ticker when new rows arrive', () => {
    const h = makeHarness({ count: 4, range: { start: 0, end: 4 }, excludedBandRows: 2 })
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS + WARMUP_TICK_MS)
    expect(h.currentIds()).toEqual([]) // whole window covered by the band

    const moreIds = Array.from({ length: 12 }, (_, i) => `m${i}`)
    h.setEntriesSig(moreIds.map(id => ({ msg: { id } } as ClassifiedEntry)))
    h.setItemsSig(moreIds.map(id => ({ id, hasSpanLines: false, heightKey: `k-${id}` } as VirtualItem)))
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS)
    expect(h.currentIds()).toEqual(['m6', 'm7', 'm8', 'm9', 'm10', 'm11'])
    h.dispose()
  })

  it('emits nothing for an empty window and parks the ticker', () => {
    const h = makeHarness({ count: 0, range: { start: 0, end: 0 } })
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS + 10 * WARMUP_TICK_MS)
    expect(h.currentIds()).toEqual([])
    expect(vi.getTimerCount()).toBe(0)
    h.dispose()
  })

  it('stops all timers on dispose', () => {
    const h = makeHarness({ count: 20, range: { start: 8, end: 10 } })
    vi.advanceTimersByTime(WARMUP_IDLE_DELAY_MS)
    expect(h.currentIds()).not.toEqual([])
    h.dispose()
    expect(vi.getTimerCount()).toBe(0)
  })
})
