import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRafResizeObserver } from './resizeObserver'

class FakeResizeObserver {
  static last: FakeResizeObserver | undefined

  readonly callback: ResizeObserverCallback
  readonly observed = new Set<Element>()
  disconnected = false

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
    FakeResizeObserver.last = this
  }

  observe(target: Element): void {
    this.observed.add(target)
  }

  unobserve(target: Element): void {
    this.observed.delete(target)
  }

  disconnect(): void {
    this.disconnected = true
    this.observed.clear()
  }

  emit(entries: ResizeObserverEntry[]): void {
    this.callback(entries, this as unknown as ResizeObserver)
  }
}

function entry(target: Element, width: number, height = 1): ResizeObserverEntry {
  return {
    target,
    contentRect: { width, height } as DOMRectReadOnly,
  } as ResizeObserverEntry
}

describe('createRafResizeObserver', () => {
  let frames: Array<FrameRequestCallback | undefined>
  let nextFrameId: number
  let realResizeObserver: typeof ResizeObserver | undefined

  const runFrame = (id: number): void => {
    const frame = frames[id]
    frames[id] = undefined
    frame?.(performance.now())
  }

  beforeEach(() => {
    frames = []
    nextFrameId = 1
    realResizeObserver = globalThis.ResizeObserver
    FakeResizeObserver.last = undefined
    globalThis.ResizeObserver = FakeResizeObserver as unknown as typeof ResizeObserver
    vi.stubGlobal('requestAnimationFrame', vi.fn((cb: FrameRequestCallback) => {
      const id = nextFrameId++
      frames[id] = cb
      return id
    }))
    vi.stubGlobal('cancelAnimationFrame', vi.fn((id: number) => {
      frames[id] = undefined
    }))
  })

  afterEach(() => {
    if (realResizeObserver)
      globalThis.ResizeObserver = realResizeObserver
    else
      Reflect.deleteProperty(globalThis, 'ResizeObserver')
    vi.unstubAllGlobals()
  })

  it('coalesces resize notifications into one frame with the latest entry per target', () => {
    const targetA = document.createElement('div')
    const targetB = document.createElement('div')
    const calls: ResizeObserverEntry[][] = []
    const observer = createRafResizeObserver(entries => calls.push(entries))

    observer!.observe(targetA)
    observer!.observe(targetB)
    FakeResizeObserver.last!.emit([entry(targetA, 100)])
    FakeResizeObserver.last!.emit([entry(targetB, 200), entry(targetA, 150)])

    expect(requestAnimationFrame).toHaveBeenCalledTimes(1)
    expect(calls).toEqual([])

    runFrame(1)

    expect(calls).toHaveLength(1)
    expect(calls[0].map(e => [e.target, e.contentRect.width])).toEqual([
      [targetA, 150],
      [targetB, 200],
    ])
  })

  it('drops pending entries for an unobserved target before the frame flushes', () => {
    const targetA = document.createElement('div')
    const targetB = document.createElement('div')
    const calls: ResizeObserverEntry[][] = []
    const observer = createRafResizeObserver(entries => calls.push(entries))

    observer!.observe(targetA)
    observer!.observe(targetB)
    FakeResizeObserver.last!.emit([entry(targetA, 100), entry(targetB, 200)])
    observer!.unobserve(targetA)

    runFrame(1)

    expect(calls).toHaveLength(1)
    expect(calls[0].map(e => e.target)).toEqual([targetB])
  })

  it('cancels queued work on disconnect', () => {
    const target = document.createElement('div')
    const callback = vi.fn()
    const observer = createRafResizeObserver(callback)

    observer!.observe(target)
    FakeResizeObserver.last!.emit([entry(target, 100)])
    observer!.disconnect()

    expect(cancelAnimationFrame).toHaveBeenCalledWith(1)
    runFrame(1)
    expect(callback).not.toHaveBeenCalled()
    expect(FakeResizeObserver.last!.disconnected).toBe(true)
  })

  it('does not retain a stale frame handle when requestAnimationFrame runs synchronously', () => {
    vi.stubGlobal('requestAnimationFrame', vi.fn((cb: FrameRequestCallback) => {
      cb(performance.now())
      return 123
    }))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    const target = document.createElement('div')
    const calls: ResizeObserverEntry[][] = []
    const observer = createRafResizeObserver(entries => calls.push(entries))

    observer!.observe(target)
    FakeResizeObserver.last!.emit([entry(target, 100)])

    expect(calls).toHaveLength(1)
    expect(calls[0][0].target).toBe(target)

    observer!.disconnect()

    expect(cancelAnimationFrame).not.toHaveBeenCalled()
  })

  it('uses a safe setTimeout timestamp when requestAnimationFrame and performance are unavailable', () => {
    vi.useFakeTimers()
    vi.stubGlobal('requestAnimationFrame', undefined)
    vi.stubGlobal('cancelAnimationFrame', undefined)
    vi.stubGlobal('performance', undefined)
    try {
      const target = document.createElement('div')
      const calls: ResizeObserverEntry[][] = []
      const observer = createRafResizeObserver(entries => calls.push(entries))

      observer!.observe(target)
      FakeResizeObserver.last!.emit([entry(target, 100)])

      expect(() => vi.runOnlyPendingTimers()).not.toThrow()
      expect(calls).toHaveLength(1)
      expect(calls[0][0].target).toBe(target)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('is unavailable when the platform has no ResizeObserver', () => {
    Reflect.deleteProperty(globalThis, 'ResizeObserver')

    expect(createRafResizeObserver(() => {})).toBeUndefined()
  })
})
