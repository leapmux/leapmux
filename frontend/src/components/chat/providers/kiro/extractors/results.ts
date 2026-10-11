import type { CommandExit } from '../../../model/commandResult'
import type { FileListEntry, SearchMatch, SearchResult } from '../../../model/searchResult'
import type { ListResult } from '../../../model/tools/list'
import type { TodoItem } from '~/models/todo'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'

/**
 * The words Kiro prints and a reader here parses. Kiro prints each of them itself, so
 * they are Kiro's own format. No other program reads them, so they are not in the
 * contract.
 */
const KIRO_SEARCH_PREFIX = /^You searched for .* and received the following (?:(complete|incomplete) )?results:\n/
const KIRO_NO_GREP_MATCHES = 'No matches found.'
const KIRO_NO_FILES = 'No files found matching your search.'
const KIRO_GREP_TRUNCATED = '... [truncated: too many matches] ...'
const KIRO_LISTING_HEADER = /^Contents of .+:$/
const KIRO_LISTING_ENTRY = /^ {2}\[(FILE|DIR)\] (.+)$/
const KIRO_EMPTY_DIRECTORY = /^Directory .+ is empty or does not exist\.$/
/** One grep line: the line number, `:` for a match or `-` for a context line, then the text. */
const KIRO_GREP_LINE = /^(\d+)([:-])(.*)$/

/** The `rawOutput` of one Kiro call, when it states an object. */
export function kiroRawOutput(tool: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(tool, 'rawOutput') ?? undefined
}

/**
 * How one shell command ended, from the output Kiro states.
 *
 * Kiro writes the exit code beside the output. A command that a signal ended
 * states no code, so it answers nothing.
 */
export function kiroCommandExit(tool: Record<string, unknown>): CommandExit | undefined {
  const code = pickNumber(kiroRawOutput(tool), 'exitCode')
  return code !== null ? { exitCode: code } : undefined
}

/**
 * What one shell command printed, as Kiro states it apart from its summary.
 *
 * The call's content states `Output:` and `Exit Code:` around the output, which is
 * what the model reads. `rawOutput.output` is the output alone.
 */
export function kiroCommandOutput(tool: Record<string, unknown>): string | undefined {
  const raw = kiroRawOutput(tool)
  return raw && typeof raw.output === 'string' ? raw.output : undefined
}

/**
 * The files one `File Search` found.
 *
 * Kiro prints the files between two `---` lines, newest first, after a sentence that
 * states whether it cut the list. Returns null for text that is not that shape, so
 * the caller keeps the words Kiro printed.
 */
export function kiroFileSearchResult(text: string): SearchResult | null {
  const header = KIRO_SEARCH_PREFIX.exec(text)
  if (!header)
    return null
  const body = text.slice(header[0].length)
  const lines = body.split('\n')
  if (lines[0] !== '---')
    return null
  const end = lines.indexOf('---', 1)
  if (end < 0)
    return null
  const listed = lines.slice(1, end).filter(line => line !== '')
  const filenames = listed.length === 1 && listed[0] === KIRO_NO_FILES ? [] : listed
  return {
    filenames,
    content: '',
    numFiles: filenames.length,
    numLines: 0,
    truncated: header[1] === 'incomplete',
    fallbackContent: text,
    empty: filenames.length === 0,
  }
}

/**
 * The matches one `Grep Search` found.
 *
 * Kiro prints each file's path on a line of its own, then one line for each match
 * (`12:text`) or context line (`11-text`), with an empty line between two files.
 * Returns null for text that is not that shape, so the caller keeps the words Kiro
 * printed.
 */
