import type { DiffInput, StructuredPatchHunk, TextsDiffInput } from './diffTypes'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { WORKER_COLD_START_ALLOWANCE_MS } from '~/lib/workerClient'
import {
  answerHeldDiffRequest,
  answerHeldDiffRequestsUntilIdle,
  installHeldDiffWorker,
  onlyHeldDiffWorker,
  settleDiffReplies,
} from '~/test-support/heldDiffWorker'
import { DIFF_DRAWN_LINE_LIMIT } from './diffBuilder'
import { fallbackDiff, LINE_DIFF_MAX_EDITS } from './diffCompute'
import { computeDiff } from './diffSearch'

type ClientModule = typeof import('./diffWorkerClient')

/** Import the client afresh, so that it spawns from this test's Worker and starts with no cache. */
async function loadClient(): Promise<ClientModule> {
  vi.resetModules()
  return import('./diffWorkerClient')
}

/**
 * The client and Solid, imported afresh together. After the reset, the client
 * reads its signal from a fresh copy of Solid, and a memo must come from that
 * copy too, or it cannot track the signal.
 */
async function loadClientWithSolid(): Promise<ClientModule & typeof import('solid-js')> {
  const client = await loadClient()
  return {
    ...client,
    ...await import('solid-js'),
  }
}

function texts(oldText: string, newText: string): TextsDiffInput {
  return { kind: 'texts', oldText, newText }
}

/** `count` distinct pairs of texts, each with one changed line. */
function distinctTexts(count: number): DiffInput[] {
  return Array.from({ length: count }, (_, i) => texts(`old ${i}\n`, `new ${i}\n`))
}

