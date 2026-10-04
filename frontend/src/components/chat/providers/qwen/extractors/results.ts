import type { ReadFileResult } from '../../../model/readFileResult'
import type { FileListEntry, SearchMatch, SearchResult } from '../../../model/searchResult'
import type { ListResult } from '../../../model/tools/list'
import { readFileResultFromContent } from '../../../model/readFileResult'

// Readers of the text Qwen Code's own tools write for the model. Each tool states a
// fixed header and a fixed layout (`packages/core/src/tools/*.ts` in qwen-code), so a
// reader matches that layout exactly and answers null for any other text: the row
// then keeps the words Qwen printed rather than a guess.

/** The notice Qwen puts before a part of a file it did not read whole. */
const READ_PARTIAL = /^Showing lines (\d+)-\d+ of (?:at least )?\d+ total lines\.\n\n---\n\n/

/**
 * The file one `read_file` returned.
 *
 * Qwen returns the text of the file with no line numbers. A read that starts at an
 * offset states the offset in its arguments, 0-based, and a truncated read states the
 * first line it shows in the notice before the text. The notice stays beside the
 * body, where the reader sees why the file is not whole.
 */
export function qwenReadResult(text: string, offset: number | null): ReadFileResult {
  const partial = READ_PARTIAL.exec(text)
  const body = partial ? text.slice(partial[0].length) : text
  const startLine = partial ? Number(partial[1]) : (offset ?? 0) + 1
  const content = body.endsWith('\n') ? body.slice(0, -1) : body
  const result = readFileResultFromContent({ content, startLine, fallbackContent: text })
  return partial
    ? { ...result, leading: [{ label: 'Partial view', text: partial[0].split('\n')[0] ?? '' }] }
    : result
}

/** The header of a grep that found matches, and the sentence of one that found none. */
const GREP_FOUND = /^Found (\d+) match(?:es)? for pattern [^\n]*:\n---\n/
const GREP_NONE = /^No matches found for pattern /
const GREP_FILE = /^File: (.+)$/
const GREP_LINE = /^L(\d+): (.*)$/

/**
 * The matches one `grep_search` states.
 *
 * Qwen groups them by file: a `File:` line, then one `L<number>:` line for each
 * match, then `---`. A truncation notice may follow the last group.
 */
export function qwenGrepResult(text: string): SearchResult | null {
  if (GREP_NONE.test(text))
    return { filenames: [], content: '', lines: [], numFiles: 0, numLines: 0, matchCount: 0, truncated: false, fallbackContent: '', empty: true }
  const header = GREP_FOUND.exec(text)
  if (!header)
    return null
  const lines: SearchMatch[] = []
  let file = ''
  let truncated = false
  for (const line of text.slice(header[0].length).split('\n')) {
    const fileMatch = GREP_FILE.exec(line)
    const lineMatch = GREP_LINE.exec(line)
    if (fileMatch)
      file = fileMatch[1] ?? ''
    else if (lineMatch && file)
      lines.push({ filePath: file, lineNumber: Number(lineMatch[1]), text: lineMatch[2] ?? '' })
    else if (line.includes('truncated]') || line.endsWith('...'))
      truncated = true
  }
  const filenames = [...new Set(lines.map(line => line.filePath))]
  return {
    filenames,
    content: '',
    lines,
    numFiles: filenames.length,
    numLines: lines.length,
    matchCount: Number(header[1]),
    truncated,
    fallbackContent: text,
    empty: false,
  }
}

/** The header of a glob that found files, and the sentence of one that found none. */
const GLOB_FOUND = /^Found (?:at least )?\d+ file\(s\) matching [^\n]*:\n---\n/
const GLOB_NONE = /^No files found matching pattern /

/** The files one `glob` found, newest first as Qwen sorts them. */
export function qwenGlobResult(text: string): SearchResult | null {
  if (GLOB_NONE.test(text))
    return { filenames: [], content: '', numFiles: 0, numLines: 0, truncated: false, fallbackContent: '', empty: true }
  const header = GLOB_FOUND.exec(text)
  if (!header)
    return null
  const [list = '', notice] = text.slice(header[0].length).split('\n---\n')
  const filenames = list.split('\n').filter(line => line.trim() !== '')
  return {
    filenames,
    content: '',
    numFiles: filenames.length,
    numLines: 0,
    truncated: notice !== undefined,
    ...(notice !== undefined ? { notice: notice.trim() } : {}),
    fallbackContent: text,
    empty: false,
  }
}

/** The header of one directory listing. */
const LIST_HEADER = /^Listed (\d+) item\(s\) in [^\n]*:\n---\n/
const LIST_DIRECTORY_MARK = '[DIR] '

/**
 * The entries one `list_directory` returned.
 *
 * Qwen marks a directory with `[DIR]` before its name. The row states a directory
 * with a trailing `/`, the way every other listing does. A truncation notice and a
 * count of ignored files may follow the entries.
 */
export function qwenListResult(text: string): ListResult | null {
  const header = LIST_HEADER.exec(text)
  if (!header)
    return null
  const rest = text.slice(header[0].length)
  const [list = '', ...tail] = rest.split(/\n---\n|\n\n/)
  const entries: FileListEntry[] = list.split('\n').filter(line => line.trim() !== '').map(line => line.startsWith(LIST_DIRECTORY_MARK)
    ? { path: `${line.slice(LIST_DIRECTORY_MARK.length)}/` }
    : { path: line })
  const notice = tail.map(part => part.trim()).filter(Boolean).join('\n')
  return {
    entries,
    totalEntries: Number(header[1]),
    ...(entries.length < Number(header[1]) ? { truncated: true } : {}),
    ...(notice ? { notice } : {}),
  }
}
