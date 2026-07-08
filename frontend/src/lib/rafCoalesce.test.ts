import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRafCoalescer } from './rafCoalesce'

describe('createRafCoalescer', () => {
  let frames: Array<FrameRequestCallback | undefined>
  let nextHandle: number

  const runFrame = (handle: number) => {
    const frame = frames[handle]
    frames[handle] = undefined
    frame?.(performance.now())
  }

  beforeEach(() => {
    frames = []
    nextHandle = 1
    vi.stubGlobal('requestAnimationFrame', vi.fn((cb: FrameRequestCallback) => {
      const handle = nextHandle++
      frames[handle] = cb
      return handle
    }))
    vi.stubGlobal('cancelAnimationFrame', vi.fn((handle: number) => {
      frames[handle] = undefined
    }))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('coalesces multiple pushes into one frame with the latest value', () => {
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>(value => seen.push(value))

    coalescer.push('first')
    coalescer.push('second')
    coalescer.push('third')

    expect(requestAnimationFrame).toHaveBeenCalledTimes(1)
    expect(seen).toEqual([])

    runFrame(1)

    expect(seen).toEqual(['third'])
  })

  it('flushes the pending value synchronously and cancels the frame', () => {
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>(value => seen.push(value))

    coalescer.push('first')
    coalescer.push('second')
    coalescer.flush()

    expect(cancelAnimationFrame).toHaveBeenCalledWith(1)
    expect(seen).toEqual(['second'])

    runFrame(1)

    expect(seen).toEqual(['second'])
  })

  it('is a no-op on flush when nothing is pending', () => {
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>(value => seen.push(value))

    coalescer.flush()

    expect(seen).toEqual([])
    expect(cancelAnimationFrame).not.toHaveBeenCalled()
  })

  it('schedules a fresh frame when pushing after a flush', () => {
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>(value => seen.push(value))

    coalescer.push('first')
    coalescer.flush()
    coalescer.push('second')

    expect(requestAnimationFrame).toHaveBeenCalledTimes(2)
    expect(seen).toEqual(['first'])

    runFrame(2)

    expect(seen).toEqual(['first', 'second'])
  })

  it('aborts the pending frame without dispatching stale work', () => {
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>(value => seen.push(value))

    coalescer.push('first')
    coalescer.abort()

    expect(cancelAnimationFrame).toHaveBeenCalledWith(1)
    runFrame(1)

    expect(seen).toEqual([])
  })
})
