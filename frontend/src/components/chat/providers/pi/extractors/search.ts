import type { SearchResult } from '../../../ir/searchResult'
import { pickCounter, pickObject } from '~/lib/jsonPick'
import { grepMatches } from '../../grepOutput'
import { PI_SEARCH_TOOL } from '../protocol'
import { piExtractTool } from './toolCommon'

/**
 * Pi's own truncation notice: ONE bracketed line, after a blank line, at the very end
 * of the result text.
 *
 * The whole shape is anchored, because two independent tests are not enough. A result
 * whose last line holds a path such as `app/[id]/page.tsx` ends in `]` and holds a
 * blank line somewhere above it, so a pair of tests cuts every line from that blank
 * line onward and shows a fragment of the output as the notice.
 */
const PI_SEARCH_NOTICE = /\n\n\[([^\]\n]*)\]$/

export function extractPiSearch(payload: Record<string, unknown>): SearchResult | null {
  const tool = piExtractTool(payload)
  if (!tool || tool.isError || !Object.values<string>(PI_SEARCH_TOOL).includes(tool.toolName))
    return null
  const result = tool.result ?? tool.partialResult
  const text = result?.text ?? ''
  const details = result?.details
  const truncated = pickObject(details, 'truncation')?.truncated === true
    || details?.linesTruncated === true
    || ['matchLimitReached', 'resultLimitReached', 'entryLimitReached'].some(key => pickCounter(details, key) !== undefined)
  // Pi appends its own notice after a blank line. Only remove it when metadata confirms a limit.
  const noticeMatch = truncated ? PI_SEARCH_NOTICE.exec(text) : null
  const content = noticeMatch ? text.slice(0, noticeMatch.index) : text
  const notice = noticeMatch?.[1]
  if (tool.toolName !== PI_SEARCH_TOOL.Grep) {
    const empty = content === 'No files found matching pattern' || content === '(empty directory)' || content === ''
    const filenames = empty ? [] : content.split('\n').filter(line => line !== '')
    return {
      filenames,
      numFiles: filenames.length,
      numLines: 0,
      content: '',
      fallbackContent: empty ? '' : content,
      empty,
      truncated,
      ...(notice !== undefined ? { notice } : {}),
    }
  }
  const matches = grepMatches(content.split('\n'))
  const matchCount = matches.lines.length || (content === 'No matches found' || content === '' ? 0 : undefined)
  return {
    filenames: [],
    content,
    numFiles: matches.numFiles,
    numLines: matches.lines.length,
    ...(matchCount !== undefined ? { matchCount } : {}),
    fallbackContent: content,
    // The wording Pi prints for a grep that matched nothing, which the counter above
    // already reads. One spelling serves both.
    empty: content === 'No matches found' || content.trim() === '',
    truncated,
    ...(notice !== undefined ? { notice } : {}),
  }
}
