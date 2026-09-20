import { pickNumber, pickString } from '~/lib/jsonPick'

/** One page a call asked the agent to read. */
export interface FetchRequest { url: string }

/**
 * What the fetch returned: the response facts, and the page as markdown.
 *
 * The address is NOT here. The request states what the call asked for, and
 * `results/webFetchResult.tsx` draws this half.
 */
export interface FetchResult {
  code?: number
  codeText?: string
  bytes?: number
  durationMs?: number
  /** Markdown body returned by the fetch. */
  result: string
}

/**
 * Build a FetchResult from a record carrying `{code, codeText, bytes,
 * durationMs, result}`. Returns null when `code` is not a number — the
 * caller can then fall back to the generic text branch.
 */
export function webFetchFromObj(
  obj: Record<string, unknown> | null | undefined,
  opts?: { resultFallback?: string },
): FetchResult | null {
  if (!obj || typeof obj.code !== 'number')
    return null
  return {
    code: obj.code,
    codeText: pickString(obj, 'codeText'),
    bytes: pickNumber(obj, 'bytes', 0),
    durationMs: pickNumber(obj, 'durationMs', 0),
    result: pickString(obj, 'result', opts?.resultFallback ?? ''),
  }
}
