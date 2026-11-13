import { expect } from 'vitest'
import { _resetMarkdownCache, requestMarkdownHtml } from '~/lib/renderMarkdown'
import { holdWorkerReplies, recordWorkerRequests } from '~/test-support/fakeWorker'

// ---------------------------------------------------------------------------
// The order in which the Markdown worker receives renders
//
// The priority gate of a Markdown lane posts two renders to its worker at most,
// and keeps every later render until a reply frees a slot. It then posts the
// renders that a reader needs now before the renders of offscreen rows (see
// createWorkerPriorityGate). A test that states the priority of a render fills
// both slots of the standard lane first, then reads the order in which the
// worker receives the later renders.
// ---------------------------------------------------------------------------

/** The two bodies that take both slots of the gate. Each is short, so it renders in the standard lane. */
const SLOT_BODIES = [
  'The first body that takes a slot of the gate.',
  'The second body that takes a slot of the gate.',
]

/** A Markdown gate whose slots two bodies take, while the worker's replies wait. */
export interface FilledMarkdownGate {
  /**
   * Deliver every reply of the Markdown worker, and resolve with the text of
   * each later render that the worker received, in the order that the gate
   * posted them.
   */
  release: () => Promise<string[]>
}

/**
 * Empty the Markdown cache, hold the replies of the Markdown worker, and render
 * two bodies that a reader needs now, which take both slots of the gate of the
 * standard lane. Each later render then waits in the gate until `release`.
 */
export function fillMarkdownRenderGate(): FilledMarkdownGate {
  _resetMarkdownCache()
  const hold = holdWorkerReplies('lib/markdownWorker.ts')
  const requests = recordWorkerRequests('lib/markdownWorker.ts')
  for (const text of SLOT_BODIES)
    requestMarkdownHtml(text)
  // Both bodies reach the worker at once, so both slots were free, and now
  // both are taken.
  expect(requests.map(request => request.text)).toEqual(SLOT_BODIES)
  return {
    release: async () => {
      await hold.release()
      return requests.slice(SLOT_BODIES.length).map(request => request.text)
    },
  }
}
