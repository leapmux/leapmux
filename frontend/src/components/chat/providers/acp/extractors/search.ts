import type { SearchResultSource } from '../../../results/searchResult'
import { pickNumber } from '~/lib/jsonPick'
import { collectAcpToolText, pickAcpRawOutputMetadata } from '../rendering'

/**
 * Build a SearchResultSource from an ACP `tool_call_update` of kind `search`.
 * Returns null when no recognizable shape is present so callers can fall
 * through to the generic text branch.
 */
export function acpSearchFromToolCall(toolUse: Record<string, unknown> | null | undefined): SearchResultSource | null {
  if (!toolUse)
    return null
  const matches = pickNumber(pickAcpRawOutputMetadata(toolUse), 'matches', undefined)
  const text = collectAcpToolText(toolUse)

  if (matches === undefined && !text)
    return null

  return {
    variant: 'search',
    filenames: [],
    content: '',
    numFiles: 0,
    numLines: 0,
    matches,
    truncated: false,
    fallbackContent: text,
  }
}