describe('requestComputedDiff', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('draws the fallback diff where no Worker exists, as for a worker that cannot start', async () => {
    vi.stubGlobal('Worker', undefined)
    const { requestComputedDiff } = await loadClient()
    const input = texts('a\n', 'b\n')

    // The calling thread never runs the search, even where no worker can.
    expect(requestComputedDiff(input)).toBeUndefined()
    await settleDiffReplies()

    expect(requestComputedDiff(input)).toEqual(fallbackDiff(input))
  })

  it('answers a diff that needs no search at once, and asks the worker nothing', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()

    for (const input of [
      texts('', 'a\nb\n'),
      texts('a\nb\n', ''),
      texts('same\n', 'same\n'),
      texts('', ''),
    ])
      expect(requestComputedDiff(input)).toEqual(computeDiff(input))
    await settleDiffReplies()

    expect(spawned.flatMap(worker => worker.messages)).toEqual([])
  })

  it('re-runs no consumer of a diff that needs no search when another diff lands', async () => {
    const spawned = installHeldDiffWorker()
    const { createMemo, createRoot, requestComputedDiff } = await loadClientWithSolid()
    const created = texts('', 'a\n')
    const [other] = distinctTexts(1) as [DiffInput]

    await createRoot(async (dispose) => {
      let runs = 0
      const diff = createMemo(() => {
        runs++
        return requestComputedDiff(created)
      })
      expect(diff()).toEqual(computeDiff(created))

      requestComputedDiff(other)
      await settleDiffReplies()
      answerHeldDiffRequest(onlyHeldDiffWorker(spawned), other)
      await settleDiffReplies()

      expect(runs).toBe(1)
      dispose()
    })
  })

  it('answers undefined until the worker replies, then re-runs the memo that read it', async () => {
    const spawned = installHeldDiffWorker()
    const { createMemo, createRoot, requestComputedDiff } = await loadClientWithSolid()
    const input = texts('a\n', 'b\n')

    await createRoot(async (dispose) => {
      const diff = createMemo(() => requestComputedDiff(input))
      expect(diff()).toBeUndefined()

      answerHeldDiffRequest(onlyHeldDiffWorker(spawned), input)
      await settleDiffReplies()

      expect(diff()).toEqual(computeDiff(input))
      dispose()
    })
  })

  it('asks the worker once for equal content, and serves the cache after it replies', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const input = texts('a\n', 'b\n')

    expect(requestComputedDiff(input)).toBeUndefined()
    expect(requestComputedDiff(texts('a\n', 'b\n'))).toBeUndefined()
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)
    expect(worker.messages).toHaveLength(1)

    answerHeldDiffRequest(worker, input)
    await settleDiffReplies()

    expect(requestComputedDiff(texts('a\n', 'b\n'))).toEqual(computeDiff(input))
    expect(worker.messages).toHaveLength(1)
  })

  it('draws the fallback diff when the worker crashes, and does not ask again', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const input = texts('a\n', 'b\n')

    requestComputedDiff(input)
    await settleDiffReplies()
    onlyHeldDiffWorker(spawned).onerror?.()
    await settleDiffReplies()

    expect(requestComputedDiff(input)).toEqual(fallbackDiff(input))
    expect(spawned).toHaveLength(1)
  })

  it('draws the fallback diff for a reply to two texts that holds no hunks, and does not ask again', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const input = texts('a\n', 'b\n')

    requestComputedDiff(input)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)
    const [request] = worker.messages
    // The worker posts the hunks of every pair of texts, so this reply is broken.
    worker.reply({
      type: 'diff-result',
      id: request!.id,
      diff: {
        ...computeDiff(input),
        hunks: null,
      },
    })
    await settleDiffReplies()

    expect(requestComputedDiff(input)).toEqual(fallbackDiff(input))
    expect(worker.messages).toHaveLength(1)
  })

  it('draws the fallback diff once the worker runs past the time budget of the input, and only then', async () => {
    vi.useFakeTimers()
    try {
      const spawned = installHeldDiffWorker()
      const { diffTimeBudgetMs, requestComputedDiff } = await loadClient()
      const input = texts('a\n', 'b\n')
      // A worker that has answered nothing yet also gets the cold-start allowance.
      const budget = diffTimeBudgetMs(input) + WORKER_COLD_START_ALLOWANCE_MS

      requestComputedDiff(input)
      await vi.advanceTimersByTimeAsync(0)
      const worker = onlyHeldDiffWorker(spawned)
      expect(worker.messages).toHaveLength(1)

      await vi.advanceTimersByTimeAsync(budget - 1)
      expect(requestComputedDiff(input)).toBeUndefined()
      expect(worker.terminate).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(1)
      expect(requestComputedDiff(input)).toEqual(fallbackDiff(input))
      expect(worker.terminate).toHaveBeenCalledOnce()
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('re-runs no consumer whose diff is ready when another diff lands', async () => {
    const spawned = installHeldDiffWorker()
    const { createMemo, createRoot, requestComputedDiff } = await loadClientWithSolid()
    const [first, second] = distinctTexts(2) as [DiffInput, DiffInput]

    await createRoot(async (dispose) => {
      let firstRuns = 0
      const firstDiff = createMemo(() => {
        firstRuns++
        return requestComputedDiff(first)
      })
      answerHeldDiffRequest(onlyHeldDiffWorker(spawned), first)
      await settleDiffReplies()
      expect(firstDiff()).toEqual(computeDiff(first))
      const runsWhenReady = firstRuns

      const secondDiff = createMemo(() => requestComputedDiff(second))
      await settleDiffReplies()
      answerHeldDiffRequest(onlyHeldDiffWorker(spawned), second)
      await settleDiffReplies()

      expect(secondDiff()).toEqual(computeDiff(second))
      expect(firstRuns).toBe(runsWhenReady)
      dispose()
    })
  })

  it('asks the worker once for each of more mounted diffs than the cache holds', async () => {
    const spawned = installHeldDiffWorker()
    const { createMemo, createRoot, DIFF_CACHE_MAX_ENTRIES, requestComputedDiff } = await loadClientWithSolid()
    const inputs = distinctTexts(DIFF_CACHE_MAX_ENTRIES + 44)

    await createRoot(async (dispose) => {
      const diffs = inputs.map(input => createMemo(() => requestComputedDiff(input)))
      await settleDiffReplies()
      const worker = onlyHeldDiffWorker(spawned)

      await answerHeldDiffRequestsUntilIdle(worker, inputs.length * 2)

      expect(worker.messages).toHaveLength(inputs.length)
      expect(diffs.filter(diff => diff() === undefined)).toHaveLength(0)
      dispose()
    })
  })

  it('keeps every diff that one consumer reads, past the cache size', async () => {
    // As the Copy text of a row with many file changes does: one memo reads them all.
    const spawned = installHeldDiffWorker()
    const { createMemo, createRoot, DIFF_CACHE_MAX_ENTRIES, requestComputedDiff } = await loadClientWithSolid()
    const inputs = distinctTexts(DIFF_CACHE_MAX_ENTRIES + 44)

    await createRoot(async (dispose) => {
      const diffs = createMemo(() => inputs.map(input => requestComputedDiff(input, () => true)))
      await settleDiffReplies()
      const worker = onlyHeldDiffWorker(spawned)

      await answerHeldDiffRequestsUntilIdle(worker, inputs.length * 2)

      expect(worker.messages).toHaveLength(inputs.length)
      expect(diffs().filter(diff => diff === undefined)).toHaveLength(0)
      dispose()
    })
  })

  it('reads the texts of one input object once while its diff waits', async () => {
    installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    let oldTextReads = 0
    const input: DiffInput = {
      kind: 'texts',
      get oldText() {
        oldTextReads++
        return 'a\n'
      },
      newText: 'b\n',
    }

    expect(requestComputedDiff(input)).toBeUndefined()
    const readsOfFirstRequest = oldTextReads
    expect(requestComputedDiff(input)).toBeUndefined()
    expect(requestComputedDiff(input)).toBeUndefined()

    expect(oldTextReads).toBe(readsOfFirstRequest)
  })

  it('keeps apart two inputs whose texts have the same length and digest', async () => {
    // FNV-1a maps both of these strings to one digest.
    const first = texts('x0032vu', 'same')
    const second = texts('x00auea', 'same')
    const spawned = installHeldDiffWorker()
    const { diffInputKey, requestComputedDiff } = await loadClient()
    expect(diffInputKey(first)).toBe(diffInputKey(second))

    requestComputedDiff(first)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)
    answerHeldDiffRequest(worker, first)
    await settleDiffReplies()

    // The cache holds the first diff under the shared key, and the second input must
    // not take it.
    expect(requestComputedDiff(second)).toBeUndefined()
    await settleDiffReplies()
    answerHeldDiffRequest(worker, second)
    await settleDiffReplies()

    expect(worker.messages).toHaveLength(2)
    expect(requestComputedDiff(first)).toEqual(computeDiff(first))
    expect(requestComputedDiff(second)).toEqual(computeDiff(second))
    expect(requestComputedDiff(texts('x00auea', 'same'))).toEqual(computeDiff(second))
  })

  it('serves no cached diff to an input with another line limit', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const chatRow = texts('a 1\nb 1\n', 'a 2\nb 2\n')
    const viewer: DiffInput = { ...chatRow, drawnLineLimit: Number.POSITIVE_INFINITY }

    requestComputedDiff(chatRow)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)
    answerHeldDiffRequest(worker, chatRow)
    await settleDiffReplies()

    expect(requestComputedDiff(viewer)).toBeUndefined()
    await settleDiffReplies()
    answerHeldDiffRequest(worker, viewer)
    await settleDiffReplies()

    expect(worker.messages).toHaveLength(2)
    expect(requestComputedDiff(viewer)).toEqual(computeDiff(viewer))
    expect(requestComputedDiff(texts('a 1\nb 1\n', 'a 2\nb 2\n'))).toEqual(computeDiff(chatRow))
  })

  it('serves no cached diff to an input with another line edit limit', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const chatRow = texts('a 1\nb 1\n', 'a 2\nb 2\n')
    const viewer: DiffInput = { ...chatRow, maxLineEdits: 5000 }

    requestComputedDiff(chatRow)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)
    answerHeldDiffRequest(worker, chatRow)
    await settleDiffReplies()

    expect(requestComputedDiff(viewer)).toBeUndefined()
    await settleDiffReplies()
    answerHeldDiffRequest(worker, viewer)
    await settleDiffReplies()

    expect(worker.messages).toHaveLength(2)
    expect(requestComputedDiff(viewer)).toEqual(computeDiff(viewer))
  })

  it('serves the cached diff of an input that states no limit to one that states the defaults', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const absent = texts('a 1\nb 1\n', 'a 2\nb 2\n')

    requestComputedDiff(absent)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)
    answerHeldDiffRequest(worker, absent)
    await settleDiffReplies()

    expect(requestComputedDiff({
      ...absent,
      drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
      maxLineEdits: LINE_DIFF_MAX_EDITS,
    })).toEqual(computeDiff(absent))
    expect(worker.messages).toHaveLength(1)
  })

  it('keeps the hunks of a patch that it sent, rather than the copy that the worker posts back', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const hunks: StructuredPatchHunk[] = [
      {
        oldStart: 1,
        oldLines: 1,
        newStart: 1,
        newLines: 1,
        lines: ['-let a', '+let b'],
      },
    ]
    const input: DiffInput = { kind: 'hunks', hunks }

    requestComputedDiff(input)
    await settleDiffReplies()
    answerHeldDiffRequest(onlyHeldDiffWorker(spawned), input)
    await settleDiffReplies()

    const diff = requestComputedDiff(input)
    expect(diff?.hunks).toBe(hunks)
    expect(diff).toEqual(computeDiff(input))
  })

  it('serves the cached diff of a patch to an equal patch in other objects, and asks the worker once', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const patchOf = (): DiffInput => ({
      kind: 'hunks',
      hunks: [
        {
          oldStart: 1,
          oldLines: 1,
          newStart: 1,
          newLines: 1,
          lines: ['-let a', '+let b'],
        },
        {
          oldStart: 9,
          oldLines: 0,
          newStart: 9,
          newLines: 1,
          lines: ['+let c'],
        },
      ],
    })
    const input = patchOf()

    requestComputedDiff(input)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)
    answerHeldDiffRequest(worker, input)
    await settleDiffReplies()

    expect(requestComputedDiff(patchOf())).toEqual(computeDiff(input))
    expect(worker.messages).toHaveLength(1)
  })

  it('posts an urgent diff ahead of the low-priority diffs that wait at the gate', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const low = () => true
    const inputs = [
      texts('1', '2'),
      texts('3', '4'),
      texts('5', '6'),
    ]
    const urgent = texts('7', '8')

    for (const input of inputs)
      requestComputedDiff(input, low)
    requestComputedDiff(urgent)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)
    // The gate posts two at a time and holds the rest.
    expect(worker.messages.map(request => request.input)).toEqual([inputs[0], inputs[1]])

    answerHeldDiffRequest(worker, inputs[0]!)
    await settleDiffReplies()

    expect(worker.messages.map(request => request.input)).toEqual([inputs[0], inputs[1], urgent])
  })

  it('makes a low-priority diff urgent when a consumer with no priority asks for it', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const low = () => true
    const inputs = [
      texts('1', '2'),
      texts('3', '4'),
      texts('5', '6'),
      texts('7', '8'),
    ]

    for (const input of inputs)
      requestComputedDiff(input, low)
    requestComputedDiff(inputs[3]!)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)

    answerHeldDiffRequest(worker, inputs[0]!)
    await settleDiffReplies()

    expect(worker.messages.map(request => request.input)).toEqual([inputs[0], inputs[1], inputs[3]])
  })

  it('follows the priority of the latest consumer while the diff waits', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const low = () => true
    const inputs = [
      texts('1', '2'),
      texts('3', '4'),
      texts('5', '6'),
      texts('7', '8'),
    ]

    for (const input of inputs)
      requestComputedDiff(input, low)
    // The row of the last diff scrolled into the viewport.
    requestComputedDiff(inputs[3]!, () => false)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)

    answerHeldDiffRequest(worker, inputs[0]!)
    await settleDiffReplies()

    expect(worker.messages.map(request => request.input)).toEqual([inputs[0], inputs[1], inputs[3]])
  })

  it('keeps a diff that a visible row waits for urgent after an offscreen row asks for it too', async () => {
    const spawned = installHeldDiffWorker()
    const { requestComputedDiff } = await loadClient()
    const low = () => true
    const inputs = [
      texts('1', '2'),
      texts('3', '4'),
      texts('5', '6'),
      texts('7', '8'),
    ]

    for (const input of inputs.slice(0, 3))
      requestComputedDiff(input, low)
    // Two rows draw the same edit: the visible one asks first, the offscreen one last.
    requestComputedDiff(inputs[3]!, () => false)
    requestComputedDiff(inputs[3]!, low)
    await settleDiffReplies()
    const worker = onlyHeldDiffWorker(spawned)

    answerHeldDiffRequest(worker, inputs[0]!)
    await settleDiffReplies()

    expect(worker.messages.map(request => request.input)).toEqual([inputs[0], inputs[1], inputs[3]])
  })
})

