import type { WebSearchResultsSource } from '../../../results/webSearchResult'
import { pickNumber, pickString } from '~/lib/jsonPick'
import { extractWebSearchLinks, extractWebSearchSummary } from '../../../results/webSearchResult'

/**
 * Build a WebSearchResultsSource from a Claude `WebSearch` tool_result. Returns
 * null when the payload doesn't carry a `results` array — the caller should
 * fall through to the catch-all renderer.
 */
export function claudeWebSearchFromToolResult(
  toolUseResult: Record<string, unknown> | null | undefined,
): WebSearchResultsSource | null {
  if (!toolUseResult || !Array.isArray(toolUseResult.results))
    return null
  const links = extractWebSearchLinks(toolUseResult.results as unknown[])
  const summary = extractWebSearchSummary(toolUseResult.results as unknown[])
  return {
    links,
    summary,
    query: pickString(toolUseResult, 'query', undefined),
    durationSeconds: pickNumber(toolUseResult, 'durationSeconds', undefined),
  }
}
