import type { ScrollContext } from './useChatScroll'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createFlingSettle, FLING_SETTLE_MS } from './chatScrollFlingSettle'

function setup() {
  const el = { scrollTop: 100 } as HTMLDivElement
  let animating = false
  const writes: Array<{ top: number, source?: string }> = []
  const ctx = {
    getEl: () => el,
    isAnimating: () => animating,
    writeScrollTop: (top: number, source?: string) => {
      el.scrollTop = top
      writes.push({ top, source })
    },
    refreshViewport: vi.fn(),
  } as unknown as ScrollContext
  const captureAnchor = vi.fn()
  const settle = createFlingSettle(ctx, {
    isRepinning: () => false,
    getAnchor: () => ({ id: 'm1', offsetWithinRow: 0 }),
    captureAnchor,
  })
  return {
    el,
    writes,
    refreshViewport: ctx.refreshViewport as ReturnType<typeof vi.fn>,
    captureAnchor,
    settle,
    setAnimating: (next: boolean) => {
      animating = next
    },
  }
}

describe('createFlingSettle', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('resets the per-capture bank when a blocked settle drops drift', () => {
    vi.useFakeTimers()
    const { settle, setAnimating, writes, captureAnchor } = setup()

    settle.accumulate(50)
    setAnimating(true)
    settle.schedule()
    vi.advanceTimersByTime(FLING_SETTLE_MS)

    setAnimating(false)
    settle.accumulate(30)
    settle.schedule()
    vi.advanceTimersByTime(FLING_SETTLE_MS)

    expect(writes).toEqual([])
    expect(captureAnchor).toHaveBeenCalledTimes(1)
  })

  it('drops deferred drift at settle instead of writing a post-momentum snap', () => {
    vi.useFakeTimers()
    const { el, settle, writes, captureAnchor, refreshViewport } = setup()
    el.scrollTop = 3818

    settle.accumulate(-171)
    settle.schedule()
    vi.advanceTimersByTime(FLING_SETTLE_MS)

    expect(el.scrollTop).toBe(3818)
    expect(writes).toEqual([])
    expect(captureAnchor).toHaveBeenCalledTimes(1)
    expect(refreshViewport).not.toHaveBeenCalled()
  })

  it('waits for a quiet window before accepting deferred drift', () => {
    vi.useFakeTimers()
    const { settle, captureAnchor } = setup()

    settle.accumulate(100)
    settle.schedule()
    vi.advanceTimersByTime(FLING_SETTLE_MS - 50)

    settle.schedule()
    vi.advanceTimersByTime(FLING_SETTLE_MS - 50)
    expect(captureAnchor).not.toHaveBeenCalled()

    vi.advanceTimersByTime(50)
    expect(captureAnchor).toHaveBeenCalledTimes(1)
  })

  it('captures the accepted viewport before releasing deferred geometry work', () => {
    vi.useFakeTimers()
    const el = { scrollTop: 100 } as HTMLDivElement
    const calls: string[] = []
    const ctx = {
      getEl: () => el,
      isAnimating: () => false,
      writeScrollTop: () => {},
      refreshViewport: () => {},
    } as unknown as ScrollContext
    const settle = createFlingSettle(ctx, {
      isRepinning: () => false,
      getAnchor: () => ({ id: 'm1', offsetWithinRow: 0 }),
      captureAnchor: () => calls.push('capture'),
      hasDeferredWork: () => true,
      onSettleQuiet: () => calls.push('release'),
    })

    settle.schedule()
    vi.advanceTimersByTime(FLING_SETTLE_MS)

    expect(calls).toEqual(['capture', 'release'])
  })
})
