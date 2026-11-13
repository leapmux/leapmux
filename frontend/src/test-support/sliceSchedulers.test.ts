import type { SliceBudget } from '~/lib/idleSlices'
import { describe, expect, it, vi } from 'vitest'
import { sliceScheduler } from '~/lib/idleSlices'
import { installControlledSlices, unlimitedSliceScheduler } from './sliceSchedulers'

describe('unlimitedSliceScheduler', () => {
  it('is the scheduler of a test that installs no other', () => {
    expect(sliceScheduler()).toBe(unlimitedSliceScheduler)
  })

  it('gives a budget that the work and what it defers never use up', () => {
    const budget = unlimitedSliceScheduler.startSlice()

    budget.defer(Number.MAX_SAFE_INTEGER)

    expect(budget.used()).toBe(false)
  })

  it('runs a requested slice in a microtask, never in the call that requests it', async () => {
    const slice = vi.fn()

    unlimitedSliceScheduler.requestSlice(slice)

    expect(slice).not.toHaveBeenCalled()
    await Promise.resolve()
    expect(slice).toHaveBeenCalledOnce()
    expect((slice.mock.calls[0]![0] as SliceBudget).used()).toBe(false)
  })

  it('runs no slice that the work cancels before its microtask', async () => {
    const slice = vi.fn()

    const cancel = unlimitedSliceScheduler.requestSlice(slice)
    cancel()
    await Promise.resolve()

    expect(slice).not.toHaveBeenCalled()
  })
})

describe('installControlledSlices', () => {
  it('installs itself as the scheduler that sliced work reads', () => {
    const slices = installControlledSlices(1)

    expect(sliceScheduler()).toBe(slices)
  })

  it('reports a budget used at the check after its last step', () => {
    const budget = installControlledSlices(3).startSlice()

    expect([
      budget.used(),
      budget.used(),
      budget.used(),
    ]).toEqual([false, false, true])
  })

  it('reports a budget of zero steps used at the first check', () => {
    const budget = installControlledSlices(0).startSlice()

    expect(budget.used()).toBe(true)
  })

  it('counts no deferred work against a budget', () => {
    const budget = installControlledSlices(2).startSlice()

    budget.defer(Number.MAX_SAFE_INTEGER)

    expect(budget.used()).toBe(false)
  })

  it('starts a slice of its own for each call outside a requested slice', () => {
    const slices = installControlledSlices(1)

    expect(slices.startSlice()).not.toBe(slices.startSlice())
  })

  it('shares the budget of a requested slice with the work that starts in it, and only during it', () => {
    const slices = installControlledSlices(1)
    let sliceBudget: SliceBudget | undefined
    let startedInSlice: SliceBudget | undefined
    slices.requestSlice((budget) => {
      sliceBudget = budget
      startedInSlice = slices.startSlice()
    })

    slices.runSlice()

    expect(startedInSlice).toBe(sliceBudget)
    expect(slices.startSlice()).not.toBe(sliceBudget)
  })

  it('stops sharing the budget of a requested slice that throws', () => {
    const slices = installControlledSlices(1)
    let sliceBudget: SliceBudget | undefined
    slices.requestSlice((budget) => {
      sliceBudget = budget
      throw new Error('the slice failed')
    })

    expect(() => slices.runSlice()).toThrow('the slice failed')

    expect(slices.startSlice()).not.toBe(sliceBudget)
    expect(slices.waiting()).toBe(0)
  })

  it('runs nothing until the test runs a slice, and then the one that waits longest', () => {
    const slices = installControlledSlices(1)
    const ran: string[] = []
    slices.requestSlice(() => ran.push('first'))
    slices.requestSlice(() => ran.push('second'))

    expect(ran).toEqual([])
    expect(slices.waiting()).toBe(2)
    slices.runSlice()

    expect(ran).toEqual(['first'])
    expect(slices.waiting()).toBe(1)
  })

  it('throws when the test runs a slice and none waits', () => {
    const slices = installControlledSlices(1)

    expect(() => slices.runSlice()).toThrow('no slice waits to run')
  })

  it('cancels only its own request when the work requests the same function twice', () => {
    const slices = installControlledSlices(1)
    const slice = vi.fn()
    const cancelFirst = slices.requestSlice(slice)
    slices.requestSlice(slice)

    cancelFirst()
    cancelFirst()

    expect(slices.waiting()).toBe(1)
    slices.runSlice()
    expect(slice).toHaveBeenCalledOnce()
  })

  it('does nothing for a cancel after its slice ran', () => {
    const slices = installControlledSlices(1)
    const cancel = slices.requestSlice(() => {})
    const later = vi.fn()
    slices.runSlice()
    slices.requestSlice(later)

    cancel()

    expect(slices.waiting()).toBe(1)
    slices.runSlice()
    expect(later).toHaveBeenCalledOnce()
  })

  it('runs every slice, those that a slice requests too, and returns how many ran', () => {
    const slices = installControlledSlices(1)
    let remaining = 3
    const step = (): void => {
      remaining--
      if (remaining > 0)
        slices.requestSlice(step)
    }
    slices.requestSlice(step)

    expect(slices.runAllSlices()).toBe(3)
    expect(slices.waiting()).toBe(0)
    expect(slices.runAllSlices()).toBe(0)
  })

  it('reports work that never stops requesting slices, instead of running forever', () => {
    const slices = installControlledSlices(1)
    const forever = (): void => {
      slices.requestSlice(forever)
    }
    slices.requestSlice(forever)

    expect(() => slices.runAllSlices()).toThrow('the work still requested slices after 100000 of them')
  })
})
