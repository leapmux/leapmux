import { afterEach, describe, expect, it, vi } from 'vitest'
import { createWorkSlicer, WORK_SLICE_MS, yieldToEventLoop } from './workSlices'

describe('createWorkSlicer', () => {
  it('reports the slice used once it has run for its full length', () => {
    let clock = 100
    const slicer = createWorkSlicer(WORK_SLICE_MS, () => clock)
    expect(slicer.sliceUsed()).toBe(false)
    clock += WORK_SLICE_MS - 1
    expect(slicer.sliceUsed()).toBe(false)
    clock += 1
    expect(slicer.sliceUsed()).toBe(true)
  })

  it('starts a fresh slice after the yield', async () => {
    let clock = 0
    const slicer = createWorkSlicer(4, () => clock)
    clock = 10
    expect(slicer.sliceUsed()).toBe(true)
    await slicer.nextSlice()
    expect(slicer.sliceUsed()).toBe(false)
    clock = 14
    expect(slicer.sliceUsed()).toBe(true)
  })

  it('reports every slice used when its length is zero', () => {
    const slicer = createWorkSlicer(0, () => 5)
    expect(slicer.sliceUsed()).toBe(true)
  })
})

describe('yieldToEventLoop', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('resumes after every microtask that runs before the next task', async () => {
    const order: string[] = []
    const yielded = yieldToEventLoop().then(() => order.push('resumed'))
    // A microtask that queues another one 100 times: a yield that resolved in a
    // microtask would resume in the middle of the chain.
    let hops = 0
    const hop = () => {
      hops++
      if (hops < 100)
        queueMicrotask(hop)
      else
        order.push('microtasks done')
    }
    queueMicrotask(hop)

    await yielded

    expect(order).toEqual(['microtasks done', 'resumed'])
  })

  // Chrome shipped scheduler.postTask several versions before scheduler.yield.
  it.each([
    { label: 'no scheduler', scheduler: undefined },
    {
      label: 'a scheduler without a yield method',
      scheduler: {
        postTask: () => Promise.resolve(),
      },
    },
  ])('yields through a message where the browser has no scheduler.yield: $label', async ({ scheduler }) => {
    vi.stubGlobal('scheduler', scheduler)
    const deliveries: Array<() => void> = []
    const closed = vi.fn()
    vi.stubGlobal('MessageChannel', class {
      port1 = {
        onmessage: null as (() => void) | null,
        close: closed,
      }

      port2 = {
        postMessage: () => deliveries.push(() => this.port1.onmessage?.()),
      }
    })
    let resumed = false

    const yielded = yieldToEventLoop().then(() => {
      resumed = true
    })
    await Promise.resolve()
    await Promise.resolve()

    // Only the message resumes the work: no timer, which a hidden page throttles.
    expect(deliveries).toHaveLength(1)
    expect(resumed).toBe(false)
    deliveries[0]!()
    await yielded
    expect(resumed).toBe(true)
    // A port with a listener keeps a Node process alive until it closes.
    expect(closed).toHaveBeenCalledOnce()
  })

  it('uses scheduler.yield where the browser has it', async () => {
    const yieldTask = vi.fn(() => Promise.resolve())
    vi.stubGlobal('scheduler', { yield: yieldTask })
    await yieldToEventLoop()
    expect(yieldTask).toHaveBeenCalledOnce()
  })
})
