import type { StructuredPatchHunk } from './diffTypes'
import type { DiffRequest } from './diffWorker'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fallbackDiff } from './diffCompute'
import { computeDiff } from './diffSearch'
import { answerDiffRequest } from './diffWorker'

/** When set, computeDiff throws the error a too-deep recursion throws. */
const failure = vi.hoisted(() => ({ throwOnCompute: false }))

vi.mock('./diffSearch', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./diffSearch')>()
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
    vi.restoreAllMocks()
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
    vi.spyOn(console, 'warn').mockImplementation(() => {})

    expect(answerDiffRequest(request)).toEqual({
      type: 'diff-result',
      id: 7,
      diff: fallbackDiff(request.input),
    })
  })

  it('logs the error that made the computation fail', () => {
    failure.throwOnCompute = true
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})

    answerDiffRequest(request)

    expect(warn).toHaveBeenCalledExactlyOnceWith(expect.stringContaining('[diffWorker]'), expect.objectContaining({
      name: 'RangeError',
      message: 'Maximum call stack size exceeded',
    }))
  })

  it('posts no hunks back for a patch, whose hunks the client holds', () => {
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-let a', '+let b'],
      },
    ]

    const response = answerDiffRequest({
      type: 'diff',
      id: 3,
      input: { kind: 'hunks', hunks },
    })

    expect(response.diff.hunks).toBeNull()
    expect(response.diff.wordDiffs).toEqual(computeDiff({ kind: 'hunks', hunks }).wordDiffs)
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
