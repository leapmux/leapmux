import type { ToolCallPayload } from '../../../ir/toolCall'
import type { WebSearchRequest, WebSearchResult } from '../../../ir/tools/webSearch'
import type { ClaudeToolRow } from './toolCommon'
import { pickNumber } from '~/lib/jsonPick'
import { unparsedResult } from '../../../ir/toolCall'
import { extractWebSearchLinks, extractWebSearchSummary } from '../../../ir/tools/webSearch'
import { claudeFailedResult } from './failure'

/**
 * Build a WebSearchResult from a Claude `WebSearch` tool_result. Returns
 * null when the payload doesn't carry a `results` array — the caller should
 * fall through to the catch-all renderer.
 */
export function claudeWebSearchFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
): WebSearchResult | null {
  if (!toolUseResult || !Array.isArray(toolUseResult.results))
    return null
  const links = extractWebSearchLinks(toolUseResult.results)
  const summary = extractWebSearchSummary(toolUseResult.results)
  const durationSeconds = pickNumber(toolUseResult, 'durationSeconds', undefined)
  return {
    links,
    summary,
    // The duration rides only when the tool reported one.
    ...(durationSeconds !== undefined ? { durationSeconds } : {}),
  }
}

/**
 * The web-search pair: the query, and the links the tool found.
 *
 * A result with no `results` array is unreadable rather than empty -- the tool
 * answers with links, and a payload that carries none states something else.
 *
 * A FAILED search is neither, and the failure rung leads for that reason. It carries
 * no `results` array either, so it fell to the unparsed rung, which states that the
 * call completed and contradicts the row's own failed status.
 */
export function claudeWebSearchPayload(request: WebSearchRequest, result: ClaudeToolRow | undefined): ToolCallPayload<'web_search'> {
  if (!result)
    return { kind: 'web_search', request }
  const failure = claudeFailedResult(result)
  if (failure)
    return { kind: 'web_search', request, result: failure }
  const source = claudeWebSearchFromToolResult(result.toolUseResult)
  if (!source)
    return { kind: 'web_search', request, result: unparsedResult(result.resultContent) }
  return { kind: 'web_search', request, result: source }
}
