import type { DiffRequest } from './diffWorker'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { computeDiff, fallbackDiff } from './diffCompute'
import { answerDiffRequest } from './diffWorker'

/** When set, computeDiff throws the error a too-deep recursion throws. */
const failure = vi.hoisted(() => ({ throwOnCompute: false }))

vi.mock('./diffCompute', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./diffCompute')>()
  return {
    ...actual,
    computeDiff: (input: Parameters<typeof actual.computeDiff>[0]) => {
      if (failure.throwOnCompute)
        throw new RangeError('Maximum call stack size exceeded')
      return actual.computeDiff(input)
    },
  }
})

const request: DiffRequest = {
  type: 'diff',
  id: 7,
  input: { kind: 'texts', oldText: 'a\nb\n', newText: 'a\nc\n' },
}

describe('answerDiffRequest', () => {
  afterEach(() => {
    failure.throwOnCompute = false
  })

  it('answers the computed diff under the request id', () => {
    expect(answerDiffRequest(request)).toEqual({
      type: 'diff-result',
      id: 7,
      diff: computeDiff(request.input),
    })
  })

  it('answers the fallback diff when the computation throws', () => {
    failure.throwOnCompute = true

    expect(answerDiffRequest(request)).toEqual({
      type: 'diff-result',
      id: 7,
      diff: fallbackDiff(request.input),
    })
  })
})

describe('diffWorker onmessage', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('posts the answer to each message back to the client', () => {
    const postMessage = vi.fn()
    vi.stubGlobal('postMessage', postMessage)

    const handler = globalThis.onmessage
    expect(handler).toBeTypeOf('function')
    // The handler reads no `this`; its type asks for a Window, which a worker scope is not.
    Reflect.apply(handler!, globalThis, [
      new MessageEvent('message', { data: request }),
    ])

    expect(postMessage).toHaveBeenCalledExactlyOnceWith(answerDiffRequest(request))
  })
})
