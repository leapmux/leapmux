import type { WebFetchResultSource } from '../../results/webFetchResult'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'

/**
 * Build a WebFetchResultSource from an ACP `tool_call_update` of kind `fetch`.
 * Returns null when the payload doesn't carry a recognizable HTTP status
 * shape — letting the caller fall back to the generic text branch.
 *
 * The shape isn't standardized across ACP agents today; this is wired up so
 * future agents that emit `{ code, bytes, durationMs }` get rendered via the
 * shared body for free.
 */
export function acpWebFetchFromToolCall(
  toolUse: Record<string, unknown> | null | undefined,
): WebFetchResultSource | null {
  if (!toolUse)
    return null
  const rawOutput = isObject(toolUse.rawOutput) ? toolUse.rawOutput as Record<string, unknown> : null
  if (!rawOutput)
    return null

  const code = pickNumber(rawOutput, 'code')
  if (code === null)
    return null

  return {
    code,
    codeText: pickString(rawOutput, 'codeText'),
    bytes: pickNumber(rawOutput, 'bytes', 0),
    durationMs: pickNumber(rawOutput, 'durationMs', 0),
    result: pickString(rawOutput, 'result'),
    url: pickString(rawOutput, 'url', undefined),
  }
}
