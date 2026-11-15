import { describe, expect, it } from 'vitest'
import { bucketWidth, computeOverscanPx, createViewportSizeObserver, measureSpaceToken } from './chatViewportGeometry'

describe('chatViewportGeometry bucketWidth', () => {
  it('rounds a measured width to the nearest 8px bucket', () => {
    expect(bucketWidth(0)).toBe(0)
    expect(bucketWidth(8)).toBe(8)
    expect(bucketWidth(11)).toBe(8) // 11/8 = 1.375 -> rounds to 1 -> 8
    expect(bucketWidth(12)).toBe(16) // 12/8 = 1.5 -> rounds to 2 -> 16
    expect(bucketWidth(801)).toBe(800) // sub-bucket jitter collapses to the same bucket
    expect(bucketWidth(804)).toBe(808)
  })
})

describe('chatViewportGeometry computeOverscanPx', () => {
  it('floors short panes, scales mid panes, and caps tall ones', () => {
    expect(computeOverscanPx(0)).toBe(800) // pre-measurement frame -> floor
    expect(computeOverscanPx(-10)).toBe(800) // defensive -> floor
    expect(computeOverscanPx(400)).toBe(800) // 400*1.5=600 < floor
    expect(computeOverscanPx(1000)).toBe(1500) // 1000*1.5
    expect(computeOverscanPx(2200)).toBe(2400) // 2200*1.5=3300 -> capped
  })
})

describe('chatViewportGeometry measureSpaceToken', () => {
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

describe('chatViewportGeometry createViewportSizeObserver', () => {
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
      this.cb([
        {
          contentRect: { width, height },
        } as ResizeObserverEntry,
      ], this as unknown as ResizeObserver)
    }
  }

  it('reports the exact width and the rounded height, deduped per axis, deferred to a microtask', async () => {
    let instance: FakeResizeObserver | undefined
    const real = globalThis.ResizeObserver
    globalThis.ResizeObserver = function (cb: ResizeObserverCallback) {
      instance = new FakeResizeObserver(cb)
      return instance
    } as unknown as typeof ResizeObserver
    try {
      const widths: number[] = []
      const heights: number[] = []
      const obs = createViewportSizeObserver({
        onWidth: w => widths.push(w),
        onHeight: h => heights.push(h),
      })
      const el = {} as HTMLElement
      obs.observe(el)
      expect(instance!.observed).toContain(el)

      // The width is the width of each row, so it arrives as the browser lays it out:
      // 803 is not rounded to its 8px step, 800. 599.6 rounds to 600.
      instance!.emit(803, 599.6)
      await Promise.resolve()
      expect(widths).toEqual([803])
      expect(heights).toEqual([600])

      // A measure that changes neither value fires nothing.
      instance!.emit(803, 600.2)
      await Promise.resolve()
      expect(widths).toEqual([803])
      expect(heights).toEqual([600])

      // A change of one axis fires ONLY that axis, also a change inside one 8px step and
      // a change of a fraction of a pixel.
      instance!.emit(801, 600.4)
      await Promise.resolve()
      expect(widths).toEqual([803, 801])
      expect(heights).toEqual([600])
      instance!.emit(801.5, 600.4)
      await Promise.resolve()
      expect(widths).toEqual([803, 801, 801.5])
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
      const obs = createViewportSizeObserver({
        onWidth: w => widths.push(w),
        onHeight: h => heights.push(h),
      })
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

  it('reports a width and a height of 0 for an entry with no content rectangle, once', async () => {
    let callback: ResizeObserverCallback | undefined
    const real = globalThis.ResizeObserver
    globalThis.ResizeObserver = function (cb: ResizeObserverCallback) {
      callback = cb
      return new FakeResizeObserver(cb)
    } as unknown as typeof ResizeObserver
    try {
      const widths: number[] = []
      const heights: number[] = []
      const obs = createViewportSizeObserver({
        onWidth: w => widths.push(w),
        onHeight: h => heights.push(h),
      })
      obs.observe({} as HTMLElement)
      const emitEmptyEntry = () => callback!([
        {} as ResizeObserverEntry,
      ], {} as ResizeObserver)

      emitEmptyEntry()
      await Promise.resolve()
      expect(widths).toEqual([0])
      expect(heights).toEqual([0])

      // The second entry changes neither value. An empty list of entries reads as 0 too.
      emitEmptyEntry()
      callback!([], {} as ResizeObserver)
      await Promise.resolve()
      expect(widths).toEqual([0])
      expect(heights).toEqual([0])
      obs.disconnect()
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
      const obs = createViewportSizeObserver({
        onWidth: () => {},
        onHeight: () => {},
      })
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
