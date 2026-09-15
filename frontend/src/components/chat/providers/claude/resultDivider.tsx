import type { ResultDividerModel } from '../registry'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { humanizeWireWord } from '../../rendererUtils'
import { turnEndLabel } from '../../turnEndLabel'

/** The interface's own word for a turn that stopped before it finished. */
const CLAUDE_SUBTYPE_CANCELLED = 'cancelled'

const apiErrorPattern = /^API Error: (\d+) (.*)$/

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
  const duration = durationMs !== null && durationMs > 0 ? durationMs : null

  if (subtype && subtype !== 'success') {
    const errorDetail = errors.length > 0 ? errors.join('\n') : resultText
    // `detail` must be undefined (never '') so the shared renderer skips the <pre>.
    return {
      label: turnEndLabel('failed', { durationMs: duration, reason: humanizeWireWord(subtype) }),
      isError: true,
      detail: errorDetail || undefined,
    }
  }

  const errorMsg = errors.length > 0 ? errors.join('; ') : resultText || 'Unknown error'
  return { label: turnEndLabel('failed', { durationMs: duration, reason: cleanAPIErrorMessage(errorMsg) }), isError: true }
}

/**
 * Build the divider model for a non-error result. Past the is_error===true
 * branch, Claude Code itself says this turn did not error, so never surface the
 * raw `result` text as a danger divider. Zero-turn local commands (`/context`,
 * `/usage`, even "Unknown command: ...") echo their already-shown output through
 * this envelope with is_error:false; rendering that echo in red was a false
 * alarm. Trust is_error and collapse to a plain "Took Xs" divider, keeping the
 * result text only for a genuine non-success subtype. Mirror the error branch's
 * `subtype && ...` guard so an absent subtype is treated as success-like instead
 * of leaking the raw echo into the label.
 *
 * The `cancelled` subtype never reaches here. `claudeResultDivider` answers it
 * ahead of the is_error test, because the interface marks its own cancellation
 * with `is_error: true` and only the error branch would ever have seen it.
 */
function buildPlainResult(
  resultText: string,
  durationMs: number | null,
  subtype: string,
): ResultDividerModel {
  const displayText = subtype && subtype !== 'success' ? resultText : ''
  // Any other non-success subtype's own text qualifies the turn end rather than
  // replacing it, so the row still opens with the words every other provider uses.
  return { label: turnEndLabel('ended', { durationMs, qualifiers: [displayText] }) }
}

/**
 * Claude result_divider hook: {"type":"result","duration_ms":865,"is_error":...}.
 * Returns the provider-neutral model; the shared `ResultDivider` draws it.
 *
 * A turn that STOPPED comes first, whatever the rest of the frame says, because the
 * command-line interface marks an interruption with `is_error: true` -- the same shape
 * as a genuine failure. Two fields report a stop, and each one carries a different half
 * of the answer:
 *
 *   - LeapMux's own completion column, for the interrupt that LeapMux sent. The
 *     interface reports that one as `error_during_execution`, and its `errors` array
 *     carries its own diagnostics, so the row read "Error during execution (12s)
 *     [ede_diagnostic] result_type=user ...".
 *   - The `cancelled` subtype, for a stop the interface reports itself.
 */
export function claudeResultDivider(parsed: unknown, completion?: MessageCompletion): ResultDividerModel | null {
  if (!isObject(parsed) || parsed.type !== 'result')
    return null

  // Shared reads — both branches need these fields. `duration_ms` defaults to
  // null (not 0) so the builders can tell a missing duration from a real 0.
  const resultText = pickString(parsed, 'result')
  const durationMs = pickNumber(parsed, 'duration_ms')
  const subtype = pickString(parsed, 'subtype')

  if (completion === MessageCompletion.INTERRUPTED || subtype === CLAUDE_SUBTYPE_CANCELLED)
    return { label: turnEndLabel('interrupted', { durationMs }) }

  return parsed.is_error === true
    ? buildErrorResult(parsed, resultText, durationMs, subtype)
    : buildPlainResult(resultText, durationMs, subtype)
}
