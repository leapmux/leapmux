import type { DiffInput, StructuredPatchHunk } from '~/components/chat/diff/diffTypes'
import type { DiffRequest } from '~/components/chat/diff/diffWorker'
import type { HeldDiffWorker } from '~/test-support/heldDiffWorker'
import { describe, expect, it } from 'vitest'
import { answerDiffRequest } from '~/components/chat/diff/diffWorker'
import { CapturingWorker } from '~/test-support/fakeWorker'
import { answerHeldDiffRequest, answerHeldDiffRequestsUntilIdle } from '~/test-support/heldDiffWorker'

const TEXTS: DiffInput = { kind: 'texts', oldText: 'a\n', newText: 'b\n' }

/** A held worker, as a client spawns it, and each reply that the client receives from it. */
function heldWorker(): { worker: HeldDiffWorker, replies: unknown[] } {
  const worker = new CapturingWorker<DiffRequest>()
  const replies: unknown[] = []
  worker.onmessage = event => replies.push(event.data)
  return { worker, replies }
}

describe('answerHeldDiffRequest', () => {
  it('answers the request for the input as the real worker would, under the id of the request', () => {
    const { worker, replies } = heldWorker()
    const request: DiffRequest = { type: 'diff', id: 4, input: TEXTS }
    worker.postMessage({
      type: 'diff',
      id: 3,
      input: { kind: 'texts', oldText: 'x\n', newText: 'y\n' },
    })
    worker.postMessage(request)

    answerHeldDiffRequest(worker, TEXTS)

    expect(replies).toEqual([
      answerDiffRequest(request),
    ])
  })

  it('posts no hunks back for a patch, as the real worker does', () => {
    const { worker, replies } = heldWorker()
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-a', '+b'],
      },
    ]
    const input: DiffInput = { kind: 'hunks', hunks }
    worker.postMessage({ type: 'diff', id: 1, input })

    answerHeldDiffRequest(worker, input)

    expect((replies[0] as ReturnType<typeof answerDiffRequest>).diff.hunks).toBeNull()
  })

  it('finds the request by the identity of its input, and fails for an input that the worker holds no request for', () => {
    const { worker, replies } = heldWorker()
    worker.postMessage({ type: 'diff', id: 1, input: TEXTS })

    expect(() => answerHeldDiffRequest(worker, { ...TEXTS })).toThrow('expected a request for the input')
    expect(replies).toEqual([])
  })
})

describe('answerHeldDiffRequestsUntilIdle', () => {
  it('answers every request, and each one that the client posts in reply, until the client posts no more', async () => {
    const { worker, replies } = heldWorker()
    const followUp: DiffInput = { kind: 'texts', oldText: 'c\n', newText: 'd\n' }
    worker.postMessage({ type: 'diff', id: 1, input: TEXTS })
    worker.postMessage({ type: 'diff', id: 2, input: TEXTS })
    // The client posts one more request in reply to the first answer, as a
    // consumer that re-runs at a reply and asks for the next diff does.
    worker.onmessage = (event) => {
      replies.push(event.data)
      if (replies.length === 1)
        worker.postMessage({ type: 'diff', id: 3, input: followUp })
    }

    await answerHeldDiffRequestsUntilIdle(worker, 3)

    expect((replies as Array<{ id: number }>).map(reply => reply.id)).toEqual([1, 2, 3])
  })

  it('fails when the client still posts after the given rounds, rather than hanging the test', async () => {
    const { worker } = heldWorker()
    let nextId = 1
    worker.postMessage({ type: 'diff', id: nextId++, input: TEXTS })
    // A client that asks again at every answer.
    worker.onmessage = () => worker.postMessage({ type: 'diff', id: nextId++, input: TEXTS })

    await expect(answerHeldDiffRequestsUntilIdle(worker, 3)).rejects.toThrow('the client still posted diff requests after 3 rounds')
    expect(worker.messages).toHaveLength(4)
  })

  it('answers nothing, and resolves at once, for a worker that holds no request', async () => {
    const { worker, replies } = heldWorker()

    await answerHeldDiffRequestsUntilIdle(worker, 1)

    expect(replies).toEqual([])
  })
})
