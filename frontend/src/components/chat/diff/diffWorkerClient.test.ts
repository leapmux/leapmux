import type { DiffInput } from './diffCompute'
import type { DiffRequest } from './diffWorker'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { computeDiff, fallbackDiff } from './diffCompute'
import { answerDiffRequest } from './diffWorker'

type ClientModule = typeof import('./diffWorkerClient')

/** A stand-in Worker that keeps each request until the test answers it. */
interface HeldWorker {
  requests: DiffRequest[]
  onmessage: ((event: { data: unknown }) => void) | null
  onerror: (() => void) | null
}

/** Install a stand-in Worker, and list each one that the client spawns. */
function installWorker(): HeldWorker[] {
  const spawned: HeldWorker[] = []
  vi.stubGlobal('Worker', class implements HeldWorker {
    requests: DiffRequest[] = []
    onmessage: ((event: { data: unknown }) => void) | null = null
    onerror: (() => void) | null = null
    terminate = vi.fn()
    constructor() {
      spawned.push(this)
    }

    postMessage(request: DiffRequest) {
      this.requests.push(request)
    }
  })
  return spawned
}

/** The one worker that the client spawned. */
function onlyWorker(spawned: readonly HeldWorker[]): HeldWorker {
  expect(spawned).toHaveLength(1)
  return spawned[0]!
}

/** Answer the request for `input` as the real worker would. */
function answer(worker: HeldWorker, input: DiffInput): void {
  const request = worker.requests.find(candidate => candidate.input === input)
  if (request === undefined)
    throw new Error('expected a request for the input')
  worker.onmessage?.({
    data: answerDiffRequest(request),
  })
}

/**
 * Let every promise settle: the reply passes through the worker client, the gate,
 * the cache and the version bump, each of which takes microtasks of its own.
 */
async function settle(): Promise<void> {
  await new Promise(resolve => setTimeout(resolve, 0))
}

/** Import the client afresh, so that it spawns from this test's Worker and starts with no cache. */
async function loadClient(): Promise<ClientModule> {
  vi.resetModules()
  return import('./diffWorkerClient')
}

function texts(oldText: string, newText: string): DiffInput {
  return { kind: 'texts', oldText, newText }
}

describe('requestComputedDiff', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('computes the diff on this thread where no Worker exists', async () => {
    vi.stubGlobal('Worker', undefined)
    const { computedDiffIfReady, requestComputedDiff } = await loadClient()
    const input = texts('a\n', 'b\n')

    expect(requestComputedDiff(input)).toEqual(computeDiff(input))
    expect(computedDiffIfReady(texts('c\n', 'd\n'))).toEqual(computeDiff(texts('c\n', 'd\n')))
  })

  it('answers undefined until the worker replies, then re-runs the memo that read it', async () => {
    const spawned = installWorker()
    const { requestComputedDiff } = await loadClient()
    // After the reset, the client reads its signal from a fresh copy of Solid; the
    // memo must come from that copy too, or it cannot track the signal.
    const { createMemo, createRoot } = await import('solid-js')
    const input = texts('a\n', 'b\n')

    await createRoot(async (dispose) => {
      const diff = createMemo(() => requestComputedDiff(input))
      expect(diff()).toBeUndefined()

      answer(onlyWorker(spawned), input)
      await settle()

      expect(diff()).toEqual(computeDiff(input))
      dispose()
    })
  })

  it('asks the worker once for equal content, and serves the cache after it replies', async () => {
    const spawned = installWorker()
    const { requestComputedDiff } = await loadClient()
    const input = texts('a\n', 'b\n')

    expect(requestComputedDiff(input)).toBeUndefined()
    expect(requestComputedDiff(texts('a\n', 'b\n'))).toBeUndefined()
    await settle()
    const worker = onlyWorker(spawned)
    expect(worker.requests).toHaveLength(1)

    answer(worker, input)
    await settle()

    expect(requestComputedDiff(texts('a\n', 'b\n'))).toEqual(computeDiff(input))
    expect(worker.requests).toHaveLength(1)
  })

  it('draws the fallback diff when the worker crashes, and does not ask again', async () => {
    const spawned = installWorker()
    const { requestComputedDiff } = await loadClient()
    const input = texts('a\n', 'b\n')

    requestComputedDiff(input)
    await settle()
    onlyWorker(spawned).onerror?.()
    await settle()

    expect(requestComputedDiff(input)).toEqual(fallbackDiff(input))
    expect(spawned).toHaveLength(1)
  })

  it('posts an urgent diff ahead of the low-priority diffs that wait at the gate', async () => {
    const spawned = installWorker()
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
    await settle()
    const worker = onlyWorker(spawned)
    // The gate posts two at a time and holds the rest.
    expect(worker.requests.map(request => request.input)).toEqual([inputs[0], inputs[1]])

    answer(worker, inputs[0]!)
    await settle()

    expect(worker.requests.map(request => request.input)).toEqual([inputs[0], inputs[1], urgent])
  })

  it('makes a low-priority diff urgent when a consumer with no priority asks for it', async () => {
    const spawned = installWorker()
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
    await settle()
    const worker = onlyWorker(spawned)

    answer(worker, inputs[0]!)
    await settle()

    expect(worker.requests.map(request => request.input)).toEqual([inputs[0], inputs[1], inputs[3]])
  })

  it('follows the priority of the latest consumer while the diff waits', async () => {
    const spawned = installWorker()
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
    await settle()
    const worker = onlyWorker(spawned)

    answer(worker, inputs[0]!)
    await settle()

    expect(worker.requests.map(request => request.input)).toEqual([inputs[0], inputs[1], inputs[3]])
  })
})

describe('computedDiffIfReady', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('answers undefined and asks the worker nothing while the diff is not cached', async () => {
    const spawned = installWorker()
    const { computedDiffIfReady } = await loadClient()

    expect(computedDiffIfReady(texts('a\n', 'b\n'))).toBeUndefined()
    await settle()

    expect(spawned).toHaveLength(0)
  })

  it('answers the diff that the worker computed', async () => {
    const spawned = installWorker()
    const { computedDiffIfReady, requestComputedDiff } = await loadClient()
    const input = texts('a\n', 'b\n')

    requestComputedDiff(input)
    await settle()
    answer(onlyWorker(spawned), input)
    await settle()

    expect(computedDiffIfReady(texts('a\n', 'b\n'))).toEqual(computeDiff(input))
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
  it('keeps two pairs of texts apart when only their split point differs', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey(texts('a\u0000', 'b'))).not.toBe(diffInputKey(texts('a', '\u0000b')))
    expect(diffInputKey(texts('ab', 'c'))).not.toBe(diffInputKey(texts('a', 'bc')))
  })

  it('gives two patches with equal hunks one key', async () => {
    const { diffInputKey } = await loadClient()
    const hunk = () => ({
      oldStart: 1,
      oldLines: 1,
      newStart: 1,
      newLines: 1,
      lines: ['-a', '+b'],
    })

    expect(diffInputKey({
      kind: 'hunks',
      hunks: [
        hunk(),
      ],
    })).toBe(diffInputKey({
      kind: 'hunks',
      hunks: [
        hunk(),
      ],
    }))
  })

  it('keeps a patch apart from two texts', async () => {
    const { diffInputKey } = await loadClient()

    expect(diffInputKey({ kind: 'hunks', hunks: [] })).not.toBe(diffInputKey(texts('', '')))
  })
})
