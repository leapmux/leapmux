import type { ToolCallSpecVariant } from '../../../model/toolCall'
import type { WebSearchRequest, WebSearchResult } from '../../../model/tools/webSearch'
import type { ClaudeToolRow } from './toolCommon'
import { isObject, pickNumber } from '~/lib/jsonPick'
import { unparsedResult } from '../../../model/toolCall'
import { extractWebSearchLinks } from '../../webSearchLinks'
import { claudeToolFailureResult } from './failure'

/** Extract the final non-blank summary from Claude's WebSearch entries. */
function extractWebSearchSummary(results: unknown[]): string {
  for (let index = results.length - 1; index >= 0; index--) {
    const result = results[index]
    if (typeof result === 'string' && result.trim().length > 0)
      return result.trim()
  }
  return ''
}

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
  const rawLinks = toolUseResult.results.flatMap(item => (isObject(item) && Array.isArray(item.content)
    ? item.content
    : []))
  const links = extractWebSearchLinks(rawLinks)
  const summary = extractWebSearchSummary(toolUseResult.results)
  const durationSeconds = pickNumber(toolUseResult, 'durationSeconds', undefined)
  return {
    links,
    summary,
    // The duration rides only when the tool reported one.
    ...(durationSeconds !== undefined
      ? { durationSeconds }
      : {}),
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
export function claudeWebSearchSpec(request: WebSearchRequest, result: ClaudeToolRow | undefined): ToolCallSpecVariant<'web_search'> {
  if (!result)
    return { kind: 'web_search', request }
  const failure = claudeToolFailureResult(result)
  if (failure)
    return { kind: 'web_search', request, result: failure }
  const source = claudeWebSearchFromToolResult(result.toolUseResult)
  if (!source) {
    return {
      kind: 'web_search',
      request,
      result: unparsedResult(result.resultContent),
    }
  }
  return { kind: 'web_search', request, result: source }
}
