import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { FetchRequest, FetchResult } from '../../../model/tools/fetch'
import type { ClaudeToolRow } from './toolCommon'
import { webFetchFromObj } from '../../../model/tools/fetch'
import { claudeToolFailureResult } from './failure'

/**
 * Build a FetchResult from a Claude `WebFetch` tool_result. Returns
 * null when the payload doesn't carry a numeric `code` field — letting the
 * catch-all renderer handle the fallback.
 */
export function claudeWebFetchFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
  resultContent: string,
): FetchResult | null {
  return webFetchFromObj(toolUseResult, { resultFallback: resultContent })
}

/**
 * The fetch pair: the address, and the page the tool brought back.
 *
 * The failure rung leads. A fetch that failed brought back no page, and the
 * fetched-page slot draws its body as MARKDOWN -- so the reason drew as the page,
 * with any `#` or `*` in it as a heading or as emphasis.
 */
export function claudeFetchSpec(request: FetchRequest, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'fetch'> {
  if (!result)
    return { kind: 'fetch', request }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'fetch', request, result: failure }
  const source = claudeWebFetchFromToolResult(result.toolUseResult, result.resultContent)
  return { kind: 'fetch', request, result: source ?? { result: result.resultContent } }
}
