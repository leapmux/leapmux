import type { SearchMatch } from '../../../model/searchResult'
import { MIMO_GREP_SKIPPED_NOTICE } from '../protocol'

/** The one sentence a MiMo glob and a MiMo grep print when they find nothing. */
export const MIMO_SEARCH_EMPTY = 'No files found'

/** What one MiMo grep found, and the notices it printed after the matches. */
export interface MiMoGrepListing {
  matches: SearchMatch[]
  /** The notices, without their parentheses, one after the other. */
  notice?: string
}

/**
 * Read MiMo's grouped grep output, or null when any row breaks its format.
 *
 * The format is a count heading, then each file on its own line followed by one
 * indented `Line N: text` row per match, with a blank line between files. Two
 * notices can follow, in this order: the truncation notice of a search that printed
 * its first page alone, and the notice of a search that could not read part of the
 * tree:
 *
 *     Found 3 matches (showing first 100)
 *     /p/a.ts:
 *       Line 4: const a = 1
 *
 *     (Results truncated: showing 100 of 130 matches (30 hidden). ...)
 *
 *     (Some paths were inaccessible and skipped)
 *
 * `count` is the total the tool reported in its metadata. A heading that states
 * another total is a body of some other shape, and the caller draws it as text.
 */
export function mimoGrepMatches(text: string, count: number): MiMoGrepListing | null {
  if (!Number.isSafeInteger(count) || count < 0)
    return null
  if (count === 0 && text.trim() === MIMO_SEARCH_EMPTY)
    return { matches: [] }
  const rows = text.split(/\r?\n/)
  const heading = /^Found (\d+) matches(?: \(showing first (\d+)\))?$/.exec(rows.shift() ?? '')
  if (!heading || Number(heading[1]) !== count)
    return null
  // The number of rows the tool printed: all of them, or the first page alone.
  const shown = heading[2] === undefined ? count : Number(heading[2])
  const matches: SearchMatch[] = []
  const notices: string[] = []
  let path = ''
  let pathHasMatch = false
  // Each notice ends the matches, and the skipped-paths notice ends the output.
  let truncationSeen = false
  let skippedSeen = false
  for (const row of rows) {
    if (row === '')
      continue
    if (skippedSeen)
      return null
    if (row === `(${MIMO_GREP_SKIPPED_NOTICE})`) {
      skippedSeen = true
      notices.push(MIMO_GREP_SKIPPED_NOTICE)
      continue
    }
    if (truncationSeen)
      return null
    if (heading[2] !== undefined && /^\(Results truncated: .*\)$/.test(row)) {
      truncationSeen = true
      notices.push(row.slice(1, -1))
      continue
    }
    const line = /^ {2}Line (\d+): (.*)$/.exec(row)
    if (line && path) {
      const lineNumber = Number(line[1])
      if (!Number.isSafeInteger(lineNumber) || lineNumber < 1)
        return null
      matches.push({ filePath: path, lineNumber, text: line[2] ?? '' })
      pathHasMatch = true
      continue
    }
    if (/^(?:\/|[a-z]:[\\/]|\\\\).+:$/i.test(row)) {
      if (path && !pathHasMatch)
        return null
      path = row.slice(0, -1)
      pathHasMatch = false
      continue
    }
    return null
  }
  if (matches.length !== Math.min(count, shown) || (path && !pathHasMatch))
    return null
  // The notice row shows one line, so the two notices join as two sentences.
  return { matches, ...(notices.length > 0 ? { notice: notices.join(' ') } : {}) }
}

/**
 * Read MiMo's glob output: one path per line, then a truncation notice when the tool
 * stopped at its limit. Null for text of another shape.
 */
export function mimoGlobFiles(text: string, count: number): { files: string[], notice?: string } | null {
  if (!Number.isSafeInteger(count) || count < 0)
    return null
  if (count === 0)
    return text.trim() === MIMO_SEARCH_EMPTY ? { files: [] } : null
  const rows = text.split(/\r?\n/).filter(row => row !== '')
  const last = rows[rows.length - 1] ?? ''
  const notice = /^\(Results are truncated: .*\)$/.test(last) ? last.slice(1, -1) : undefined
  const files = notice === undefined ? rows : rows.slice(0, -1)
  if (files.length !== count || files.some(file => file.startsWith('(')))
    return null
  return { files, ...(notice !== undefined ? { notice } : {}) }
}
