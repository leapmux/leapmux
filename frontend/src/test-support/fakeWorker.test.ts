import type { CapturingWorker } from './fakeWorker'
import type { DiffRequest, DiffResponse } from '~/components/chat/diff/diffWorker'
import type { MarkdownRenderResponse } from '~/lib/markdownWorker'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { computeDiff } from '~/components/chat/diff/diffSearch'
import { answerDiffRequest } from '~/components/chat/diff/diffWorker'
import { requestComputedDiff } from '~/components/chat/diff/diffWorkerClient'
import { renderMarkdownInWorker } from '~/lib/markdownWorkerClient'
import { syntaxThemePair } from '~/lib/shikiThemes'
import { tokenizeAsync } from '~/lib/shikiWorkerClient'
import { AnsweringWorker, holdWorkerReplies, installCapturingWorkers, recordWorkerRequests, resetWorkerStandIns, settleWorkerReplies } from './fakeWorker'

const DIFF_WORKER_URL = new URL('../components/chat/diff/diffWorker.ts', import.meta.url)
const MARKDOWN_WORKER_URL = new URL('../lib/markdownWorker.ts', import.meta.url)

function diffRequest(id: number, oldText: string, newText: string): DiffRequest {
  return {
    type: 'diff',
    id,
    input: { kind: 'texts', oldText, newText },
  }
}

/** A stand-in for the module at `url`, and each reply that it delivers. */
function answeringWorker(url: URL): { worker: AnsweringWorker, replies: unknown[] } {
  const worker = new AnsweringWorker(url)
  const replies: unknown[] = []
  worker.onmessage = event => replies.push(event.data)
  return { worker, replies }
}

afterEach(() => {
  resetWorkerStandIns()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('AnsweringWorker', () => {
  it('is the global Worker of a jsdom test', () => {
    expect(globalThis.Worker).toBe(AnsweringWorker)
  })

  it('answers the diff client through the real diff worker', async () => {
    const input = { kind: 'texts' as const, oldText: 'a\nb\n', newText: 'a\nc\n' }

    expect(requestComputedDiff(input)).toBeUndefined()
    await settleWorkerReplies()

    expect(requestComputedDiff(input)).toEqual(computeDiff(input))
  })

  it('answers the Markdown client through the real Markdown worker', async () => {
    const result = await renderMarkdownInWorker('# Title', syntaxThemePair())

    expect(result?.html).toContain('<h1>Title</h1>')
  })

  it('answers the token client through the real Shiki worker', async () => {
    const tokens = await tokenizeAsync('typescript', 'const answer = 42')

    expect(tokens?.flat().map(token => token.content).join('')).toBe('const answer = 42')
    // Tokenized: the keyword is a token of its own.
    expect(tokens?.[0]?.[0]?.content).toBe('const')
  })

  it('answers each message with a structured clone of the reply, in post order', async () => {
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)
    const first = diffRequest(1, 'a\n', 'b\n')

    worker.postMessage(first)
    worker.postMessage(diffRequest(2, 'c\n', 'd\n'))
    await settleWorkerReplies()

    expect(replies.map(reply => (reply as DiffResponse).id)).toEqual([1, 2])
    expect((replies[0] as DiffResponse).diff).toEqual(computeDiff(first.input))
    expect(worker.messages[0]).toBe(first)
  })

  it('answers in post order while the first message waits for a grammar', async () => {
    const { worker, replies } = answeringWorker(MARKDOWN_WORKER_URL)
    const syntax = syntaxThemePair()

    worker.postMessage({
      type: 'render',
      id: 1,
      text: '```json\n{"a": 1}\n```',
      syntax,
    })
    worker.postMessage({
      type: 'render',
      id: 2,
      text: 'no fence',
      syntax,
    })
    await settleWorkerReplies()

    expect(replies.map(reply => (reply as MarkdownRenderResponse).id)).toEqual([1, 2])
  })

  it('answers nothing in the call that posts the message', () => {
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))

    expect(replies).toEqual([])
  })

  it('throws for a message that cannot be cloned, and records nothing', () => {
    const { worker } = answeringWorker(DIFF_WORKER_URL)

    expect(() => worker.postMessage({
      type: 'diff',
      id: 1,
      input: () => {},
    })).toThrow('could not be cloned')
    expect(worker.messages).toEqual([])
  })

  it('delivers nothing after it is terminated', async () => {
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    worker.terminate()
    await settleWorkerReplies()

    expect(replies).toEqual([])
    expect(worker.terminate).toHaveBeenCalledOnce()
  })

  it('fires onerror when the handler throws, and still answers the next message', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)
    const onerror = vi.fn()
    worker.onerror = onerror

    // A request without an input fails outside the handler's own fallback.
    worker.postMessage({ type: 'diff', id: 1 })
    worker.postMessage(diffRequest(2, 'a\n', 'b\n'))
    await settleWorkerReplies()

    expect(onerror).toHaveBeenCalledOnce()
    expect(error).toHaveBeenCalledOnce()
    expect(replies.map(reply => (reply as DiffResponse).id)).toEqual([2])
  })

  it('cannot start for a worker module that it does not answer', () => {
    expect(() => new AnsweringWorker('file:///nowhere/unknownWorker.ts'))
      .toThrow('no stand-in Worker answers the worker module file:///nowhere/unknownWorker.ts')
  })

  // The channel session worker holds the keys of every channel, and one command
  // can produce many events, so the tests host its endpoint in process instead.
  it('cannot start for the channel session worker, which does not answer one request with one reply', () => {
    const url = new URL('../lib/channelSessionWorker.ts', import.meta.url)

    expect(() => new AnsweringWorker(url)).toThrow(`no stand-in Worker answers the worker module ${url.href}`)
  })

  it('leaves the message handler of the window as it was after it loads a worker module', async () => {
    const { worker } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    await settleWorkerReplies()

    expect(globalThis.onmessage).toBeNull()
  })
})

