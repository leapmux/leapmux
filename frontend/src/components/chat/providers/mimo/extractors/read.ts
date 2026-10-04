import type { NumberedFileLine, ReadReminder } from '../../../model/readFileResult'

/**
 * What one MiMo `read` answered, in its own output format.
 *
 * MiMo writes a read as tagged text: `<path>`, `<type>`, then the body. A file body
 * sits inside `<content>` with each line as `N: text`, and a directory body sits
 * inside `<entries>` with one name for each line. Each body ends with a notice in
 * parentheses -- the line range, or the entry count -- and a file read can carry a
 * `<system-reminder>` after the body, with the instructions MiMo loaded for the file.
 *
 * The body is parsed only when EVERY line matches that format. Anything else is a
 * body this build cannot read, and the caller draws the text as it stands.
 */
export type MiMoReadBody
  = | {
    type: 'file'
    path: string
    lines: NumberedFileLine[]
    /** The notice after the lines: the range shown, or the end of the file. */
    notice?: string
    trailing: ReadReminder[]
  }
  | {
    type: 'directory'
    path: string
    entries: string[]
    /** The whole directory's size, when the read showed only part of it. */
    totalEntries?: number
    /** The first entry shown, counted from one, when the read skipped entries. */
    offset?: number
    truncated: boolean
  }

const PATH_LINE = /^<path>(.*)<\/path>$/
const TYPE_LINE = /^<type>(file|directory)<\/type>$/
const NUMBERED_LINE = /^(\d+): (.*)$/
/** `(Showing 20 of 45 entries. Use 'offset' parameter to read beyond entry 25)` */
const PARTIAL_ENTRIES = /^\(Showing (\d+) of (\d+) entries\. Use 'offset' parameter to read beyond entry (\d+)\)$/
/** `(45 entries)` */
const ALL_ENTRIES = /^\(\d+ entries\)$/

/** Read one MiMo `read` output, or null for text that is not in its format. */
export function parseMiMoRead(output: string): MiMoReadBody | null {
  const rows = output.split('\n')
  const path = PATH_LINE.exec(rows[0] ?? '')?.[1]
  const type = TYPE_LINE.exec(rows[1] ?? '')?.[1]
  if (path === undefined || type === undefined)
    return null
  return type === 'file' ? parseFileBody(path, rows.slice(2)) : parseDirectoryBody(path, rows.slice(2))
}

function parseFileBody(path: string, rows: string[]): MiMoReadBody | null {
  if (rows[0] !== '<content>')
    return null
  const close = rows.indexOf('</content>')
  if (close < 0)
    return null
  const body = rows.slice(1, close)
  // The notice is the last non-empty line before the close, after one blank line.
  let end = body.length
  while (end > 0 && body[end - 1] === '')
    end--
  let notice: string | undefined
  const last = body[end - 1]
  if (last !== undefined && last.startsWith('(') && last.endsWith(')') && !NUMBERED_LINE.test(last)) {
    notice = last.slice(1, -1)
    end--
    while (end > 0 && body[end - 1] === '')
      end--
  }
  const lines: NumberedFileLine[] = []
  for (const row of body.slice(0, end)) {
    const match = NUMBERED_LINE.exec(row)
    if (!match)
      return null
    lines.push({ num: Number(match[1]), text: match[2] ?? '' })
  }
  const trailing = parseReminders(rows.slice(close + 1))
  if (trailing === null)
    return null
  return { type: 'file', path, lines, ...(notice !== undefined ? { notice } : {}), trailing }
}

function parseDirectoryBody(path: string, rows: string[]): MiMoReadBody | null {
  if (rows[0] !== '<entries>')
    return null
  const close = rows.lastIndexOf('</entries>')
  if (close < 0)
    return null
  const body = rows.slice(1, close).filter(row => row !== '')
  const summary = body.pop() ?? ''
  const partial = PARTIAL_ENTRIES.exec(summary)
  if (partial) {
    const shown = Number(partial[1])
    const total = Number(partial[2])
    const beyond = Number(partial[3])
    return {
      type: 'directory',
      path,
      entries: body,
      totalEntries: total,
      ...(beyond - shown > 1 ? { offset: beyond - shown } : {}),
      truncated: true,
    }
  }
  if (!ALL_ENTRIES.test(summary))
    return null
  return { type: 'directory', path, entries: body, truncated: false }
}

/**
 * The tag blocks after a file body, or null when anything else follows it.
 *
 * MiMo appends one `<system-reminder>` with the instructions it loaded for the file.
 */
function parseReminders(rows: string[]): ReadReminder[] | null {
  const reminders: ReadReminder[] = []
  let index = 0
  while (index < rows.length) {
    const row = rows[index] ?? ''
    if (row === '') {
      index++
      continue
    }
    const open = /^<([a-z][\w-]*)>$/i.exec(row)
    if (!open)
      return null
    const tag = open[1] ?? ''
    const closeAt = rows.indexOf(`</${tag}>`, index + 1)
    if (closeAt < 0)
      return null
    reminders.push({ label: reminderLabel(tag), text: rows.slice(index + 1, closeAt).join('\n').trim() })
    index = closeAt + 1
  }
  return reminders
}

/** Title-case a tag name: `system-reminder` -> "System Reminder". */
function reminderLabel(tag: string): string {
  return tag.split(/[-_]+/).filter(Boolean).map(word => word.charAt(0).toUpperCase() + word.slice(1)).join(' ')
}
