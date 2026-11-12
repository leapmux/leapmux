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

  it('resumes after a task that was already queued', async () => {
    const order: string[] = []
    setTimeout(() => order.push('queued task'), 0)
    await yieldToEventLoop()
    order.push('resumed')
    expect(order).toEqual(['queued task', 'resumed'])
  })

  it('uses scheduler.yield where the browser has it', async () => {
    const yieldTask = vi.fn(() => Promise.resolve())
    vi.stubGlobal('scheduler', { yield: yieldTask })
    await yieldToEventLoop()
    expect(yieldTask).toHaveBeenCalledOnce()
  })
})