describe('holdWorkerReplies', () => {
  it('keeps a reply from the client until the test releases it', async () => {
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)
    const request = diffRequest(1, 'a\n', 'b\n')

    worker.postMessage(request)
    await settleWorkerReplies()

    expect(replies).toEqual([])
    expect(hold.pending()).toBe(1)

    await hold.release()

    expect(replies).toEqual([
      answerDiffRequest(request),
    ])
    expect(hold.pending()).toBe(0)
  })

  it('counts a reply only once the handler computed it', async () => {
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    const { worker } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))

    expect(hold.pending()).toBe(0)
    await settleWorkerReplies()
    expect(hold.pending()).toBe(1)
  })

  it('releases the replies one by one, oldest first', async () => {
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)
    const ids = () => replies.map(reply => (reply as DiffResponse).id)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    worker.postMessage(diffRequest(2, 'c\n', 'd\n'))
    await settleWorkerReplies()

    await hold.releaseNext()
    expect(ids()).toEqual([1])
    expect(hold.pending()).toBe(1)

    await hold.releaseNext()
    expect(ids()).toEqual([1, 2])
    expect(hold.pending()).toBe(0)
  })

  it('keeps holding the later replies after one is released', async () => {
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    await hold.releaseNext()
    worker.postMessage(diffRequest(2, 'c\n', 'd\n'))
    await settleWorkerReplies()

    expect(replies.map(reply => (reply as DiffResponse).id)).toEqual([1])
    expect(hold.pending()).toBe(1)
  })

  it('waits for a reply that the handler still computes before it releases the next one', async () => {
    const hold = holdWorkerReplies('lib/markdownWorker.ts')
    const { worker, replies } = answeringWorker(MARKDOWN_WORKER_URL)

    // The fence makes the handler load a grammar first, which takes tasks of its own.
    worker.postMessage({
      type: 'render',
      id: 1,
      text: '```json\n{"a": 1}\n```',
      syntax: syntaxThemePair(),
    })
    await hold.releaseNext()

    expect(replies.map(reply => (reply as MarkdownRenderResponse).id)).toEqual([1])
  })

  it('rejects a release of the next reply when it holds none', async () => {
    const hold = holdWorkerReplies('lib/shikiWorker.ts')

    await expect(hold.releaseNext()).rejects.toThrow('no reply of lib/shikiWorker.ts is held')
  })

  it('delivers every later reply at once after the release', async () => {
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)

    await hold.release()
    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    await settleWorkerReplies()

    expect(replies.map(reply => (reply as DiffResponse).id)).toEqual([1])
    await expect(hold.releaseNext()).rejects.toThrow('the hold on the replies of components/chat/diff/diffWorker.ts has ended')
  })

  it('keeps the replies of the other worker modules flowing', async () => {
    holdWorkerReplies('lib/markdownWorker.ts')
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    await settleWorkerReplies()

    expect(replies.map(reply => (reply as DiffResponse).id)).toEqual([1])
  })

  it('keeps an error of the handler until the release', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    const { worker } = answeringWorker(DIFF_WORKER_URL)
    const onerror = vi.fn()
    worker.onerror = onerror

    // A request without an input fails outside the handler's own fallback.
    worker.postMessage({ type: 'diff', id: 1 })
    await settleWorkerReplies()

    expect(error).toHaveBeenCalledOnce()
    expect(onerror).not.toHaveBeenCalled()
    await hold.release()
    expect(onerror).toHaveBeenCalledOnce()
  })

  it('keeps a reply that the handler computed before the hold', async () => {
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    await settleWorkerReplies()

    expect(replies).toEqual([])
    expect(hold.pending()).toBe(1)
  })

  it('drops the replies of a stand-in that is terminated', async () => {
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    await settleWorkerReplies()
    worker.terminate()

    expect(hold.pending()).toBe(0)
    await hold.release()
    expect(replies).toEqual([])
  })

  it('keeps the replies of the other stand-ins of the module when one of them is terminated', async () => {
    const hold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    const terminated = answeringWorker(DIFF_WORKER_URL)
    const kept = answeringWorker(DIFF_WORKER_URL)

    // Two replies of the terminated stand-in, side by side, then one of the other.
    terminated.worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    terminated.worker.postMessage(diffRequest(2, 'c\n', 'd\n'))
    await settleWorkerReplies()
    kept.worker.postMessage(diffRequest(3, 'e\n', 'f\n'))
    await settleWorkerReplies()
    expect(hold.pending()).toBe(3)
    terminated.worker.terminate()

    expect(hold.pending()).toBe(1)
    await hold.release()
    expect(terminated.replies).toEqual([])
    expect(kept.replies.map(reply => (reply as DiffResponse).id)).toEqual([3])
  })

  it('holds the replies of the real client, which waits until the release', async () => {
    const hold = holdWorkerReplies('lib/shikiWorker.ts')
    let tokens: Awaited<ReturnType<typeof tokenizeAsync>> | undefined

    void tokenizeAsync('typescript', 'const held = 1').then((answer) => {
      tokens = answer
    })
    await settleWorkerReplies()

    expect(tokens).toBeUndefined()
    expect(hold.pending()).toBe(1)
    await hold.release()
    expect(tokens?.[0]?.[0]?.content).toBe('const')
  })

  it('refuses a second hold on the same worker module', () => {
    holdWorkerReplies('lib/fieldDigestWorker.ts')

    expect(() => holdWorkerReplies('lib/fieldDigestWorker.ts'))
      .toThrow('the replies of lib/fieldDigestWorker.ts are held already')
  })
})

