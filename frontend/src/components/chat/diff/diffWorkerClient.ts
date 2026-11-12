import type { ComputedDiff, DiffInput } from './diffCompute'
import type { StructuredPatchHunk } from './diffTypes'
import type { DiffRequest, DiffResponse } from './diffWorker'
import { createSignal } from 'solid-js'
import { lruGet, lruSet } from '~/lib/mapLru'
import { createWorkerClient } from '~/lib/workerClient'
import { createWorkerPriorityGate } from '~/lib/workerPriorityGate'
import { computeDiff, fallbackDiff } from './diffCompute'

// ---------------------------------------------------------------------------
// Diff worker client
//
// `requestComputedDiff` answers from a cache keyed by content, or starts the
// worker and answers undefined until it replies. A consumer reads it inside a
// memo: the version signal re-runs the memo when any diff arrives. A chat row
// stays behind its loading skeleton while its diff is pending, as it does for a
// pending Markdown body.
// ---------------------------------------------------------------------------

const client = createWorkerClient<DiffRequest, ComputedDiff | null>({
  spawn: () => new Worker(new URL('./diffWorker.ts', import.meta.url), { type: 'module' }),
  extract: (data: DiffResponse) => ({ id: data.id, value: data.diff }),
  failureValue: null,
})

// Viewport rows' diffs go ahead of overscan rows' (see createWorkerPriorityGate).
const gate = createWorkerPriorityGate()

/** Diffs kept: well above a viewport of edit rows, so a scroll stops recomputing. */
const CACHE_MAX_SIZE = 256
const diffCache = new Map<string, ComputedDiff>()

/** One diff that the worker computes now, and the demand on it. */
interface InFlightDiff {
  /** True once a consumer with no priority thunk asked for it. Never resets. */
  urgent: boolean
  /** The priority thunk of the latest consumer: its row's offscreen test. */
  isLowPriority: (() => boolean) | undefined
}

const inFlight = new Map<string, InFlightDiff>()

const [diffVersion, setDiffVersion] = createSignal(0)
let bumpScheduled = false

/** Re-run every waiting consumer once for a burst of replies. */
function scheduleVersionBump(): void {
  if (bumpScheduled)
    return
  bumpScheduled = true
  queueMicrotask(() => {
    bumpScheduled = false
    setDiffVersion(v => v + 1)
  })
}

/**
 * The time a request may spend in the worker: 10 s, plus 40 ms per KiB of input.
 * Past it the client terminates the worker, and the view draws the fallback.
 */
export const DIFF_BUDGET_BASE_MS = 10_000
export const DIFF_BUDGET_MS_PER_KIB = 40

function inputSize(input: DiffInput): number {
  if (input.kind === 'texts')
    return input.oldText.length + input.newText.length
  let size = 0
  for (const hunk of input.hunks) {
    for (const line of hunk.lines)
      size += line.length + 1
  }
  return size
}

export function diffTimeBudgetMs(input: DiffInput): number {
  return DIFF_BUDGET_BASE_MS + Math.ceil(inputSize(input) / 1024) * DIFF_BUDGET_MS_PER_KIB
}

// The key of a patch is its JSON. A patch's hunks array is stable for a row
// revision, so the JSON is built once per array.
const patchKeys = new WeakMap<StructuredPatchHunk[], string>()

/** The cache key of `input`: its content, so a re-render with equal content hits. */
export function diffInputKey(input: DiffInput): string {
  if (input.kind === 'texts')
    return `t\u0000${input.oldText.length}\u0000${input.oldText}\u0000${input.newText}`
  let key = patchKeys.get(input.hunks)
  if (key === undefined) {
    key = `h\u0000${JSON.stringify(input.hunks)}`
    patchKeys.set(input.hunks, key)
  }
  return key
}

function dispatch(key: string, input: DiffInput, demand: InFlightDiff): void {
  const isLowPriority = () => !demand.urgent && (demand.isLowPriority?.() ?? true)
  void gate
    .enqueue(() => client.request(id => ({ type: 'diff', id, input }), {
      timeBudgetMs: diffTimeBudgetMs(input),
    }), isLowPriority)
    .then((diff) => {
      // A worker that failed or ran past its budget leaves the view a diff that
      // needs no search; the request is not retried.
      lruSet(diffCache, key, diff ?? fallbackDiff(input), CACHE_MAX_SIZE)
      inFlight.delete(key)
      scheduleVersionBump()
    })
}

/**
 * The diff a view draws for `input`, or undefined while the worker computes it.
 *
 * Reactive: read it in a memo, which re-runs when a diff arrives. Where no Worker
 * exists (the jsdom unit tests) it computes the diff on the calling thread.
 *
 * `isLowPriority` lets the gate hold this diff behind others while it is true. A
 * consumer that passes none makes the diff urgent.
 */
export function requestComputedDiff(input: DiffInput, isLowPriority?: () => boolean): ComputedDiff | undefined {
  diffVersion()
  const key = diffInputKey(input)
  const ready = readyDiff(key, input)
  if (ready !== undefined)
    return ready
  let demand = inFlight.get(key)
  if (demand === undefined) {
    demand = { urgent: false, isLowPriority: undefined }
    inFlight.set(key, demand)
    dispatch(key, input, demand)
  }
  if (isLowPriority === undefined)
    demand.urgent = true
  else
    demand.isLowPriority = isLowPriority
  return undefined
}

/**
 * The diff of `input` when it is ready without the worker: the cache holds it, or
 * no Worker exists and the calling thread computes it.
 */
function readyDiff(key: string, input: DiffInput): ComputedDiff | undefined {
  const cached = lruGet(diffCache, key)
  if (cached !== undefined || typeof Worker !== 'undefined')
    return cached
  const diff = computeDiff(input)
  lruSet(diffCache, key, diff, CACHE_MAX_SIZE)
  return diff
}

/**
 * The diff of `input` when it is ready, as requestComputedDiff answers it, or
 * undefined. Unlike requestComputedDiff, it starts no worker request and subscribes
 * to nothing.
 */
export function computedDiffIfReady(input: DiffInput): ComputedDiff | undefined {
  return readyDiff(diffInputKey(input), input)
}
