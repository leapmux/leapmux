import type { SearchResultSource } from '../../../results/searchResult'
import type { ZCodeRow } from './toolCommon'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeExtractTool, zcodeToolInput } from './toolCommon'

const GLOB_TRUNCATION = '(Results are truncated. Consider using a more specific path or pattern.)'
const PAGINATION = 'limit: \\d+(?:, offset: \\d+)?|offset: \\d+'
const FILES_HEADER = new RegExp(`^Found (\\d+) files?(?: (${PAGINATION}))?\\n`)
const COUNT_FOOTER = new RegExp(`\\n\\nFound (\\d+) total occurrences? across (\\d+) files?\\.(?: with pagination = (${PAGINATION}))?$`)
const CONTENT_FOOTER = new RegExp(`\\n\\n\\[Showing results with pagination = (${PAGINATION})\\]$`)

/** Read ZCode's native search formatter without interpreting matches as metadata. */
export function extractZCodeSearch(row: ZCodeRow): SearchResultSource | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update || update.isError || (row.toolName !== ZCODE_TOOL.Glob && row.toolName !== ZCODE_TOOL.Grep))
    return null
  const input = zcodeToolInput(row)
  const original = update.result?.content ?? ''
  const text = original
  const source: SearchResultSource = {
    variant: row.toolName === ZCODE_TOOL.Glob ? 'glob' : 'grep',
    pattern: pickString(input, 'pattern'),
    filenames: [],
    content: '',
    numFiles: 0,
    numLines: 0,
    truncated: update.result?.truncated ?? false,
    fallbackContent: original,
  }
  if (row.toolName === ZCODE_TOOL.Glob) {
    const lines = text ? text.split('\n').filter(line => line !== '') : []
    if (lines.at(-1) === GLOB_TRUNCATION) {
      lines.pop()
      source.truncated = true
    }
    source.filenames = text === 'No files found' ? [] : lines
    source.numFiles = source.filenames.length
    return source
  }
  const mode = pickString(input, 'output_mode') || 'files_with_matches'
  source.mode = mode
  if (mode === 'files_with_matches') {
    const match = FILES_HEADER.exec(text)
    if (match) {
      const filenames = text.slice(match[0].length).split('\n').filter(line => line !== '')
      const count = Number(match[1])
      if (Number.isSafeInteger(count)) {
        source.filenames = filenames
        source.numFiles = count
        source.notice = match[2]
      }
    }
    return source
  }
  if (mode === 'count') {
    const match = COUNT_FOOTER.exec(text)
    if (match) {
      const matches = Number(match[1])
      const files = Number(match[2])
      if (Number.isSafeInteger(matches) && Number.isSafeInteger(files)) {
        source.content = text.slice(0, match.index)
        source.numMatches = matches
        source.numFiles = files
        source.notice = match[3]
      }
    }
    return source
  }
  // Context and multiline searches do not expose enough data for a reliable match count.
  const pagination = CONTENT_FOOTER.exec(text)
  source.content = pagination ? text.slice(0, pagination.index) : original
  source.notice = pagination?.[1]
  return source
}
