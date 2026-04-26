import type { SearchResultSource } from '../../results/searchResult'
import { isObject, pickNumber } from '~/lib/jsonPick'
import { collectAcpToolText } from '../shared/acpRendering'

/**
 * Build a SearchResultSource from an ACP `tool_call_update` of kind `search`.
 * Returns null when no recognizable shape is present so callers can fall
 * through to the generic text branch.
 */
export function acpSearchFromToolCall(toolUse: Record<string, unknown> | null | undefined): SearchResultSource | null {
  if (!toolUse)
    return null
  const rawOutput = isObject(toolUse.rawOutput) ? toolUse.rawOutput as Record<string, unknown> : null
  const metadata = rawOutput && isObject(rawOutput.metadata) ? rawOutput.metadata as Record<string, unknown> : null
  const matches = pickNumber(metadata, 'matches', undefined)

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
