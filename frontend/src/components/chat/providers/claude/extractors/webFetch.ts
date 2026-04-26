import type { WebFetchResultSource } from '../../../results/webFetchResult'
import { pickNumber, pickString } from '~/lib/jsonPick'

/**
 * Build a WebFetchResultSource from a Claude `WebFetch` tool_result. Returns
 * null when the payload doesn't carry a numeric `code` field — letting the
 * catch-all renderer handle the fallback.
 */
export function claudeWebFetchFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): WebFetchResultSource | null {
  if (!toolUseResult || typeof toolUseResult.code !== 'number')
    return null

  return {
    code: toolUseResult.code as number,
    codeText: pickString(toolUseResult, 'codeText'),
    bytes: pickNumber(toolUseResult, 'bytes', 0),
    durationMs: pickNumber(toolUseResult, 'durationMs', 0),
    result: pickString(toolUseResult, 'result', resultContent),
    url: pickString(toolUseResult, 'url', undefined),
  }
}
