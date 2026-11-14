import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createHmrSettleWindow, HMR_SETTLE_MS } from './chatScrollHmrSettleWindow'

/** A settle window inside a reactive root, which starts its timer at the mount. */
function mountWindow(hmrRemount: boolean) {
  let dispose!: () => void
  const settle = createRoot((d) => {
    dispose = d
    return createHmrSettleWindow(hmrRemount)
  })
  return { settle, dispose }
}

describe('createHmrSettleWindow', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('stays closed for a mount that no HMR module swap caused', () => {
    const { settle, dispose } = mountWindow(false)
    expect(settle.isSettling()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    dispose()
  })

  it('closes HMR_SETTLE_MS after the mount of an HMR remount', () => {
    const { settle, dispose } = mountWindow(true)
    expect(settle.isSettling()).toBe(true)

    vi.advanceTimersByTime(HMR_SETTLE_MS - 1)
    expect(settle.isSettling()).toBe(true)
    vi.advanceTimersByTime(1)
    expect(settle.isSettling()).toBe(false)
    dispose()
  })

  it('closes at end, before its timer', () => {
    const { settle, dispose } = mountWindow(true)

    settle.end()

    expect(settle.isSettling()).toBe(false)
    vi.advanceTimersByTime(HMR_SETTLE_MS)
    expect(settle.isSettling()).toBe(false)
    dispose()
  })

  it('clears its timer at the cleanup of its owner', () => {
    const { dispose } = mountWindow(true)
    expect(vi.getTimerCount()).toBe(1)

    dispose()

    expect(vi.getTimerCount()).toBe(0)
  })
})
