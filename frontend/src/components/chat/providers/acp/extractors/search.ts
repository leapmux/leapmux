import type { SearchResult } from '../../../model/searchResult'
import { pickNumber } from '~/lib/jsonPick'
import { collectAcpToolText, pickAcpRawOutputMetadata } from '../content'

/**
 * Build a SearchResult from an ACP `tool_call_update` of kind `search`.
 * Returns null when no recognizable shape is present so callers can fall
 * through to the generic text branch.
 */
export function acpSearchFromToolCall(toolUse: Record<string, unknown> | null | undefined): SearchResult | null {
  if (!toolUse)
    return null
  const matches = pickNumber(pickAcpRawOutputMetadata(toolUse), 'matches', undefined)
  const text = collectAcpToolText(toolUse, { rawObjects: false })

  if (matches === undefined && !text)
    return null

  return {
    filenames: [],
    content: '',
    numFiles: 0,
    numLines: 0,
    ...(matches !== undefined ? { matchCount: matches } : {}),
    truncated: false,
    fallbackContent: text,
    // The protocol states a `matches` total rather than a sentence, and the shared
    // tables read no empty wording for any provider of this family. An empty body is
    // the one empty result this build can recognize. A stated zero reaches the row
    // through `matchCount`, which the search summary words on its own.
    empty: text.trim() === '',
  }
}
