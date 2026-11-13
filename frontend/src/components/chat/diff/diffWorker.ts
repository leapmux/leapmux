import type { ComputedDiff } from './diffCompute'
import type { DiffInput, StructuredPatchHunk } from './diffTypes'
import { fallbackDiff } from './diffCompute'
import { computeDiff } from './diffSearch'

// ---------------------------------------------------------------------------
// Diff worker
//
// Computes the line and word diffs that a diff view draws, off the main thread
// (see diffCompute). The client limits each request with a time budget, and
// draws a fallback diff when the worker runs past it.
// ---------------------------------------------------------------------------

export interface DiffRequest {
  type: 'diff'
  id: number
  input: DiffInput
}

/**
 * A computed diff as the worker posts it. For a `hunks` input, `hunks` is null:
 * the client holds those hunks already, and a structured clone would copy every
 * line to the worker and back for nothing.
 */
export type PostedDiff = Omit<ComputedDiff, 'hunks'> & { hunks: StructuredPatchHunk[] | null }

export interface DiffResponse {
  type: 'diff-result'
  id: number
  diff: PostedDiff
}

/**
 * Answer one request. A computation that throws, such as on a stack overflow,
 * answers with the fallback. It logs the error, so that a bug in the computation
 * leaves a diagnostic, and not only a fallback diff.
 */
export function answerDiffRequest(request: DiffRequest): DiffResponse {
  let diff: ComputedDiff
  try {
    diff = computeDiff(request.input)
  }
  catch (error) {
    console.warn('[diffWorker] the diff computation failed; drawing the fallback diff:', error)
    diff = fallbackDiff(request.input)
  }
  return {
    type: 'diff-result',
    id: request.id,
    diff: request.input.kind === 'hunks'
      ? { ...diff, hunks: null }
      : diff,
  }
}

globalThis.onmessage = (event: MessageEvent<DiffRequest>) => {
  globalThis.postMessage(answerDiffRequest(event.data))
}
