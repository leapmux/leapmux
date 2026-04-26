import type { WebFetchResultSource } from '../../../results/webFetchResult'
import { webFetchFromObj } from '../../../results/webFetchResult'

/**
 * Build a WebFetchResultSource from a Claude `WebFetch` tool_result. Returns
 * null when the payload doesn't carry a numeric `code` field — letting the
 * catch-all renderer handle the fallback.
 */
export function claudeWebFetchFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): WebFetchResultSource | null {
  return webFetchFromObj(toolUseResult, { resultFallback: resultContent })
}
