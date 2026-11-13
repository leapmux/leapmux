import type { SliceBudget } from './idleSlices'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { IDLE_SLICE_TIMEOUT_MS, idleSliceScheduler, setSliceSchedulerForTests, sliceScheduler } from './idleSlices'
import { WORK_SLICE_MS } from './workSlices'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** A clock that the test moves, read through `performance.now`. */
function stubClock(): { advance: (ms: number) => void } {
  let now = 1000
  vi.spyOn(performance, 'now').mockImplementation(() => now)
  return {
    advance: (ms) => {
      now += ms
    },
  }
}

/** Idle callbacks that wait until the test runs them, with the deadline that the test gives. */
function stubIdleCallbacks() {
  const waiting = new Map<number, IdleRequestCallback>()
  let nextHandle = 1
  const requestIdleCallback = vi.fn((callback: IdleRequestCallback, _options?: IdleRequestOptions) => {
    const handle = nextHandle++
    waiting.set(handle, callback)
    return handle
  })
  const cancelIdleCallback = vi.fn((handle: number) => {
    waiting.delete(handle)
  })
  vi.stubGlobal('requestIdleCallback', requestIdleCallback)
  vi.stubGlobal('cancelIdleCallback', cancelIdleCallback)
  return {
    requestIdleCallback,
    cancelIdleCallback,
    waiting,
    /** Run every waiting callback with an idle deadline that has `remaining` ms left. */
    runIdle(remaining: () => number, didTimeout = false) {
      const callbacks = [
        ...waiting.values(),
      ]
      waiting.clear()
      for (const callback of callbacks)
        callback({ didTimeout, timeRemaining: remaining })
    },
  }
}

describe('idleSliceScheduler', () => {
  it('gives a slice that starts now one work slice', async () => {
    const clock = stubClock()
    await Promise.resolve()
    const budget = idleSliceScheduler.startSlice()

    clock.advance(WORK_SLICE_MS - 1)
    expect(budget.used()).toBe(false)
    clock.advance(1)
    expect(budget.used()).toBe(true)
  })

  it('counts the work that a slice defers against its budget', async () => {
    const clock = stubClock()
    await Promise.resolve()
    const budget = idleSliceScheduler.startSlice()

    budget.defer(WORK_SLICE_MS - 2)
    clock.advance(1)
    expect(budget.used()).toBe(false)
    clock.advance(1)
    expect(budget.used()).toBe(true)
  })

  it('shares one slice between the callers of one synchronous run, and starts a new one after it', async () => {
    const clock = stubClock()
    await Promise.resolve()
    const first = idleSliceScheduler.startSlice()
    first.defer(WORK_SLICE_MS / 2)
    const second = idleSliceScheduler.startSlice()

    expect(second).toBe(first)
    second.defer(WORK_SLICE_MS / 2)
    expect(first.used()).toBe(true)

    await Promise.resolve()
    const later = idleSliceScheduler.startSlice()

    expect(later).not.toBe(first)
    expect(later.used()).toBe(false)
    clock.advance(WORK_SLICE_MS)
    expect(later.used()).toBe(true)
  })

  it('runs a requested slice in an idle period, with a timeout for a page that never goes idle', () => {
    stubClock()
    const idle = stubIdleCallbacks()
    const ran = vi.fn()

    idleSliceScheduler.requestSlice(ran)

    expect(idle.requestIdleCallback).toHaveBeenCalledWith(expect.any(Function), { timeout: IDLE_SLICE_TIMEOUT_MS })
    expect(ran).not.toHaveBeenCalled()
    idle.runIdle(() => 20)
    expect(ran).toHaveBeenCalledTimes(1)
  })

  it('ends a requested slice where its idle period ends, even before a work slice', () => {
    stubClock()
    const idle = stubIdleCallbacks()
    let budget: SliceBudget | undefined
    let remaining = 2
    idleSliceScheduler.requestSlice((received) => {
      budget = received
    })

    idle.runIdle(() => remaining)

    expect(budget?.used()).toBe(false)
    remaining = 0
    expect(budget?.used()).toBe(true)
  })

  it('ends a requested slice where its idle period ends, counting the work that it defers', () => {
    stubClock()
    const idle = stubIdleCallbacks()
    let budget: SliceBudget | undefined
    idleSliceScheduler.requestSlice((received) => {
      budget = received
    })

    idle.runIdle(() => 3)

    budget?.defer(2)
    expect(budget?.used()).toBe(false)
    budget?.defer(1)
    expect(budget?.used()).toBe(true)
  })

  it('ends a requested slice after one work slice, even when its idle period lasts longer', () => {
    const clock = stubClock()
    const idle = stubIdleCallbacks()
    let budget: SliceBudget | undefined
    idleSliceScheduler.requestSlice((received) => {
      budget = received
    })

    idle.runIdle(() => 50)

    clock.advance(WORK_SLICE_MS - 1)
    expect(budget?.used()).toBe(false)
    clock.advance(1)
    expect(budget?.used()).toBe(true)
  })

  it('gives one work slice to a slice that its timeout started', () => {
    const clock = stubClock()
    const idle = stubIdleCallbacks()
    let budget: SliceBudget | undefined
    idleSliceScheduler.requestSlice((received) => {
      budget = received
    })

    idle.runIdle(() => 0, true)

    expect(budget?.used()).toBe(false)
    clock.advance(WORK_SLICE_MS)
    expect(budget?.used()).toBe(true)
  })

  // Work that a requested slice starts, such as a gap separator that the slice
  // mounts, would otherwise open a second budget in the same task and mount a
  // second slice's lines before the browser can paint.
  it('shares the budget of a requested slice with the work that starts in it, and only during it', async () => {
    stubClock()
    const idle = stubIdleCallbacks()
    await Promise.resolve()
    let sliceBudget: SliceBudget | undefined
    let startedInSlice: SliceBudget | undefined
    idleSliceScheduler.requestSlice((budget) => {
      sliceBudget = budget
      startedInSlice = idleSliceScheduler.startSlice()
    })

    idle.runIdle(() => 50)

    expect(startedInSlice).toBe(sliceBudget)
    await Promise.resolve()
    expect(idleSliceScheduler.startSlice()).not.toBe(sliceBudget)
  })

  // Only startSlice queues the microtask that ends a task's slice, so a slice
  // that throws and kept its budget as the task's slice would hand that spent
  // budget to every later caller: each later run would take one step per slice.
  it('stops sharing the budget of a requested slice that throws', async () => {
    stubClock()
    const idle = stubIdleCallbacks()
    await Promise.resolve()
    let sliceBudget: SliceBudget | undefined
    idleSliceScheduler.requestSlice((budget) => {
      sliceBudget = budget
      throw new Error('the slice failed')
    })

    expect(() => idle.runIdle(() => 50)).toThrow('the slice failed')

    expect(sliceBudget).toBeDefined()
    expect(idleSliceScheduler.startSlice()).not.toBe(sliceBudget)
  })

  it('cancels a requested slice before it runs', () => {
    stubClock()
    const idle = stubIdleCallbacks()
    const ran = vi.fn()

    const cancel = idleSliceScheduler.requestSlice(ran)
    cancel()
    idle.runIdle(() => 20)

    expect(idle.cancelIdleCallback).toHaveBeenCalledTimes(1)
    expect(ran).not.toHaveBeenCalled()
  })
})

describe('setSliceSchedulerForTests', () => {
  it('restores the scheduler of the app when it receives no scheduler', () => {
    setSliceSchedulerForTests()

    expect(sliceScheduler()).toBe(idleSliceScheduler)
  })
})
