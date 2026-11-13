import type { ComputedDiff } from './diffCompute'
import type { DiffInput } from './diffTypes'
import type { DiffRequest, DiffResponse, PostedDiff } from './diffWorker'
import type { WorkerDemand } from '~/lib/workerDemand'
import { lruGet, lruSet } from '~/lib/mapLru'
import { memoizeByObject } from '~/lib/memoizeByObject'
import { fnv1a32Hex } from '~/lib/stringDigest'
import { createWorkerClient } from '~/lib/workerClient'
import { createCoalescedVersion, createWorkerDemand } from '~/lib/workerDemand'
import { createWorkerPriorityGate } from '~/lib/workerPriorityGate'
import { sameHunks } from './diffBuilder'
import { diffWithoutSearch, drawnLineLimitOf, fallbackDiff, maxLineEditsOf } from './diffCompute'

// ---------------------------------------------------------------------------
// Diff worker client
//
// `requestComputedDiff` answers the diff of an input, or starts the worker and
// answers undefined until it replies. A consumer reads it inside a memo. While
// the diff is pending, the memo depends on a version signal, which re-runs it
// when a diff arrives. A diff never changes once it is ready, so a ready memo
// depends on nothing here, and a reply for another input does not re-run it. A
// chat row over two texts stays behind its loading skeleton while its diff is
// pending, as it does for a pending Markdown body. A chat row over a patch draws
// the patch's hunks meanwhile, and only its word highlights wait (see
// FileEditDiffBody). A diff that needs no search, such as that of a created file,
// never waits for the worker (see diffWithoutSearch).
//
// Two caches answer a ready diff:
// - Each input object keeps its diff for as long as the object lives. A consumer
//   that holds the object therefore never loses its diff to the eviction below,
//   however many diffs are mounted.
// - A cache keyed by content serves an equal input in another object, such as
//   the source of a row revision whose content did not change.
// ---------------------------------------------------------------------------

const client = createWorkerClient<DiffRequest, PostedDiff | null>({
  spawn: () => new Worker(new URL('./diffWorker.ts', import.meta.url), { type: 'module' }),
  extract: (data: DiffResponse) => ({ id: data.id, value: data.diff }),
  failureValue: null,
})

// Viewport rows' diffs go ahead of overscan rows' (see createWorkerPriorityGate).
const gate = createWorkerPriorityGate()

/** The diff of each input object that received one, for as long as the object lives. */
const diffByInput = new WeakMap<DiffInput, ComputedDiff>()

/**
 * The most diffs that the content cache keeps: well above a viewport of edit rows,
 * so a row that mounts again with new source objects does not compute again.
 */
export const DIFF_CACHE_MAX_ENTRIES = 256

/** One diff of the content cache, with the input that it answers. */
interface CachedDiff {
  /** Kept to check a hit: two contents can share a key (see diffInputKey). */
  input: DiffInput
  diff: ComputedDiff
}

const diffCache = new Map<string, CachedDiff>()

/** The diffs that the worker computes now, keyed like the cache, with the demand on each. */
const inFlight = new Map<string, WorkerDemand>()

/** Bumped when replies land, once for a burst, to re-run every waiting consumer. */
const diffVersion = createCoalescedVersion()

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

function contentKey(input: DiffInput): string {
  const limit = drawnLineLimitOf(input)
  if (input.kind === 'texts') {
    const { oldText, newText } = input
    return `t:${limit}:${maxLineEditsOf(input)}:${oldText.length}:${fnv1a32Hex(oldText)}:${newText.length}:${fnv1a32Hex(newText)}`
  }
  const json = JSON.stringify(input.hunks)
  return `h:${limit}:${json.length}:${fnv1a32Hex(json)}`
}