describe('recordWorkerRequests', () => {
  it('records each request to the module as the client posted it, in post order', () => {
    const requests = recordWorkerRequests('components/chat/diff/diffWorker.ts')
    const { worker } = answeringWorker(DIFF_WORKER_URL)
    const first = diffRequest(1, 'a\n', 'b\n')
    const second = diffRequest(2, 'c\n', 'd\n')

    worker.postMessage(first)
    worker.postMessage(second)

    expect(requests).toHaveLength(2)
    expect(requests[0]).toBe(first)
    expect(requests[1]).toBe(second)
  })

  it('records nothing that a client posts before the record starts', () => {
    const { worker } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    const requests = recordWorkerRequests('components/chat/diff/diffWorker.ts')

    expect(requests).toEqual([])
  })

  it('records only the requests to its own module', () => {
    const requests = recordWorkerRequests('lib/markdownWorker.ts')
    const { worker } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))

    expect(requests).toEqual([])
  })

  it('records the requests of the real client', () => {
    const requests = recordWorkerRequests('lib/shikiWorker.ts')

    void tokenizeAsync('typescript', 'const recorded = 1')

    expect(requests.map(({ lang, code }) => [lang, code])).toEqual([
      ['typescript', 'const recorded = 1'],
    ])
  })

  it('records no message that cannot be cloned', () => {
    const requests = recordWorkerRequests('components/chat/diff/diffWorker.ts')
    const { worker } = answeringWorker(DIFF_WORKER_URL)

    expect(() => worker.postMessage({
      type: 'diff',
      id: 1,
      input: () => {},
    })).toThrow('could not be cloned')
    expect(requests).toEqual([])
  })

  it('refuses a second record of the same worker module', () => {
    recordWorkerRequests('lib/shikiWorker.ts')

    expect(() => recordWorkerRequests('lib/shikiWorker.ts'))
      .toThrow('the requests to lib/shikiWorker.ts are recorded already')
  })
})

