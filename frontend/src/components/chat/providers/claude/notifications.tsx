/* eslint-disable solid/components-return-once -- render methods are not Solid components */
import type { MessageContentRenderer } from '../../messageRenderers'
import type { RateLimitInfo } from '~/stores/agentSession.store'
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

/** Handles Claude rate limit notifications: {"type":"rate_limit","rate_limit_info":{...}} */
export const rateLimitRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'rate_limit')
      return null
    const info = parsed.rate_limit_info
    if (!isObject(info))
      return <div>Rate limit update</div>
    // Hide "allowed" status from chat — the popover still shows it.
    const rl = info as RateLimitInfo
    if (rl.status === 'allowed')
      return null
    return <div>{formatRateLimitMessage(rl)}</div>
  },
}

/** Handles Claude result messages: {"type":"result","duration_ms":865,"num_turns":558,...} */
export const resultRenderer: MessageContentRenderer = {
  render(parsed, _role, _context) {
    if (!isObject(parsed) || parsed.type !== 'result')
      return null

    if (parsed.is_error === true) {
      const errors = Array.isArray(parsed.errors) ? parsed.errors as string[] : []
      const resultText = pickString(parsed, 'result')
      const durationMs = pickNumber(parsed, 'duration_ms', 0)
      const durationSuffix = durationMs > 0 ? ` (${formatDuration(durationMs)})` : ''

      const subtype = pickString(parsed, 'subtype')
      if (subtype && subtype !== 'success') {
        const humanSubtype = humanizeSubtype(subtype)
        const label = humanSubtype + durationSuffix
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

    const durationMs = pickNumber(parsed, 'duration_ms', 0)
    const resultText = pickString(parsed, 'result')
    const durationStr = formatDuration(durationMs)

    const numTurns = pickNumber(parsed, 'num_turns', 0)
    if (!parsed.stop_reason && numTurns <= 1 && resultText) {
      return <div class={resultDivider} style={{ color: 'var(--danger)' }}>{resultText}</div>
    }

    const displayText = parsed.subtype !== 'success' ? resultText : ''
    const label = displayText
      ? `${displayText} (${durationStr})`
      : `Took ${durationStr}`
    return <div class={resultDivider}>{label}</div>
  },
}

/** Handles rate_limit entries within a notification thread (consolidated dividers). */
export function claudeNotificationThreadEntry(
  m: Record<string, unknown>,
):
  | Array<{ kind: 'text', text: string } | { kind: 'group', groupKey: string, prefix: string, entry: string } | { kind: 'divider', text: string, loading?: boolean }>
  | null {
  const t = m.type as string | undefined
  if (t === 'rate_limit') {
    const info = m.rate_limit_info
    if (isObject(info)) {
      const rlInfo = info as Record<string, unknown>
      if (rlInfo.status !== 'allowed')
        return [{ kind: 'text', text: formatRateLimitMessage(rlInfo) }]
    }
    return []
  }
  return null
}
