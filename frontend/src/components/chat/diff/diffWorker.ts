import type { ComputedDiff, DiffInput } from './diffCompute'
import { computeDiff, fallbackDiff } from './diffCompute'

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

export interface DiffResponse {
  type: 'diff-result'
  id: number
  diff: ComputedDiff
}

/** Answer one request. A diff that throws, such as on a stack overflow, answers with the fallback. */
export function answerDiffRequest(request: DiffRequest): DiffResponse {
  let diff: ComputedDiff
  try {
    diff = computeDiff(request.input)
  }
  catch {
    diff = fallbackDiff(request.input)
  }
  return { type: 'diff-result', id: request.id, diff }
}

globalThis.onmessage = (event: MessageEvent<DiffRequest>) => {
  globalThis.postMessage(answerDiffRequest(event.data))
}
