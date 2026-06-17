import { describe, expect, it } from 'vitest'
import { bucketWidth, computeOverscanPx, createViewportSizeObserver, measureSpaceToken } from './chatViewportGeometry'

describe('chatviewportgeometry bucketWidth', () => {
  it('rounds a measured width to the nearest 8px bucket', () => {
    expect(bucketWidth(0)).toBe(0)
    expect(bucketWidth(8)).toBe(8)
    expect(bucketWidth(11)).toBe(8) // 11/8 = 1.375 -> rounds to 1 -> 8
    expect(bucketWidth(12)).toBe(16) // 12/8 = 1.5 -> rounds to 2 -> 16
    expect(bucketWidth(801)).toBe(800) // sub-bucket jitter collapses to the same bucket
    expect(bucketWidth(804)).toBe(808)
  })
})

describe('chatviewportgeometry computeOverscanPx', () => {
  it('floors short panes, scales mid panes, and caps tall ones', () => {
    expect(computeOverscanPx(0)).toBe(800) // pre-measurement frame -> floor
    expect(computeOverscanPx(-10)).toBe(800) // defensive -> floor
    expect(computeOverscanPx(400)).toBe(800) // 400*1.5=600 < floor
    expect(computeOverscanPx(1000)).toBe(1500) // 1000*1.5
    expect(computeOverscanPx(2200)).toBe(2400) // 2200*1.5=3300 -> capped
  })
})

describe('chatviewportgeometry measureSpaceToken', () => {
  it('falls back when the probe has no resolvable height (jsdom reports 0)', () => {
    // jsdom's getBoundingClientRect reports height 0 for the detached probe, so the
    // token can't be measured and the caller's fallback is returned.
    expect(measureSpaceToken('--space-2', 8)).toBe(8)
    expect(measureSpaceToken('--space-5', 20)).toBe(20)
  })

  it('returns the fallback when there is no document (SSR / non-DOM env)', () => {
    const realDocument = globalThis.document
    // @ts-expect-error -- temporarily remove document to exercise the guard.
    delete globalThis.document
    try {
      expect(measureSpaceToken('--space-2', 8)).toBe(8)
    }
    finally {
      globalThis.document = realDocument
    }
  })
})

describe('chatviewportgeometry createViewportSizeObserver', () => {
  /** A controllable ResizeObserver the test triggers via emit(). */
  class FakeResizeObserver {
    cb: ResizeObserverCallback
    observed: Element[] = []
    disconnected = false
    constructor(cb: ResizeObserverCallback) {
      this.cb = cb
    }

    observe(el: Element): void {
      this.observed.push(el)
    }

    disconnect(): void {
      this.disconnected = true
    }

    emit(width: number, height: number): void {
      this.cb([{ contentRect: { width, height } } as ResizeObserverEntry], this as unknown as ResizeObserver)
    }
  }

  it('reports bucketed width + rounded height, deduped per axis, deferred to a microtask', async () => {
    let instance: FakeResizeObserver | undefined
    const real = globalThis.ResizeObserver
    globalThis.ResizeObserver = function (cb: ResizeObserverCallback) {
      instance = new FakeResizeObserver(cb)
      return instance
    } as unknown as typeof ResizeObserver
    try {
      const widths: number[] = []
      const heights: number[] = []
      const obs = createViewportSizeObserver({ onWidth: w => widths.push(w), onHeight: h => heights.push(h) })
      const el = {} as HTMLElement
      obs.observe(el)
      expect(instance!.observed).toContain(el)

      instance!.emit(803, 599.6) // 803 -> bucket 800; 599.6 -> round 600
      await Promise.resolve()
      expect(widths).toEqual([800])
      expect(heights).toEqual([600])

      // A measure that leaves BOTH axes in the same bucket fires nothing.
      instance!.emit(801, 600.2)
      await Promise.resolve()
      expect(widths).toEqual([800])
      expect(heights).toEqual([600])

      // A real change to one axis fires ONLY that axis.
      instance!.emit(900, 600.4) // 900 -> bucket 904; 600.4 -> round 600 (unchanged)
      await Promise.resolve()
      expect(widths).toEqual([800, bucketWidth(900)])
      expect(heights).toEqual([600])

      obs.disconnect()
      expect(instance!.disconnected).toBe(true)
    }
    finally {
      globalThis.ResizeObserver = real
    }
  })

  it('does not fire a callback queued before disconnect (no write to a disposed owner)', async () => {
    let instance: FakeResizeObserver | undefined
    const real = globalThis.ResizeObserver
    globalThis.ResizeObserver = function (cb: ResizeObserverCallback) {
      instance = new FakeResizeObserver(cb)
      return instance
    } as unknown as typeof ResizeObserver
    try {
      const widths: number[] = []
      const heights: number[] = []
      const obs = createViewportSizeObserver({ onWidth: w => widths.push(w), onHeight: h => heights.push(h) })
      obs.observe({} as HTMLElement)
      // The observe callback runs synchronously and queues the writes as microtasks;
      // the consumer tears us down before they flush. The deferred writes must NOT land.
      instance!.emit(803, 600)
      obs.disconnect()
      await Promise.resolve()
      expect(widths).toEqual([])
      expect(heights).toEqual([])
    }
    finally {
      globalThis.ResizeObserver = real
    }
  })

  it('observe is a no-op when ResizeObserver is unavailable (jsdom/older Safari)', () => {
    const real = globalThis.ResizeObserver
    // @ts-expect-error -- remove RO to exercise the unavailable guard.
    delete globalThis.ResizeObserver
    try {
      const obs = createViewportSizeObserver({ onWidth: () => {}, onHeight: () => {} })
      expect(() => {
        obs.observe({} as HTMLElement)
        obs.disconnect()
      }).not.toThrow()
    }
    finally {
      globalThis.ResizeObserver = real
    }
  })
})
