import type { OwnedTimers } from './ownedTimers'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createOwnedTimers } from './ownedTimers'

describe('createOwnedTimers', () => {
  let disposeRoot: (() => void) | undefined

  function mount(): OwnedTimers {
    return createRoot((dispose) => {
      disposeRoot = dispose
      return createOwnedTimers()
    })
  }

  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    disposeRoot?.()
    disposeRoot = undefined
    vi.useRealTimers()
  })

  it('runs the callback once, after the delay', () => {
    const timers = mount()
    const callback = vi.fn()
    timers.after(100, callback)

    vi.advanceTimersByTime(99)
    expect(callback).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(callback).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(1000)
    expect(callback).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('runs the callback of a zero delay after the current task, not at once', () => {
    const timers = mount()
    const callback = vi.fn()
    timers.after(0, callback)
    expect(callback).not.toHaveBeenCalled()

    vi.advanceTimersByTime(0)
    expect(callback).toHaveBeenCalledTimes(1)
  })

  it('cancels one timer through the function that after returns, and keeps the others', () => {
    const timers = mount()
    const cancelled = vi.fn()
    const kept = vi.fn()
    const cancel = timers.after(100, cancelled)
    timers.after(100, kept)

    cancel()
    expect(vi.getTimerCount()).toBe(1)
    vi.advanceTimersByTime(100)

    expect(cancelled).not.toHaveBeenCalled()
    expect(kept).toHaveBeenCalledTimes(1)
  })

  it('ignores a cancel of a timer that already ran', () => {
    const timers = mount()
    const cancelFirst = timers.after(10, () => {})
    const second = vi.fn()
    timers.after(20, second)
    vi.advanceTimersByTime(10)

    cancelFirst()

    expect(vi.getTimerCount()).toBe(1)
    vi.advanceTimersByTime(10)
    expect(second).toHaveBeenCalledTimes(1)
  })

  it('starts a timer from the callback of another', () => {
    const timers = mount()
    const inner = vi.fn()
    timers.after(10, () => timers.after(10, inner))

    vi.advanceTimersByTime(19)
    expect(inner).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(inner).toHaveBeenCalledTimes(1)
  })

  it('cancels every pending timer on clear, and starts a timer again after it', () => {
    const timers = mount()
    const cleared = vi.fn()
    timers.after(10, cleared)
    timers.after(20, cleared)

    timers.clear()
    expect(vi.getTimerCount()).toBe(0)
    vi.advanceTimersByTime(20)
    expect(cleared).not.toHaveBeenCalled()

    const later = vi.fn()
    timers.after(10, later)
    vi.advanceTimersByTime(10)
    expect(later).toHaveBeenCalledTimes(1)
  })

  it('cancels every pending timer when its owner is disposed', () => {
    const timers = mount()
    const callback = vi.fn()
    timers.after(10, callback)
    timers.after(20, callback)

    disposeRoot!()
    disposeRoot = undefined

    expect(vi.getTimerCount()).toBe(0)
    vi.advanceTimersByTime(20)
    expect(callback).not.toHaveBeenCalled()
  })

  it('starts no timer after its owner is disposed', () => {
    const timers = mount()
    disposeRoot!()
    disposeRoot = undefined

    const callback = vi.fn()
    const cancel = timers.after(10, callback)
    expect(vi.getTimerCount()).toBe(0)

    // The cancel function of a timer that never started does nothing.
    cancel()
    vi.advanceTimersByTime(10)
    expect(callback).not.toHaveBeenCalled()
  })
})
