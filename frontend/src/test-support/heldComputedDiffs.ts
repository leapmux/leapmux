import type { DiffInput } from '~/components/chat/diff/diffCompute'
import type * as DiffWorkerClient from '~/components/chat/diff/diffWorkerClient'
import { createSignal } from 'solid-js'
import { vi } from 'vitest'

// ---------------------------------------------------------------------------
// A diff worker client whose diffs a test holds pending
//
// jsdom has no Worker, so the real client computes each diff on the calling
// thread and a diff is never pending. A test that needs the pending state mocks
// the client with this module:
//
//   vi.mock('~/components/chat/diff/diffWorkerClient', async () =>
//     (await import('~/test-support/heldComputedDiffs')).heldDiffWorkerClient())
//
// While `holdComputedDiffs(true)` holds them, every diff answers as the worker's
// would before it replies. Releasing them re-runs each memo that read one, as a
// reply from the worker does. `requestedDiffInputs` lists what the code under
// test asked the worker for. Call `resetHeldComputedDiffs` after each test.
// ---------------------------------------------------------------------------

const [held, setHeld] = createSignal(false)
const requested: DiffInput[] = []

/** Hold every diff pending, or release them. */
export function holdComputedDiffs(hold: boolean): void {
  setHeld(hold)
}

/** Each input that `requestComputedDiff` received, in order. */
export function requestedDiffInputs(): readonly DiffInput[] {
  return requested
}

/** Release every diff and forget each request. */
export function resetHeldComputedDiffs(): void {
  setHeld(false)
  requested.length = 0
}

/** The mocked client: the real one, with each diff held while the test holds them. */
export async function heldDiffWorkerClient(): Promise<typeof DiffWorkerClient> {
  const actual = await vi.importActual<typeof DiffWorkerClient>('~/components/chat/diff/diffWorkerClient')
  return {
    ...actual,
    requestComputedDiff: (input, isLowPriority) => {
      requested.push(input)
      return held()
        ? undefined
        : actual.requestComputedDiff(input, isLowPriority)
    },
    computedDiffIfReady: input => held()
      ? undefined
      : actual.computedDiffIfReady(input),
  }
}