/**
 * The cache key of `input`: its limits, and the length and the digest of its
 * content, so that an equal input in another object finds the same entry. Each
 * limit changes the diff (see DiffInput): the line limit decides which lines have
 * word diffs, and the line edit limit decides whether two texts draw their
 * minimal diff. Two inputs that differ only in a limit therefore have different
 * diffs, and an absent limit keys as its default. The key holds no copy of the
 * content. Two contents can share a digest, so a cache entry keeps its input, and
 * a read compares the two (see sameDiffInput).
 *
 * A key reads the whole input, so each input object builds its key once.
 */
export const diffInputKey = memoizeByObject(contentKey)

/** Whether two inputs hold the same content and the same limits, so that they have one diff. */
function sameDiffInput(a: DiffInput, b: DiffInput): boolean {
  if (a === b)
    return true
  if (drawnLineLimitOf(a) !== drawnLineLimitOf(b))
    return false
  if (a.kind === 'texts') {
    return b.kind === 'texts'
      && maxLineEditsOf(a) === maxLineEditsOf(b)
      && a.oldText === b.oldText
      && a.newText === b.newText
  }
  return b.kind === 'hunks' && sameHunks(a.hunks, b.hunks)
}

/**
 * The diff that a worker reply states for `input`. A worker that could not
 * start, failed or ran past its budget leaves the view a diff that needs no
 * search, and the request is not retried. A `hunks` input keeps its own hunks
 * (see PostedDiff).
 */
function receivedDiff(input: DiffInput, posted: PostedDiff | null): ComputedDiff {
  if (posted === null)
    return fallbackDiff(input)
  if (input.kind === 'hunks')
    return { ...posted, hunks: input.hunks }
  // The worker posts the hunks of every `texts` input; a reply without them is broken.
  return posted.hunks === null
    ? fallbackDiff(input)
    : { ...posted, hunks: posted.hunks }
}

function dispatch(key: string, input: DiffInput, demand: WorkerDemand): void {
  void gate
    .enqueue(() => client.request(id => ({ type: 'diff', id, input }), {
      timeBudgetMs: diffTimeBudgetMs(input),
    }), demand.isLow)
    .then((posted) => {
      const diff = receivedDiff(input, posted)
      diffByInput.set(input, diff)
      lruSet(diffCache, key, { input, diff }, DIFF_CACHE_MAX_ENTRIES)
      inFlight.delete(key)
      diffVersion.bump()
    })
}

/**
 * The diff a view draws for `input`, or undefined while the worker computes it.
 * A diff that needs no search (see diffWithoutSearch) is ready at once.
 *
 * Reactive: read it in a memo. While the diff is pending, the memo re-runs when
 * a diff arrives; once the diff is ready, the memo depends on nothing here. Pass
 * the same input object on each run, so that the object keeps its diff.
 *
 * `isLowPriority` lets the gate hold this diff behind others while it is true. A
 * consumer that passes none makes the diff urgent.
 */
export function requestComputedDiff(input: DiffInput, isLowPriority?: () => boolean): ComputedDiff | undefined {
  const ready = readyDiff(input)
  if (ready !== undefined)
    return ready
  const key = diffInputKey(input)
  let demand = inFlight.get(key)
  if (demand === undefined) {
    // A diff that needs no search, such as a created or a deleted file, costs
    // less than a worker round trip, so the calling thread answers it. The check
    // compares the two texts, so it runs only where the client would dispatch: a
    // consumer that waits for the worker does not compare them again at each re-run.
    const known = diffWithoutSearch(input)
    if (known !== undefined) {
      diffByInput.set(input, known)
      return known
    }
    demand = createWorkerDemand()
    inFlight.set(key, demand)
    dispatch(key, input, demand)
  }
  diffVersion.read()
  demand.note(isLowPriority)
  return undefined
}

/**
 * The diff of `input` when it is ready without the worker: the input object or
 * the content cache holds it.
 */
function readyDiff(input: DiffInput): ComputedDiff | undefined {
  const held = diffByInput.get(input)
  if (held !== undefined)
    return held
  const cached = lruGet(diffCache, diffInputKey(input))
  if (cached === undefined || !sameDiffInput(cached.input, input))
    return undefined
  diffByInput.set(input, cached.diff)
  return cached.diff
}
