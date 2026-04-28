import type { WebFetchResultSource } from '../../../results/webFetchResult'
import { pickObject } from '~/lib/jsonPick'
import { webFetchFromObj } from '../../../results/webFetchResult'

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
  return webFetchFromObj(pickObject(toolUse, 'rawOutput'))
}
