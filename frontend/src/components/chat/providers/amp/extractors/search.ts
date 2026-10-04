import type { SearchMatch, SearchResult } from '../../../model/searchResult'
import { stringArray } from '~/lib/jsonPick'

/**
 * Amp's `Grep` and `glob` results.
 *
 * Each answers with a list: `glob` with one path for each file, and `Grep` with one
 * `path:line:text` entry for each match. The list reaches the stream as a JSON array
 * written as a string, or as plain text with one entry on each line. `Grep` states
 * `No results found.` when it matched nothing.
 */

/** Amp's words for a search that matched nothing. */
const NO_RESULTS = 'No results found.'

/** One `path:line:text` entry of a `Grep` result. */
const GREP_ENTRY = /^(.+?):(\d+):(.*)$/

/** The entries of one result: the JSON list, else the lines of the text. */
function resultEntries(text: string): string[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  }
  catch {
    parsed = undefined
  }
  if (Array.isArray(parsed))
    return stringArray(parsed)
  const trimmed = text.trim()
  if (trimmed === '' || trimmed === NO_RESULTS)
    return []
  return trimmed.replace(/\r\n/g, '\n').split('\n').filter(line => line.trim() !== '')
}

/** One finished `glob`: the files it matched. */
export function ampGlobResult(text: string): SearchResult {
  const filenames = resultEntries(text)
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
 * One finished `Grep`: the matching lines, and the files that hold them.
 *
 * An entry that is not a `path:line:text` match -- a note Amp adds after the matches --
 * stays in the text the body can expand, and it is not counted.
 */
export function ampGrepResult(text: string): SearchResult {
  const entries = resultEntries(text)
  const lines: SearchMatch[] = entries.flatMap((entry) => {
    const match = GREP_ENTRY.exec(entry)
    return match?.[1] !== undefined && match[2] !== undefined
      ? [{ filePath: match[1], lineNumber: Number(match[2]), text: match[3] ?? '' }]
      : []
  })
  const filenames = [...new Set(lines.map(line => line.filePath))]
  return {
    filenames,
    content: entries.join('\n'),
    lines,
    numFiles: filenames.length,
    numLines: lines.length,
    truncated: false,
    fallbackContent: text,
    empty: entries.length === 0,
  }
}
