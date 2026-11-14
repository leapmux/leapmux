import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createScrollRest, SCROLL_REST_QUIET_MS } from './chatScrollRest'

/** A rest detector with a manual frame queue, and the state that its dependencies read. */
function restHarness(init: { inputActive?: boolean } = {}) {
  const state = { inputActive: init.inputActive ?? false, unseenMove: false }
  const frames = new Map<number, () => void>()
  let nextHandle = 1
  const requestFrame = vi.fn((cb: () => void) => {
    const handle = nextHandle++
    frames.set(handle, cb)
    return handle
  })
  const cancelFrame = vi.fn((handle: number) => {
    frames.delete(handle)
  })
  const onRest = vi.fn()
  const rest = createScrollRest({
    isInputActive: () => state.inputActive,
    hasUnseenMove: () => state.unseenMove,
    onRest,
    requestFrame,
    cancelFrame,
  })
  const runFrames = () => {
    const pending = [
      ...frames.values(),
    ]
    frames.clear()
    for (const cb of pending)
      cb()
  }
  return {
    state,
    rest,
    onRest,
    requestFrame,
    cancelFrame,
    runFrames,
    pendingFrames: () => frames.size,
  }
}

describe('createScrollRest', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('rests at creation', () => {
    const { rest, onRest } = restHarness()
    expect(rest.isResting()).toBe(true)
    expect(onRest).not.toHaveBeenCalled()
  })

  it('rests after the quiet time and one frame', () => {
    const { rest, onRest, requestFrame, runFrames } = restHarness()
    rest.noteScroll()
    expect(rest.isResting()).toBe(false)
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS - 1)
    expect(requestFrame).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(requestFrame).toHaveBeenCalledOnce()
    expect(rest.isResting()).toBe(false)

    runFrames()

    expect(rest.isResting()).toBe(true)
    expect(onRest).toHaveBeenCalledOnce()
  })

  it('restarts the quiet time on each scroll', () => {
    const { rest, requestFrame } = restHarness()
    rest.noteScroll()
    vi.advanceTimersByTime(100)
    rest.noteScroll()
    vi.advanceTimersByTime(100)
    rest.noteScroll()
    vi.advanceTimersByTime(149)
    expect(requestFrame).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(requestFrame).toHaveBeenCalledOnce()
  })

  it('starts again when the position changed before the frame', () => {
    const { state, rest, onRest, requestFrame, runFrames } = restHarness()
    rest.noteScroll()
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    state.unseenMove = true
    runFrames()
    expect(onRest).not.toHaveBeenCalled()
    expect(rest.isResting()).toBe(false)

    state.unseenMove = false
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS - 1)
    expect(requestFrame).toHaveBeenCalledOnce()
    vi.advanceTimersByTime(1)
    expect(requestFrame).toHaveBeenCalledTimes(2)
    runFrames()
    expect(onRest).toHaveBeenCalledOnce()
  })

  it('cancels the frame on a scroll', () => {
    const { rest, onRest, requestFrame, cancelFrame, runFrames } = restHarness()
    rest.noteScroll()
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    const handle = requestFrame.mock.results[0]?.value as number
    rest.noteScroll()
    expect(cancelFrame).toHaveBeenCalledWith(handle)
    runFrames()
    expect(onRest).not.toHaveBeenCalled()
    expect(rest.isResting()).toBe(false)
  })

  it('reports no rest while input is down', () => {
    const { rest } = restHarness({ inputActive: true })
    expect(rest.isResting()).toBe(false)
  })

  it('does not rest while input is down', () => {
    const { state, rest, onRest, requestFrame, runFrames, pendingFrames } = restHarness()
    rest.noteScroll()
    state.inputActive = true
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    runFrames()

    expect(onRest).not.toHaveBeenCalled()
    expect(rest.isResting()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    expect(pendingFrames()).toBe(0)
    expect(requestFrame).toHaveBeenCalledOnce()
  })

  it('rests when the input ends after the quiet time', () => {
    const { state, rest, onRest, runFrames } = restHarness()
    rest.noteScroll()
    state.inputActive = true
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    runFrames()
    state.inputActive = false

    rest.noteInputEnded()

    expect(onRest).toHaveBeenCalledOnce()
    expect(rest.isResting()).toBe(true)
  })

  it('rests when the input ends without a scroll', () => {
    const { state, rest, onRest } = restHarness()
    state.inputActive = true
    expect(rest.isResting()).toBe(false)
    state.inputActive = false

    rest.noteInputEnded()

    expect(onRest).toHaveBeenCalledOnce()
  })

  it('does not rest when the input ends during a scroll', () => {
    const { rest, onRest, runFrames } = restHarness()
    rest.noteScroll()
    vi.advanceTimersByTime(50)
    rest.noteInputEnded()
    expect(onRest).not.toHaveBeenCalled()

    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS - 50)
    expect(onRest).not.toHaveBeenCalled()
    runFrames()
    expect(onRest).toHaveBeenCalledOnce()
  })

  it('does not rest when the input ends while the confirm frame is pending', () => {
    const { rest, onRest, runFrames } = restHarness()
    rest.noteScroll()
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    rest.noteInputEnded()
    expect(onRest).not.toHaveBeenCalled()
    runFrames()
    expect(onRest).toHaveBeenCalledOnce()
  })

  it('calls onRest again when the input ends twice', () => {
    const { rest, onRest } = restHarness()
    rest.noteInputEnded()
    rest.noteInputEnded()
    expect(onRest).toHaveBeenCalledTimes(2)
  })

  it('clears the timer on dispose', () => {
    const { rest, onRest, requestFrame, runFrames } = restHarness()
    rest.noteScroll()
    rest.dispose()
    vi.advanceTimersByTime(1000)
    runFrames()
    expect(requestFrame).not.toHaveBeenCalled()
    expect(onRest).not.toHaveBeenCalled()
  })

  it('cancels the frame on dispose', () => {
    const { rest, onRest, runFrames } = restHarness()
    rest.noteScroll()
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    rest.dispose()
    runFrames()
    expect(onRest).not.toHaveBeenCalled()
  })

  it('confirms in a frame that runs inside requestFrame', () => {
    const onRest = vi.fn()
    const rest = createScrollRest({
      isInputActive: () => false,
      hasUnseenMove: () => false,
      onRest,
      requestFrame: (cb) => {
        cb()
        return 7
      },
      cancelFrame: () => {},
    })
    rest.noteScroll()
    vi.advanceTimersByTime(SCROLL_REST_QUIET_MS)
    expect(onRest).toHaveBeenCalledOnce()
    // The synchronous frame left nothing pending, so the end of an input rests at once.
    rest.noteInputEnded()
    expect(onRest).toHaveBeenCalledTimes(2)
  })
})