describe('diffTimeBudgetMs', () => {
  it('gives the base budget to an empty input', async () => {
    const { DIFF_BUDGET_BASE_MS, diffTimeBudgetMs } = await loadClient()

    expect(diffTimeBudgetMs(texts('', ''))).toBe(DIFF_BUDGET_BASE_MS)
    expect(diffTimeBudgetMs({ kind: 'hunks', hunks: [] })).toBe(DIFF_BUDGET_BASE_MS)
  })

  it('adds the per-KiB budget for each started KiB of both texts', async () => {
    const { DIFF_BUDGET_BASE_MS, DIFF_BUDGET_MS_PER_KIB, diffTimeBudgetMs } = await loadClient()

    expect(diffTimeBudgetMs(texts('x'.repeat(512), 'y'.repeat(512)))).toBe(DIFF_BUDGET_BASE_MS + DIFF_BUDGET_MS_PER_KIB)
    expect(diffTimeBudgetMs(texts('x'.repeat(512), 'y'.repeat(513)))).toBe(DIFF_BUDGET_BASE_MS + 2 * DIFF_BUDGET_MS_PER_KIB)
  })

  it('counts each line of a patch with its newline', async () => {
    const { DIFF_BUDGET_BASE_MS, DIFF_BUDGET_MS_PER_KIB, diffTimeBudgetMs } = await loadClient()
    // Two lines of 511 characters and their newlines make exactly 1 KiB.
    const line = `+${'x'.repeat(510)}`

    expect(diffTimeBudgetMs({
      kind: 'hunks',
      hunks: [
        {
          oldStart: 1,
          oldLines: 0,
          newStart: 1,
          newLines: 2,
          lines: [line, line],
        },
      ],
    })).toBe(DIFF_BUDGET_BASE_MS + DIFF_BUDGET_MS_PER_KIB)
  })
})

