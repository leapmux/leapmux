import type { DiffInput } from '~/components/chat/diff/diffTypes'
import type { DiffRequest } from '~/components/chat/diff/diffWorker'
import type { CapturingWorker } from '~/test-support/fakeWorker'
import { expect } from 'vitest'
import { answerDiffRequest } from '~/components/chat/diff/diffWorker'
import { installCapturingWorkers } from '~/test-support/fakeWorker'

// ---------------------------------------------------------------------------
// A diff worker whose replies a test controls, for a test that runs the real client
//
// Every jsdom test has a stand-in Worker that answers each diff request soon
// after the client posts it (see ~/test-support/fakeWorker). `installHeldDiffWorker`
// replaces it with one that keeps each request until the test answers it, so
// that the test can observe a diff while it is pending. The test then answers a
// request as the real worker would, through a structured clone, as `postMessage`
// delivers it.
//
// Import the client after the stub, and afresh (`vi.resetModules()`), so that it
// spawns the held worker and starts with no cache. Call `vi.unstubAllGlobals()`
// after each test.
// ---------------------------------------------------------------------------

/** A stand-in diff worker that keeps each request until the test answers it. */
export type HeldDiffWorker = CapturingWorker<DiffRequest>

/** Install a stand-in Worker, and list each one that the client spawns. */
export function installHeldDiffWorker(): HeldDiffWorker[] {
  return installCapturingWorkers<DiffRequest>()
}

/** The one worker that the client spawned. */
export function onlyHeldDiffWorker(spawned: readonly HeldDiffWorker[]): HeldDiffWorker {
  expect(spawned).toHaveLength(1)
  return spawned[0]!
}

/** Answer `request` as the real worker would, through a structured clone. */
function answerRequest(worker: HeldDiffWorker, request: DiffRequest): void {
  worker.reply(structuredClone(answerDiffRequest(request)))
}

/** Answer the request for `input` as the real worker would. */
export function answerHeldDiffRequest(worker: HeldDiffWorker, input: DiffInput): void {
  const request = worker.messages.find(candidate => candidate.input === input)
  if (request === undefined)
    throw new Error('expected a request for the input')
  answerRequest(worker, request)
}

/**
 * Let every promise settle: a reply passes through the worker client, the gate,
 * the cache and the version bump, each of which takes microtasks of its own.
 */
export async function settleDiffReplies(): Promise<void> {
  await new Promise(resolve => setTimeout(resolve, 0))
}

/**
 * Answer every request that the worker holds, then each request that the client
 * posts in reply, until the client posts no more. Fails when the client still
 * posts after `maxRounds` rounds, so a client that asks again forever fails the
 * test instead of hanging it.
 */
export async function answerHeldDiffRequestsUntilIdle(worker: HeldDiffWorker, maxRounds: number): Promise<void> {
  const answered = new Set<number>()
  for (let round = 0; round < maxRounds; round++) {
    const open = worker.messages.filter(request => !answered.has(request.id))
    if (open.length === 0)
      return
    for (const request of open) {
      answered.add(request.id)
      answerRequest(worker, request)
    }
    await settleDiffReplies()
  }
  throw new Error(`the client still posted diff requests after ${maxRounds} rounds`)
}
