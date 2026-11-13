import type { DiffInput } from '~/components/chat/diff/diffTypes'
import type * as DiffWorkerClient from '~/components/chat/diff/diffWorkerClient'
import { createSignal } from 'solid-js'
import { vi } from 'vitest'
import { diffWithoutSearch } from '~/components/chat/diff/diffCompute'

// ---------------------------------------------------------------------------
// A diff worker client whose diffs a test holds pending
//
// The stand-in Worker of the jsdom tests answers each diff soon after the client
// asks (see ~/test-support/fakeWorker), so a test cannot keep a diff pending for
// as long as it needs. A test that needs the pending state mocks the client with
// this module:
//
//   vi.mock('~/components/chat/diff/diffWorkerClient', async () =>
//     (await import('~/test-support/heldComputedDiffs')).heldDiffWorkerClient())
//
// While `holdComputedDiffs(true)` holds them, each diff answers as the worker's
// would before it replies, unless this test already received it (the real cache
// keeps a diff that arrived) or it needs no search (the real client answers it
// at once, see diffWithoutSearch). As in the real client, only a consumer that
// waits depends on the release, so releasing the diffs re-runs each memo that
// waits for one, as a reply from the worker does, and no other memo. `requestedDiffInputs`
// lists what the code under test asked the worker for. Call
// `resetHeldComputedDiffs` after each test.
//
// A test that answers the requests of the real client by hand uses
// ~/test-support/heldDiffWorker instead.
// ---------------------------------------------------------------------------

let held = false
const [releases, setReleases] = createSignal(0)
const requested: DiffInput[] = []
/** The key of each diff that this test received, which the real cache would keep. */
const received = new Set<string>()

/** Hold every diff that has not arrived pending, or release them. */
export function holdComputedDiffs(hold: boolean): void {
  held = hold
  if (!hold)
    setReleases(count => count + 1)
}

/** Each input that `requestComputedDiff` received, in order. */
export function requestedDiffInputs(): readonly DiffInput[] {
  return requested
}

/** Release every diff, and forget each request and each diff that arrived. */
export function resetHeldComputedDiffs(): void {
  held = false
  requested.length = 0
  received.clear()
}

/** The mocked client: the real one, with each diff held while the test holds them. */
export async function heldDiffWorkerClient(): Promise<typeof DiffWorkerClient> {
  const actual = await vi.importActual<typeof DiffWorkerClient>('~/components/chat/diff/diffWorkerClient')
  return {
    ...actual,
    requestComputedDiff: (input, isLowPriority) => {
      requested.push(input)
      const key = actual.diffInputKey(input)
      if (held && !received.has(key) && diffWithoutSearch(input) === undefined) {
        // A pending consumer depends on the release, as a real one depends on the
        // worker's replies.
        releases()
        return undefined
      }
      const diff = actual.requestComputedDiff(input, isLowPriority)
      // Only a diff that arrived stays ready: one that the worker still computes
      // is pending, so a hold that starts before it arrives holds it too.
      if (diff !== undefined)
        received.add(key)
      return diff
    },
  }
}
