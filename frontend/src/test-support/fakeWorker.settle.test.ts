import { describe, expect, it, vi } from 'vitest'
import { AnsweringWorker, settleWorkerReplies } from './fakeWorker'

// ---------------------------------------------------------------------------
// settleWorkerReplies with a handler that the test times
//
// The diff worker's handler here answers after `turns` timer turns. A reply then
// stays pending past the timer turn that ends one round of the wait, which no real
// handler does in a way that a test can control.
// ---------------------------------------------------------------------------

interface TimedRequest {
  id: number
  turns: number
}

vi.mock('~/components/chat/diff/diffWorker', () => ({
  answerDiffRequest: async ({ id, turns }: TimedRequest) => {
    for (let turn = 0; turn < turns; turn++)
      await new Promise(resolve => setTimeout(resolve, 0))
    return { id }
  },
}))

const DIFF_WORKER_URL = new URL('../components/chat/diff/diffWorker.ts', import.meta.url)

describe('settleWorkerReplies', () => {
  it('waits for a message that a client posts in reply to an answer', async () => {
    const worker = new AnsweringWorker(DIFF_WORKER_URL)
    const replies: number[] = []
    worker.onmessage = (event) => {
      const { id } = event.data as TimedRequest
      replies.push(id)
      if (id === 1)
        worker.postMessage({ id: 2, turns: 2 } satisfies TimedRequest)
    }

    worker.postMessage({ id: 1, turns: 0 } satisfies TimedRequest)
    await settleWorkerReplies()

    expect(replies).toEqual([1, 2])
  })
})
