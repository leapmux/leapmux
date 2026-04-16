import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { trailingDebounce } from './debounce'

describe('trailingDebounce', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('fires once after the quiet period', () => {
    const fn = vi.fn()
    const debounced = trailingDebounce(fn, 100)
    debounced()
    expect(fn).not.toHaveBeenCalled()
    vi.advanceTimersByTime(99)
    expect(fn).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('coalesces rapid-fire calls into a single trailing invocation', () => {
    const fn = vi.fn()
    const debounced = trailingDebounce(fn, 100)
    debounced()
    vi.advanceTimersByTime(50)
    debounced()
    vi.advanceTimersByTime(50)
    debounced()
    vi.advanceTimersByTime(99)
    expect(fn).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('starts a fresh quiet period after firing', () => {
    const fn = vi.fn()
    const debounced = trailingDebounce(fn, 100)
    debounced()
    vi.advanceTimersByTime(100)
    expect(fn).toHaveBeenCalledTimes(1)
    debounced()
    vi.advanceTimersByTime(100)
    expect(fn).toHaveBeenCalledTimes(2)
  })

  describe('cancel()', () => {
    it('drops a pending invocation', () => {
      const fn = vi.fn()
      const debounced = trailingDebounce(fn, 100)
      debounced()
      debounced.cancel()
      vi.advanceTimersByTime(200)
      expect(fn).not.toHaveBeenCalled()
    })

    it('is a no-op when nothing is pending', () => {
      const fn = vi.fn()
      const debounced = trailingDebounce(fn, 100)
      expect(() => debounced.cancel()).not.toThrow()
      expect(fn).not.toHaveBeenCalled()
    })

    it('does not affect subsequent calls', () => {
      const fn = vi.fn()
      const debounced = trailingDebounce(fn, 100)
      debounced()
      debounced.cancel()
      debounced()
      vi.advanceTimersByTime(100)
      expect(fn).toHaveBeenCalledTimes(1)
    })
  })

  describe('flush()', () => {
    it('fires the pending invocation immediately', () => {
      const fn = vi.fn()
      const debounced = trailingDebounce(fn, 100)
      debounced()
      debounced.flush()
      expect(fn).toHaveBeenCalledTimes(1)
    })

    it('cancels the pending timer so it does not fire again', () => {
      const fn = vi.fn()
      const debounced = trailingDebounce(fn, 100)
      debounced()
      debounced.flush()
      vi.advanceTimersByTime(200)
      expect(fn).toHaveBeenCalledTimes(1)
    })

    it('is a no-op when nothing is pending', () => {
      const fn = vi.fn()
      const debounced = trailingDebounce(fn, 100)
      debounced.flush()
      expect(fn).not.toHaveBeenCalled()
    })

    it('allows new calls to schedule after a flush', () => {
      const fn = vi.fn()
      const debounced = trailingDebounce(fn, 100)
      debounced()
      debounced.flush()
      debounced()
      vi.advanceTimersByTime(100)
      expect(fn).toHaveBeenCalledTimes(2)
    })
  })
})
