import type { RafCoalescer } from './rafCoalesce'
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

  it('is a no-op on abort when nothing is pending', () => {
    const coalescer = createRafCoalescer<string>(() => {})

    coalescer.abort()

    expect(cancelAnimationFrame).not.toHaveBeenCalled()
    expect(coalescer.isPending()).toBe(false)
  })

  it('reports a pending frame from the push until the frame runs', () => {
    const coalescer = createRafCoalescer<string>(() => {})
    expect(coalescer.isPending()).toBe(false)

    coalescer.push('first')
    expect(coalescer.isPending()).toBe(true)

    runFrame(1)
    expect(coalescer.isPending()).toBe(false)
  })

  it('reports no pending frame after a flush or an abort', () => {
    const coalescer = createRafCoalescer<string>(() => {})

    coalescer.push('first')
    coalescer.flush()
    expect(coalescer.isPending()).toBe(false)

    coalescer.push('second')
    coalescer.abort()
    expect(coalescer.isPending()).toBe(false)
  })

  it('dispatches a null event', () => {
    const seen: Array<string | null> = []
    const coalescer = createRafCoalescer<string | null>(value => seen.push(value))

    coalescer.push(null)
    runFrame(1)

    expect(seen).toEqual([null])
  })

  it('schedules a new frame for a push from inside the handler', () => {
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>((value) => {
      seen.push(value)
      if (value === 'first')
        coalescer.push('second')
    })

    coalescer.push('first')
    runFrame(1)

    expect(seen).toEqual(['first'])
    expect(coalescer.isPending()).toBe(true)
    runFrame(2)
    expect(seen).toEqual(['first', 'second'])
  })

  it.each<[string, (coalescer: RafCoalescer<string>) => void]>([
    [
      'the frame',
      () => runFrame(1),
    ],
    [
      'a flush',
      coalescer => coalescer.flush(),
    ],
  ])('schedules again after the handler throws in %s', (_name, dispatch) => {
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>((value) => {
      seen.push(value)
      if (value === 'first')
        throw new Error('handler failed')
    })

    coalescer.push('first')
    expect(() => dispatch(coalescer)).toThrow('handler failed')

    // The coalescer cleared its pending frame before the call, so the throw leaves none.
    expect(coalescer.isPending()).toBe(false)
    coalescer.push('second')
    expect(requestAnimationFrame).toHaveBeenCalledTimes(2)
    runFrame(2)
    expect(seen).toEqual(['first', 'second'])
  })

  it('schedules again after a frame that ran inside requestAnimationFrame', () => {
    vi.stubGlobal('requestAnimationFrame', vi.fn((cb: FrameRequestCallback) => {
      cb(performance.now())
      return 7
    }))
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>(value => seen.push(value))

    coalescer.push('first')
    coalescer.push('second')

    expect(seen).toEqual(['first', 'second'])
    expect(coalescer.isPending()).toBe(false)
  })

  it('dispatches each push at once where requestAnimationFrame is absent', () => {
    vi.stubGlobal('requestAnimationFrame', undefined)
    vi.stubGlobal('cancelAnimationFrame', undefined)
    const seen: string[] = []
    const coalescer = createRafCoalescer<string>(value => seen.push(value))

    coalescer.push('first')
    coalescer.push('second')

    expect(seen).toEqual(['first', 'second'])
    expect(coalescer.isPending()).toBe(false)
  })

  describe('with injected frame functions', () => {
    it('requests and cancels its frames through them', () => {
      const requestFrame = vi.fn((_cb: () => void) => 41)
      const cancelFrame = vi.fn((_handle: number) => {})
      const coalescer = createRafCoalescer<string>(() => {}, { requestFrame, cancelFrame })

      coalescer.push('first')
      coalescer.push('second')
      coalescer.abort()

      expect(requestFrame).toHaveBeenCalledOnce()
      expect(cancelFrame).toHaveBeenCalledExactlyOnceWith(41)
      expect(requestAnimationFrame).not.toHaveBeenCalled()
      expect(cancelAnimationFrame).not.toHaveBeenCalled()
    })

    it('dispatches every push under a synchronous frame', () => {
      const seen: string[] = []
      const coalescer = createRafCoalescer<string>(value => seen.push(value), {
        requestFrame: (cb) => {
          cb()
          return 7
        },
        cancelFrame: () => {},
      })

      coalescer.push('first')
      coalescer.push('second')

      expect(seen).toEqual(['first', 'second'])
      expect(coalescer.isPending()).toBe(false)
    })

    it('ignores a frame that runs after an abort, when the cancel did not stop it', () => {
      const callbacks: Array<() => void> = []
      const seen: string[] = []
      const coalescer = createRafCoalescer<string>(value => seen.push(value), {
        requestFrame: (cb) => {
          callbacks.push(cb)
          return callbacks.length
        },
        cancelFrame: () => {},
      })

      coalescer.push('first')
      coalescer.abort()
      coalescer.push('second')
      callbacks[0]!()

      expect(seen).toEqual([])
      expect(coalescer.isPending()).toBe(true)
      callbacks[1]!()
      expect(seen).toEqual(['second'])
    })

    it('ignores a frame that runs after a flush, when the cancel did not stop it', () => {
      const callbacks: Array<() => void> = []
      const seen: string[] = []
      const coalescer = createRafCoalescer<string>(value => seen.push(value), {
        requestFrame: (cb) => {
          callbacks.push(cb)
          return callbacks.length
        },
        cancelFrame: () => {},
      })

      coalescer.push('first')
      coalescer.flush()
      callbacks[0]!()

      expect(seen).toEqual(['first'])
      expect(coalescer.isPending()).toBe(false)
    })

    it('falls back to the default for a function that is undefined', () => {
      const seen: string[] = []
      const coalescer = createRafCoalescer<string>(value => seen.push(value), {
        requestFrame: undefined,
        cancelFrame: undefined,
      })

      coalescer.push('first')
      coalescer.abort()

      expect(cancelAnimationFrame).toHaveBeenCalledWith(1)
      runFrame(1)
      expect(seen).toEqual([])
    })
  })
})
