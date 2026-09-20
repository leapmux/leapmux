import type { SearchResult } from '../../../model/searchResult'
import type { ZCodeRow } from './toolCommon'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { searchOutputMode } from '../../../model/searchOutputMode'
import { zcodeExtractTool, zcodeToolInput } from './toolCommon'

const GLOB_TRUNCATION = '(Results are truncated. Consider using a more specific path or pattern.)'
const PAGINATION = 'limit: \\d+(?:, offset: \\d+)?|offset: \\d+'
const FILES_HEADER = new RegExp(`^Found (\\d+) files?(?: (${PAGINATION}))?\\n`)
const COUNT_FOOTER = new RegExp(`\\n\\nFound (\\d+) total occurrences? across (\\d+) files?\\.(?: with pagination = (${PAGINATION}))?$`)
const CONTENT_FOOTER = new RegExp(`\\n\\n\\[Showing results with pagination = (${PAGINATION})\\]$`)

/** Read ZCode's native search formatter without interpreting matches as metadata. */
export function extractZCodeSearch(row: ZCodeRow): SearchResult | null {
  const update = zcodeExtractTool(row.parsed)
  if (!update || update.isError || (row.toolName !== ZCODE_TOOL.Glob && row.toolName !== ZCODE_TOOL.Grep))
    return null
  const input = zcodeToolInput(row)
  const original = update.result?.content ?? ''
  const text = original
  const source: SearchResult = {
    filenames: [],
    content: '',
    numFiles: 0,
    numLines: 0,
    truncated: update.result?.truncated ?? false,
    fallbackContent: original,
    // POSITIVE EVIDENCE only: this build knows ZCode's empty wording for `glob`
    // alone, which the branch below states. No transcript in `testdata/` records
    // what the grep modes print when they match nothing, so every other branch
    // recognizes an empty result only from an empty body. Tighten this predicate
    // once a real transcript states that wording -- do not guess one.
    empty: text.trim() === '',
  }
  if (row.toolName === ZCODE_TOOL.Glob) {
    const lines = text ? text.split('\n').filter(line => line !== '') : []
    if (lines.at(-1) === GLOB_TRUNCATION) {
      lines.pop()
      source.truncated = true
    }
    // The wording ZCode prints for a glob that matched no file, which this branch
    // already reads to empty the list. The flag states the same fact for the row.
    const statedNothing = text === 'No files found'
    source.filenames = statedNothing ? [] : lines
    source.numFiles = source.filenames.length
    source.empty ||= statedNothing
    return source
  }
  // An unrecognized `output_mode` now narrows to `undefined` instead of keeping the
  // raw word; both still fall through to the content branch below, exactly as an
  // absent mode always did.
  const declared = pickString(input, 'output_mode')
  const mode = declared ? searchOutputMode(declared) : 'files_with_matches'
  // An unrecognized word leaves the mode unstated rather than explicitly undefined.
  if (mode !== undefined)
    source.mode = mode
  if (mode === 'files_with_matches') {
    const match = FILES_HEADER.exec(text)
    if (match) {
      const filenames = text.slice(match[0].length).split('\n').filter(line => line !== '')
      const count = Number(match[1])
      if (Number.isSafeInteger(count)) {
        source.filenames = filenames
        source.numFiles = count
        if (match[2] !== undefined)
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
        source.matchCount = matches
        source.numFiles = files
        if (match[3] !== undefined)
          source.notice = match[3]
      }
    }
    return source
  }
  // Context and multiline searches do not expose enough data for a reliable match count.
  const pagination = CONTENT_FOOTER.exec(text)
  source.content = pagination ? text.slice(0, pagination.index) : original
  if (pagination?.[1] !== undefined)
    source.notice = pagination[1]
  return source
}
