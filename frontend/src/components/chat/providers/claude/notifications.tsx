import type { JSX } from 'solid-js'
import type { MessageContentRenderer } from '../../messageRenderers'
import type { NotificationThreadEntry } from '../registry'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { formatRateLimitMessage } from '~/lib/rateLimitUtils'
import { resultDivider, resultErrorDetail } from '../../messageStyles.css'
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
 *   "API Error: 529 · Overloaded..."
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
 * Render a failed result (is_error===true) as a danger divider. Non-success
 * subtypes get a humanized label plus an errors/result detail block; generic
 * errors get the cleaned API-error message inline. `durationMs` is null when
 * the envelope omitted `duration_ms`, in which case the duration suffix is
 * dropped.
 */
function renderErrorResult(
  parsed: Record<string, unknown>,
  resultText: string,
  durationMs: number | null,
  subtype: string,
): JSX.Element {
  const errors = Array.isArray(parsed.errors) ? parsed.errors as string[] : []
  const durationSuffix = durationMs !== null && durationMs > 0 ? ` (${formatDuration(durationMs)})` : ''

  if (subtype && subtype !== 'success') {
    const label = humanizeSubtype(subtype) + durationSuffix
    const errorDetail = errors.length > 0 ? errors.join('\n') : resultText || ''
    return (
      <>
        <div class={resultDivider} style={{ color: 'var(--danger)' }}>{label}</div>
        {errorDetail && <pre class={resultErrorDetail}>{errorDetail}</pre>}
      </>
    )
  }

  const errorMsg = errors.length > 0 ? errors.join('; ') : resultText || 'Unknown error'
  const label = cleanAPIErrorMessage(errorMsg) + durationSuffix
  return <div class={resultDivider} style={{ color: 'var(--danger)' }}>{label}</div>
}

/**
 * Render a non-error result as a plain turn-end divider. Past the
 * is_error===true branch, Claude Code itself says this turn did not error, so
 * never surface the raw `result` text as a danger divider. Zero-turn local
 * commands (`/context`, `/usage`, even "Unknown command: ...") echo their
 * already-shown output through this envelope with is_error:false; rendering
 * that echo in red was a false alarm. Trust is_error and collapse to a plain
 * "Took Xs" divider, keeping the result text only for a genuine non-success
 * subtype (e.g. "cancelled"). Mirror the error branch's `subtype && ...` guard
 * so an absent subtype is treated as success-like instead of leaking the raw
 * echo into the label.
 */
function renderPlainResult(
  resultText: string,
  durationMs: number | null,
  subtype: string,
): JSX.Element {
  const displayText = subtype && subtype !== 'success' ? resultText : ''
  let label: string
  if (displayText) {
    const suffix = durationMs !== null ? ` (${formatDuration(durationMs)})` : ''
    label = displayText + suffix
  }
  else {
    // Duration-only divider. A missing duration_ms (null) has no meaningful
    // "Took" value, so fall back to a plain "Turn ended"; a real zero stays
    // "Took 0ms".
    label = durationMs !== null ? `Took ${formatDuration(durationMs)}` : 'Turn ended'
  }
  return <div class={resultDivider}>{label}</div>
}

/** Handles Claude result messages: {"type":"result","duration_ms":865,"num_turns":558,...} */
export const resultRenderer: MessageContentRenderer = {
  render(parsed, _context) {
    if (!isObject(parsed) || parsed.type !== 'result')
      return null

    // Shared reads — both branches need these fields. `duration_ms` defaults to
    // null (not 0) so the renderers can tell a missing duration from a real 0.
    const resultText = pickString(parsed, 'result')
    const durationMs = pickNumber(parsed, 'duration_ms')
    const subtype = pickString(parsed, 'subtype')

    return parsed.is_error === true
      ? renderErrorResult(parsed, resultText, durationMs, subtype)
      : renderPlainResult(resultText, durationMs, subtype)
  },
}

/** Handles rate_limit_event entries within a notification thread (consolidated dividers). */
export function claudeNotificationThreadEntry(
  m: Record<string, unknown>,
): NotificationThreadEntry[] | null {
  const t = m.type as string | undefined
  if (t === 'rate_limit_event') {
    const info = m.rate_limit_info
    if (!isObject(info))
      // A malformed payload (non-object rate_limit_info) still surfaces a
      // generic line rather than vanishing -- classify only routes it here when
      // it isn't an "allowed" status, so it is a real (if rare) notification.
      return [{ kind: 'text', text: 'Rate limit update' }]
    const rlInfo = info as Record<string, unknown>
    if (rlInfo.status !== 'allowed')
      return [{ kind: 'text', text: formatRateLimitMessage(rlInfo) }]
    return []
  }
  return null
}
