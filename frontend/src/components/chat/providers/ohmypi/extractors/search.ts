import type { SearchMatch, SearchResult } from '../../../model/searchResult'
import { isObject, pickNumber, pickString, stringArray } from '~/lib/jsonPick'

/**
 * One header of omp's folded file tree (`formatGroupedFiles`): one `#` for each level,
 * then a directory (`src/`, or a folded chain such as `packages/pkg/src/`) or a file.
 */
const TREE_HEADER = /^(#+) (.+)$/
/** The snapshot tag that the hashline edit adds to the name in a file's header. */
const TREE_HEADER_TAG = /#[0-9A-F]{4}$/i
/** The header omp prints above the matches of a single-file scope in hashline mode: `[PATH#TAG]`. */
const SCOPE_HEADER = /^\[(.+)#[0-9A-F]{4}\]$/i
/**
 * One line of a file's matches (`formatMatchLine`): `*12:text` for a match and
 * ` 13:text` for context in hashline mode, with `|` in place of `:` in the other modes.
 */
const GREP_LINE = /^([* ])(\d+)[:|](.*)$/

/** A name from a header, under the directory of the level above it. */
function underDirectory(directory: string, name: string): string {
  if (name === '' || name === '.')
    return directory
  return directory ? `${directory}/${name}` : name
}

/**
 * The matches one `grep` printed, one per matching line.
 *
 * omp prints a search of a directory, or of several paths, as a folded tree: one `#`
 * for each level, a directory header ending in `/`, and a file header that states the
 * file's name alone. A file's path is the directories above it and then its name. A
 * stack keyed by the level holds those directories, as omp's own
 * `classifyGroupedLines` does.
 *
 * A search of one file prints no tree: a `[PATH#TAG]` header in hashline mode, and no
 * header in the other modes. `scopeFile` is the file such a search states, or '' when
 * it states none.
 *
 * Only the matching lines are matches; the context lines stay in the text the body
 * can expand.
 */
function grepMatches(text: string, scopeFile: string): SearchMatch[] {
  const matches: SearchMatch[] = []
  // directories[n] is the directory that the header of level n + 1 opened.
  const directories: string[] = []
  let filePath = scopeFile
  for (const row of text.replace(/\r\n/g, '\n').split('\n')) {
    const header = TREE_HEADER.exec(row)
    if (header?.[1] !== undefined && header[2] !== undefined) {
      const level = header[1].length
      const name = header[2].trimEnd()
      const parent = level > 1 ? directories[level - 2] ?? '' : ''
      // A header closes every directory at its own level and below.
      directories.length = Math.min(directories.length, level - 1)
      if (name.endsWith('/')) {
        directories[level - 1] = underDirectory(parent, name.slice(0, -1))
        filePath = ''
      }
      else {
        filePath = underDirectory(parent, name.replace(TREE_HEADER_TAG, ''))
      }
      continue
    }
    const scope = SCOPE_HEADER.exec(row)
    if (scope?.[1] !== undefined) {
      filePath = scope[1]
      continue
    }
    const line = GREP_LINE.exec(row)
    if (!filePath || line?.[1] !== '*' || line[2] === undefined)
      continue
    matches.push({ filePath, lineNumber: Number(line[2]), text: line[3] ?? '' })
  }
  return matches
}

/**
 * One finished `grep`: the files, the matching lines, and omp's own counts.
 *
 * `details.files` and `details.matchCount` are omp's own statement of what it found,
 * so an empty result is one whose count is zero, never a text the body failed to read.
 */
export function ohMyPiGrepResult(text: string, details: Record<string, unknown>): SearchResult {
  const filenames = stringArray(details.files)
  // A search of one file prints its matches with no header in the plain modes, and
  // `details.files` then states that one file.
  const lines = grepMatches(text, filenames.length === 1 ? filenames[0] ?? '' : '')
  const matchCount = pickNumber(details, 'matchCount', undefined)
  const numFiles = pickNumber(details, 'fileCount', filenames.length)
  return {
    filenames,
    content: text,
    lines,
    numFiles,
    numLines: lines.length,
    ...(matchCount !== undefined ? { matchCount } : {}),
    truncated: details.truncated === true,
    fallbackContent: text,
    empty: matchCount === 0 || (matchCount === undefined && numFiles === 0 && lines.length === 0),
  }
}

/** One finished `glob`: the files it matched. */
export function ohMyPiGlobResult(text: string, details: Record<string, unknown>): SearchResult {
  const filenames = stringArray(details.files)
  const numFiles = pickNumber(details, 'fileCount', filenames.length)
  return {
    filenames,
    content: '',
    numFiles,
    numLines: 0,
    truncated: details.truncated === true || details.resultLimitReached === true,
    fallbackContent: text,
    empty: numFiles === 0,
  }
}

/**
 * One finished `find` or `ast_grep`: a search whose matches omp states as text alone.
 *
 * The body prints the text unchanged. When a result lists files in `details`, the
 * summary counts them.
 */
export function ohMyPiTextSearchResult(text: string, details: Record<string, unknown>): SearchResult {
  const filenames = isObject(details) ? stringArray(details.files) : []
  return {
    filenames,
    content: text,
    numFiles: filenames.length,
    numLines: 0,
    truncated: details.truncated === true,
    fallbackContent: text,
    empty: text.trim() === '' && filenames.length === 0,
  }
}

/** The pattern a search call asked for, in the key its tool spells it. */
export function ohMyPiSearchPattern(args: Record<string, unknown>): string {
  return pickString(args, 'pattern') || pickString(args, 'query')
}
