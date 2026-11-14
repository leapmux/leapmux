import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelFrame, requestFrame } from './animationFrame'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('requestFrame', () => {
  it('runs the callback in the animation frame, with no timestamp, and returns its handle', () => {
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', vi.fn((frame: FrameRequestCallback) => {
      frames.push(frame)
      return 41
    }))
    const cb = vi.fn()

    expect(requestFrame(cb)).toBe(41)
    expect(cb).not.toHaveBeenCalled()

    frames[0]!(16)
    expect(cb).toHaveBeenCalledOnce()
    expect(cb).toHaveBeenCalledWith()
  })

  it('runs the callback at once and returns 0 without requestAnimationFrame', () => {
    vi.stubGlobal('requestAnimationFrame', undefined)
    const cb = vi.fn()

    expect(requestFrame(cb)).toBe(0)
    expect(cb).toHaveBeenCalledOnce()
  })
})

describe('cancelFrame', () => {
  it('cancels the animation frame of the handle', () => {
    const cancel = vi.fn()
    vi.stubGlobal('cancelAnimationFrame', cancel)

    cancelFrame(7)

    expect(cancel).toHaveBeenCalledExactlyOnceWith(7)
  })

  it('does nothing without cancelAnimationFrame', () => {
    vi.stubGlobal('cancelAnimationFrame', undefined)

    expect(() => cancelFrame(7)).not.toThrow()
  })
})
