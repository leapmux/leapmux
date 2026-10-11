import type { SearchMatch, SearchResult } from '../../../model/searchResult'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { isObject, pickBool, pickNumber, pickString } from '~/lib/jsonPick'

/**
 * The readers of the Codewhale searches: `grep_files`, `file_search`, and the
 * searches of a corpus that is not the file tree.
 *
 * Each answers `null` for a text that is not the document it expects, and the caller
 * then keeps the text as the tool printed it. A failed call answers its reason in
 * plain text, so the null is the normal answer there rather than a parse failure.
 */

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text)
  }
  catch {
    return undefined
  }
}

/**
 * A `grep_files` answer: `{matches:[{file, line_number, line}], total_matches,
 * files_searched, truncated}`.
 *
 * The matches keep their path apart from their text, so a line that holds a colon
 * never splits into a second path.
 */
export function codewhaleGrepResult(text: string): SearchResult | null {
  const document = parseJson(text)
  if (!isObject(document) || !Array.isArray(document.matches))
    return null
  const lines: SearchMatch[] = document.matches.filter(isObject).flatMap((match) => {
    const filePath = pickString(match, 'file')
    if (!filePath)
      return []
    const lineNumber = pickNumber(match, 'line_number')
    return [{ filePath, ...(lineNumber !== null ? { lineNumber } : {}), text: pickString(match, 'line') }]
  })
  const filenames = [...new Set(lines.map(line => line.filePath))]
  const total = pickNumber(document, 'total_matches')
  return {
    filenames,
    content: '',
    lines,
    numFiles: filenames.length,
    numLines: lines.length,
    ...(total !== null ? { matchCount: total } : {}),
    truncated: pickBool(document, 'truncated'),
    fallbackContent: text,
    empty: lines.length === 0,
  }
}

/** A `file_search` answer: `[{path, name, score}]`, best match first. */
export function codewhaleFileSearchResult(text: string): SearchResult | null {
  const document = parseJson(text)
  if (!Array.isArray(document))
    return null
  const filenames = document.filter(isObject).map(match => pickString(match, 'path')).filter(Boolean)
  return {
    filenames,
    content: '',
    numFiles: filenames.length,
    numLines: 0,
    truncated: false,
    fallbackContent: text,
    empty: filenames.length === 0,
  }
}

/**
 * A `tool_search` answer: `{tool_references:[{tool_name}], unavailable_tool_references}`.
 *
 * The corpus is the tool registry, so the matches are tool NAMES and not files. They
 * ride in `content`, one to a line, which the search body prints unchanged.
 */
function codewhaleToolSearchResult(text: string): SearchResult | null {
  const document = parseJson(text)
  if (!isObject(document) || !Array.isArray(document.tool_references))
    return null
  const names = document.tool_references.filter(isObject).map(reference => pickString(reference, 'tool_name')).filter(Boolean)
  return {
    filenames: [],
    content: names.join('\n'),
    numFiles: 0,
    numLines: names.length,
    matchCount: names.length,
    truncated: false,
    fallbackContent: text,
    empty: names.length === 0,
  }
}

/**
 * The answer of a `search` call: the registry match list of `tool_search`, and the
 * text any other search printed.
 *
 * A search over a corpus that is not the file tree states its matches in `content`,
 * which is exactly the text the language server answered with.
 */
export function codewhaleCorpusSearchResult(toolName: string, text: string): SearchResult {
  const structured = toolName === CODEWHALE_TOOL.ToolSearch ? codewhaleToolSearchResult(text) : null
  if (structured)
    return structured
  const lines = text ? text.split('\n').filter(line => line.trim() !== '') : []
  return {
    filenames: [],
    content: text,
    numFiles: 0,
    numLines: lines.length,
    truncated: false,
    fallbackContent: text,
    empty: lines.length === 0,
  }
}
