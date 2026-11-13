import { createMemo, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createCoalescedVersion, createWorkerDemand } from './workerDemand'

describe('createWorkerDemand', () => {
  it('stays low while no consumer waits, as for a prefetch', () => {
    expect(createWorkerDemand().isLow()).toBe(true)
  })

  it('turns urgent for good once a consumer with no thunk waits', () => {
    const demand = createWorkerDemand()
    demand.note()
    demand.note(() => true)

    expect(demand.isLow()).toBe(false)
  })

  it('stays high while any consumer thunk says its row needs the request', () => {
    const demand = createWorkerDemand()
    let visibleRowOffscreen = false
    demand.note(() => visibleRowOffscreen)
    demand.note(() => true)

    expect(demand.isLow()).toBe(false)
    visibleRowOffscreen = true
    expect(demand.isLow()).toBe(true)
  })

  it('reads each thunk at every call, so a row that scrolls in raises the request', () => {
    const demand = createWorkerDemand()
    let offscreen = true
    demand.note(() => offscreen)

    expect(demand.isLow()).toBe(true)
    offscreen = false
    expect(demand.isLow()).toBe(false)
  })

  it('keeps one entry for a thunk that a consumer notes again', () => {
    const demand = createWorkerDemand()
    const high = () => false
    demand.note(high)
    for (let i = 0; i < 100; i++)
      demand.note(high)
    // The repeats take one slot, so the 31 low thunks after them still leave the
    // high thunk among the 32 recent ones.
    for (let i = 0; i < 31; i++)
      demand.note(() => true)

    expect(demand.isLow()).toBe(false)
  })

  it('moves a thunk that a consumer notes again to the newest place, so that it outlives the thunks noted between', () => {
    const demand = createWorkerDemand()
    const high = () => false
    demand.note(high)
    for (let i = 0; i < 31; i++)
      demand.note(() => true)
    // The consumer re-runs: without the move, `high` would stay the oldest entry,
    // and the next low thunk would push it out.
    demand.note(high)
    demand.note(() => true)

    expect(demand.isLow()).toBe(false)
  })

  it('keeps only the recent thunks, so a consumer that stopped asking no longer holds the request high', () => {
    const demand = createWorkerDemand()
    demand.note(() => false)
    for (let i = 0; i < 32; i++)
      demand.note(() => true)

    expect(demand.isLow()).toBe(true)
  })
})

describe('createCoalescedVersion', () => {
  it('re-runs a reader once for a burst of bumps within one task', async () => {
    const version = createCoalescedVersion()
    let runs = 0
    const dispose = createRoot((dispose) => {
      const read = createMemo(() => {
        runs++
        return version.read()
      })
      read()
      return dispose
    })
    expect(runs).toBe(1)

    version.bump()
    version.bump()
    version.bump()
    expect(runs).toBe(1)
    await Promise.resolve()

    expect(runs).toBe(2)
    dispose()
  })

  it('bumps again after the earlier bump has landed', async () => {
    const version = createCoalescedVersion()
    version.bump()
    await Promise.resolve()
    version.bump()
    await Promise.resolve()

    expect(version.read()).toBe(2)
  })
})
