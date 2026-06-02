import type { ResultDividerModel } from '../registry'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { formatDuration } from '../../rendererUtils'

const UNDERSCORE = /_/g
const FIRST_CHAR = /^\w/
const apiErrorPattern = /^API Error: (\d+) (.*)$/

function humanizeSubtype(subtype: string): string {
  return subtype.replace(UNDERSCORE, ' ').replace(FIRST_CHAR, c => c.toUpperCase())
}

/**
 * Cleans up synthetic API error messages from Claude Code.
 * Extracts a human-readable message from the embedded JSON body, e.g.:
 *   "API Error: 529 {\"type\":\"error\",...,\"message\":\"Overloaded...\"}"
 * becomes:
 *   "API Error: 529 Overloaded..."
 */
function cleanAPIErrorMessage(msg: string): string {
  const match = apiErrorPattern.exec(msg)
  if (!match)
    return msg
  const [, statusCode, body] = match
  if (body.startsWith('{')) {
    try {
      const parsed = JSON.parse(body)
      const message = parsed?.error?.message
      if (typeof message === 'string')
        return `API Error: ${statusCode} ${message}`
    }
    catch { /* not parseable JSON */ }
    return `API Error: ${statusCode}`
  }
  return msg
}

/**
 * Build the divider model for a failed result (is_error===true). Non-success
 * subtypes get a humanized label plus an errors/result detail block; generic
 * errors get the cleaned API-error message baked into the label. `durationMs` is
 * null when the envelope omitted `duration_ms`, in which case the duration
 * suffix is dropped.
 */
function buildErrorResult(
  parsed: Record<string, unknown>,
  resultText: string,
  durationMs: number | null,
  subtype: string,
): ResultDividerModel {
  const errors = Array.isArray(parsed.errors) ? parsed.errors as string[] : []
  const durationSuffix = durationMs !== null && durationMs > 0 ? ` (${formatDuration(durationMs)})` : ''

  if (subtype && subtype !== 'success') {
    const label = humanizeSubtype(subtype) + durationSuffix
    const errorDetail = errors.length > 0 ? errors.join('\n') : resultText
    // `detail` must be undefined (never '') so the shared renderer skips the <pre>.
    return { label, isError: true, detail: errorDetail || undefined }
  }

  const errorMsg = errors.length > 0 ? errors.join('; ') : resultText || 'Unknown error'
  return { label: cleanAPIErrorMessage(errorMsg) + durationSuffix, isError: true }
}

/**
 * Build the divider model for a non-error result. Past the is_error===true
 * branch, Claude Code itself says this turn did not error, so never surface the
 * raw `result` text as a danger divider. Zero-turn local commands (`/context`,
 * `/usage`, even "Unknown command: ...") echo their already-shown output through
 * this envelope with is_error:false; rendering that echo in red was a false
 * alarm. Trust is_error and collapse to a plain "Took Xs" divider, keeping the
 * result text only for a genuine non-success subtype (e.g. "cancelled"). Mirror
 * the error branch's `subtype && ...` guard so an absent subtype is treated as
 * success-like instead of leaking the raw echo into the label.
 */
function buildPlainResult(
  resultText: string,
  durationMs: number | null,
  subtype: string,
): ResultDividerModel {
  const displayText = subtype && subtype !== 'success' ? resultText : ''
  if (displayText) {
    const suffix = durationMs !== null ? ` (${formatDuration(durationMs)})` : ''
    return { label: displayText + suffix }
  }
  // Duration-only divider. A missing duration_ms (null) has no meaningful "Took"
  // value, so fall back to a plain "Turn ended"; a real zero stays "Took 0ms".
  return { label: durationMs !== null ? `Took ${formatDuration(durationMs)}` : 'Turn ended' }
}

/**
 * Claude result_divider hook: {"type":"result","duration_ms":865,"is_error":...}.
 * Returns the provider-neutral model; the shared `ResultDivider` draws it.
 */
export function claudeResultDivider(parsed: unknown): ResultDividerModel | null {
  if (!isObject(parsed) || parsed.type !== 'result')
    return null

  // Shared reads — both branches need these fields. `duration_ms` defaults to
  // null (not 0) so the builders can tell a missing duration from a real 0.
  const resultText = pickString(parsed, 'result')
  const durationMs = pickNumber(parsed, 'duration_ms')
  const subtype = pickString(parsed, 'subtype')

  return parsed.is_error === true
    ? buildErrorResult(parsed, resultText, durationMs, subtype)
    : buildPlainResult(resultText, durationMs, subtype)
}