describe('resetWorkerStandIns', () => {
  it('stops every record', () => {
    const requests = recordWorkerRequests('components/chat/diff/diffWorker.ts')
    const { worker } = answeringWorker(DIFF_WORKER_URL)

    resetWorkerStandIns()
    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))

    expect(requests).toEqual([])
    expect(() => recordWorkerRequests('components/chat/diff/diffWorker.ts')).not.toThrow()
  })

  it('delivers every kept reply and ends every hold', async () => {
    const diffHold = holdWorkerReplies('components/chat/diff/diffWorker.ts')
    holdWorkerReplies('lib/markdownWorker.ts')
    const { worker, replies } = answeringWorker(DIFF_WORKER_URL)

    worker.postMessage(diffRequest(1, 'a\n', 'b\n'))
    await settleWorkerReplies()
    resetWorkerStandIns()

    expect(replies.map(reply => (reply as DiffResponse).id)).toEqual([1])
    expect(diffHold.pending()).toBe(0)
    // Each module accepts a new hold: the old ones ended.
    expect(() => holdWorkerReplies('components/chat/diff/diffWorker.ts')).not.toThrow()
    expect(() => holdWorkerReplies('lib/markdownWorker.ts')).not.toThrow()
  })
})

describe('settleWorkerReplies', () => {
  it('resolves at once when no message is posted', async () => {
    await expect(settleWorkerReplies()).resolves.toBeUndefined()
  })

  it('resolves under fake timers once the reply landed', async () => {
    vi.useFakeTimers()
    try {
      const input = { kind: 'texts' as const, oldText: 'fake\n', newText: 'timers\n' }

      expect(requestComputedDiff(input)).toBeUndefined()
      await settleWorkerReplies()

      expect(requestComputedDiff(input)).toEqual(computeDiff(input))
    }
    finally {
      vi.useRealTimers()
    }
  })
})

describe('installCapturingWorkers', () => {
  it('records each spawned worker and each message, and replies only when the test does', () => {
    const spawned = installCapturingWorkers<{ id: number }>()
    const worker = new Worker('file:///any.ts') as unknown as CapturingWorker<{ id: number }>
    const onmessage = vi.fn()
    worker.onmessage = onmessage

    worker.postMessage({ id: 7 })
    expect(onmessage).not.toHaveBeenCalled()
    worker.reply({ id: 7, value: 'answer' })

    expect(spawned).toEqual([worker])
    expect(worker.messages).toEqual([
      { id: 7 },
    ])
    expect(onmessage).toHaveBeenCalledWith({
      data: { id: 7, value: 'answer' },
    })
  })

  it('gives the global Worker back to the answering stand-in when the test unstubs it', () => {
    installCapturingWorkers()
    vi.unstubAllGlobals()

    expect(globalThis.Worker).toBe(AnsweringWorker)
  })
})