export function kiroGrepResult(text: string): SearchResult | null {
  const header = KIRO_SEARCH_PREFIX.exec(text)
  if (!header)
    return null
  const body = text.slice(header[0].length).replace(/\n+$/, '')
  if (body === KIRO_NO_GREP_MATCHES)
    return { filenames: [], content: '', lines: [], numFiles: 0, numLines: 0, matchCount: 0, truncated: false, fallbackContent: text, empty: true }
  const lines: SearchMatch[] = []
  const filenames: string[] = []
  let filePath = ''
  let truncated = false
  for (const line of body.split('\n')) {
    if (line === '') {
      filePath = ''
      continue
    }
    if (line === KIRO_GREP_TRUNCATED) {
      truncated = true
      continue
    }
    const match = filePath ? KIRO_GREP_LINE.exec(line) : null
    if (!match) {
      // A line that is no match line opens the next file.
      filePath = line
      filenames.push(line)
      continue
    }
    // A context line is no match, so it states no line of its own.
    if (match[2] === ':')
      lines.push({ filePath, lineNumber: Number(match[1]), text: match[3] ?? '' })
  }
  if (filenames.length === 0)
    return null
  return {
    filenames,
    content: '',
    lines,
    numFiles: filenames.length,
    numLines: lines.length,
    matchCount: lines.length,
    truncated,
    fallbackContent: text,
    empty: lines.length === 0,
  }
}

/**
 * The entries one `List Directory` states.
 *
 * Kiro prints `Contents of <path>:` and then one `[FILE]` or `[DIR]` line for each
 * entry, or one sentence for an empty or absent directory. The line breaks at the
 * end are no entry. A recursive listing takes another format, and this returns null
 * for it, so the caller keeps the words Kiro printed.
 */
export function kiroListResult(text: string): ListResult | null {
  const lines = text.replace(/\n+$/, '').split('\n')
  if (lines.length === 1 && KIRO_EMPTY_DIRECTORY.test(lines[0] ?? ''))
    return { entries: [], notice: lines[0] ?? '' }
  if (!KIRO_LISTING_HEADER.test(lines[0] ?? ''))
    return null
  const entries: FileListEntry[] = []
  for (const line of lines.slice(1)) {
    const entry = KIRO_LISTING_ENTRY.exec(line)
    if (!entry)
      return null
    entries.push({ path: entry[1] === 'DIR' ? `${entry[2]}/` : entry[2] ?? '' })
  }
  return { entries }
}

/**
 * The tasks of Kiro's to-do list, from the `tasks` list that each finished
 * `Task List` call states.
 *
 * A task is done or not: Kiro has no state for a task that runs. A task with no text
 * is no item. The `details` of a task are its long-form description.
 */
export function kiroTodoItems(tasks: unknown): TodoItem[] {
  if (!Array.isArray(tasks))
    return []
  return tasks.filter(isObject).flatMap((task, index) => {
    const content = pickString(task, 'task_description').trim()
    if (!content)
      return []
    const details = pickString(task, 'details').trim()
    const id = pickString(task, 'id')
    return [{
      ...(id ? { id } : {}),
      rowKey: todoRowKey(id || undefined, index, content),
      content,
      activeForm: '',
      ...(details ? { description: details } : {}),
      status: task.completed === true ? 'completed' as const : 'pending' as const,
    }]
  })
}

/**
 * The tasks that one `Task List` call ASKED to create or add.
 *
 * The model sends them as a list, and Kiro's own record of the call states the same
 * tasks as an object keyed by position (`{"0": {...}, "1": {...}}`). Both read here.
 * Every other command of the tool asks about tasks by id, and states no task of its
 * own.
 */
export function kiroRequestedTodoItems(input: Record<string, unknown>): TodoItem[] {
  if (Array.isArray(input.tasks))
    return kiroTodoItems(input.tasks)
  const tasks = pickObject(input, 'tasks')
  if (!tasks)
    return []
  const ordered = Object.entries(tasks)
    .filter(([key]) => /^\d+$/.test(key))
    .sort(([a], [b]) => Number(a) - Number(b))
    .map(([, task]) => task)
  return kiroTodoItems(ordered)
}
