import type { FetchResult } from '../../../model/tools/fetch'
import { ACP_SUPPLEMENT } from '~/generated/contracts/acp-protocol'
import { pickObject } from '~/lib/jsonPick'
import { webFetchFromObj } from '../../../model/tools/fetch'
import { collectAcpToolText } from '../content'

/**
 * Build a FetchResult from an ACP `tool_call_update` of kind `fetch`.
 * Returns null when the payload doesn't carry a recognizable HTTP status
 * shape — letting the caller fall back to the generic text branch.
 *
 * The shape isn't standardized across ACP agents today; this is wired up so
 * future agents that emit `{ code, bytes, durationMs }` get rendered via the
 * shared body for free.
 */
export function acpWebFetchFromToolCall(
  toolUse: Record<string, unknown> | null | undefined,
): FetchResult | null {
  if (!toolUse)
    return null
  const result = collectAcpToolText(toolUse, { rawObjects: false })
  return webFetchFromObj(pickObject(toolUse, ACP_SUPPLEMENT.RawOutput), { resultFallback: result })
    ?? (result ? { result } : null)
}