describe('diffInputKey', () => {
  /** One patch with `count` added lines of `text`. */
  function patch(count: number, text: string): DiffInput {
    return {
      kind: 'hunks',
      hunks: [
        {
          oldStart: 1,
          oldLines: 0,
          newStart: 1,
          newLines: count,
          lines: Array.from<string>({ length: count }).fill(`+${text}`),
        },
      ],
    }
  }

  it('keeps two pairs of texts apart when only their split point differs', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey(texts('a\u0000', 'b'))).not.toBe(diffInputKey(texts('a', '\u0000b')))
    expect(diffInputKey(texts('ab', 'c'))).not.toBe(diffInputKey(texts('a', 'bc')))
  })

  it('gives two patches with equal hunks one key', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey(patch(2, 'b'))).toBe(diffInputKey(patch(2, 'b')))
  })

  it('keeps two patches apart when their lines differ', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey(patch(2, 'b'))).not.toBe(diffInputKey(patch(2, 'c')))
    expect(diffInputKey(patch(2, 'b'))).not.toBe(diffInputKey(patch(3, 'b')))
  })

  it('keeps two inputs apart when only their line limit differs', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey({
      ...texts('a', 'b'),
      drawnLineLimit: 10,
    })).not.toBe(diffInputKey({
      ...texts('a', 'b'),
      drawnLineLimit: 11,
    }))
    expect(diffInputKey({
      ...texts('a', 'b'),
      drawnLineLimit: Number.POSITIVE_INFINITY,
    })).not.toBe(diffInputKey(texts('a', 'b')))
    expect(diffInputKey({
      ...patch(2, 'b'),
      drawnLineLimit: Number.POSITIVE_INFINITY,
    })).not.toBe(diffInputKey(patch(2, 'b')))
  })

  it('keeps two pairs of texts apart when only their line edit limit differs', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey({
      ...texts('a', 'b'),
      maxLineEdits: 5000,
    })).not.toBe(diffInputKey({
      ...texts('a', 'b'),
      maxLineEdits: 5001,
    }))
    expect(diffInputKey({
      ...texts('a', 'b'),
      maxLineEdits: 5000,
    })).not.toBe(diffInputKey(texts('a', 'b')))
  })

  it('gives two texts that state no line edit limit the key of two at LINE_DIFF_MAX_EDITS', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey(texts('a', 'b'))).toBe(diffInputKey({
      ...texts('a', 'b'),
      maxLineEdits: LINE_DIFF_MAX_EDITS,
    }))
  })

  it('gives an input that states no line limit the key of one at DIFF_DRAWN_LINE_LIMIT', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey(texts('a', 'b'))).toBe(diffInputKey({
      ...texts('a', 'b'),
      drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
    }))
    expect(diffInputKey(patch(2, 'b'))).toBe(diffInputKey({
      ...patch(2, 'b'),
      drawnLineLimit: DIFF_DRAWN_LINE_LIMIT,
    }))
  })

  it('keeps a patch apart from two texts', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey({ kind: 'hunks', hunks: [] })).not.toBe(diffInputKey(texts('', '')))
  })

  it('holds no copy of the texts or of the patch lines', async () => {
    const { diffInputKey } = await loadClient()
    const long = 'x'.repeat(100_000)

    expect(diffInputKey(texts(long, `${long}y`)).length).toBeLessThan(64)
    expect(diffInputKey(patch(10, long)).length).toBeLessThan(64)
  })
})
