import type { SearchResultSource } from '../../../results/searchResult'
import { pickCounter, pickObject, pickString } from '~/lib/jsonPick'
import { PI_SEARCH_TOOL } from '../protocol'
import { piExtractTool } from './toolCommon'

export function extractPiSearch(payload: Record<string, unknown>): SearchResultSource | null {
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
  const noticeStart = truncated ? text.lastIndexOf('\n\n[') : -1
  const hasNotice = noticeStart >= 0 && text.endsWith(']')
  const content = hasNotice ? text.slice(0, noticeStart) : text
  const notice = hasNotice ? text.slice(noticeStart + 3, -1) : undefined
  if (tool.toolName !== PI_SEARCH_TOOL.Grep) {
    const empty = content === 'No files found matching pattern' || content === '(empty directory)' || content === ''
    const filenames = empty ? [] : content.split('\n').filter(line => line !== '')
    return {
      variant: 'glob',
      filenames,
      numFiles: filenames.length,
      numLines: 0,
      content: '',
      fallbackContent: empty ? '' : content,
      truncated,
      notice,
    }
  }
  const matches = content.split('\n').filter(line => /^.+:\d+:/.test(line)).length
  return {
    variant: 'search',
    pattern: pickString(tool.args, 'pattern'),
    filenames: [],
    content,
    numFiles: 0,
    numLines: matches,
    matches: matches || (content === 'No matches found' || content === '' ? 0 : undefined),
    fallbackContent: content,
    truncated,
    notice,
  }
}
