import type { SliceBudget, SliceScheduler } from '~/lib/idleSlices'
import { setSliceSchedulerForTests } from '~/lib/idleSlices'

/** A budget that never runs out, whatever work the slice defers. */
const NO_LIMIT: SliceBudget = {
  defer: () => {},
  used: () => false,
}

/**
 * The scheduler of every test that installs no other (see `vitest.setup.ts`).
 * Each slice runs without a budget, so work that starts in a task also ends in
 * it, as before the work ran in slices. A slice that the work requests later
 * runs in a microtask, which a test awaits through `settleWorkerReplies`,
 * `findBy*` or `waitFor`.
 *
 * A budget measured in time would make the outcome of a test depend on the
 * speed of the machine: a loaded runner would stop the first slice early, and a
 * test that reads every line at once would fail at random.
 */
export const unlimitedSliceScheduler: SliceScheduler = {
  startSlice: () => NO_LIMIT,
  requestSlice(slice) {
    let cancelled = false
    queueMicrotask(() => {
      if (!cancelled)
        slice(NO_LIMIT)
    })
    return () => {
      cancelled = true
    }
  },
}

/** A scheduler whose requested slices run only when the test runs them. */
export interface ControlledSlices extends SliceScheduler {
  /** How many requested slices wait to run. */
  waiting: () => number
  /** Run the slice that waits longest. Throws when none waits. */
  runSlice: () => void
  /** Run slices until none waits, and return how many ran. */
  runAllSlices: () => number
}

/** The most slices that `runAllSlices` runs before it reports work that never ends. */
const MAX_SLICES_IN_ONE_RUN = 100_000

/**
 * A budget that reports used at the check after the `steps`-th step, whatever
 * work the slice defers. With zero steps it reports used at the first check, as
 * an idle deadline with no time left does, and the work still takes its one step.
 */
function stepBudget(steps: number): SliceBudget {
  let checks = 0
  return {
    defer: () => {},
    used() {
      checks += 1
      return checks >= steps
    },
  }
}

/**
 * Install a scheduler whose slices take `stepsPerSlice` steps each, and whose
 * requested slices wait until the test runs them. Outside a requested slice,
 * unlike the scheduler of the app, each call of `startSlice` starts a slice of
 * its own. Inside one, as in the app, `startSlice` returns the budget of the
 * slice that runs, so the work that it starts shares the slice. The global
 * `beforeEach` of `vitest.setup.ts` installs the unlimited scheduler again
 * before the next test.
 */
export function installControlledSlices(stepsPerSlice: number): ControlledSlices {
  // One entry per request, so that a cancel removes its own request even when
  // the work requests the same function twice.
  const queue: Array<{ slice: (budget: SliceBudget) => void }> = []
  let running: SliceBudget | undefined
  const runSlice = (): void => {
    const entry = queue.shift()
    if (entry === undefined)
      throw new Error('no slice waits to run')
    const budget = stepBudget(stepsPerSlice)
    running = budget
    try {
      entry.slice(budget)
    }
    finally {
      running = undefined
    }
  }
  const controlled: ControlledSlices = {
    startSlice: () => running ?? stepBudget(stepsPerSlice),
    requestSlice(slice) {
      const entry = { slice }
      queue.push(entry)
      return () => {
        const index = queue.indexOf(entry)
        if (index !== -1)
          queue.splice(index, 1)
      }
    },
    waiting: () => queue.length,
    runSlice,
    runAllSlices() {
      let ran = 0
      while (queue.length > 0) {
        if (ran >= MAX_SLICES_IN_ONE_RUN)
          throw new Error(`the work still requested slices after ${MAX_SLICES_IN_ONE_RUN} of them`)
        runSlice()
        ran += 1
      }
      return ran
    },
  }
  setSliceSchedulerForTests(controlled)
  return controlled
}
