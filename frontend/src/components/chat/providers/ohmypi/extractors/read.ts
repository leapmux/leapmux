import type { NumberedFileLine, ReadFileResult, ReadReminder } from '../../../model/readFileResult'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'

/**
 * The header omp prints above a file in hashline mode: `[PATH#TAG]`, where TAG
 * identifies the snapshot that a later edit must match. The other edit modes print no
 * header (`tools/read.ts`: the header context exists only when `hashLines` holds).
 */
const FILE_HEADER = /^\[.+#[0-9A-F]{4}\]$/i

/**
 * One numbered row of a printed file: `12:text` in hashline mode, and `12|text` when
 * `readLineNumbers` is on in another mode. A structural summary prints the row that
 * stands for a folded brace pair with the range that it covers:
 * `42-120:export function second() { … }` (`formatMergedBraceLine`).
 */
const NUMBERED_ROW = /^(\d+)(?:-\d+)?[:|](.*)$/

/**
 * The row omp prints in place of lines that it left out: an elided span of a summary,
 * or the gap between a range and the block header above it.
 */
const ELISION = '…'

/**
 * The notice omp adds below a file: the limit of a partial read, or the count of the
 * lines that a summary left out (`[…42ln elided; re-read needed ranges with …]`,
 * `formatSummaryElisionFooter`).
 */
const TRAILING_NOTICE = /^\[(?:Showing|Truncated|Output|…\d+ln elided).*\]$/

/** Drop the blank rows at the end of a list of rows, in place. */
function trimTrailingBlank(rows: string[]): void {
  while (rows.length > 0 && (rows.at(-1)?.trim() ?? '') === '')
    rows.pop()
}

/** The rows of a text, with each line ending made one `\n`. */
function rowsOf(text: string): string[] {
  return text.replace(/\r\n/g, '\n').split('\n')
}

/** The rows of the text omp printed for the model, without its header and its trailing notice. */
interface PrintedText {
  rows: string[]
  notice?: string
}

function printedText(text: string): PrintedText {
  const rows = rowsOf(text)
  if (FILE_HEADER.test(rows[0] ?? ''))
    rows.shift()
  trimTrailingBlank(rows)
  const last = rows.at(-1)?.trim() ?? ''
  if (!TRAILING_NOTICE.test(last))
    return { rows }
  rows.pop()
  trimTrailingBlank(rows)
  return { rows, notice: last.slice(1, -1) }
}

/**
 * The lines of a printed text that numbers each of its rows, or null for a text in
 * another shape.
 *
 * An elision row stays, with no number, as it does in `displayLines`. A blank row
 * between two ranges holds no line, and a printed file line is never a bare blank
 * row: an empty line prints as `12:`.
 */
function numberedRows(rows: readonly string[]): NumberedFileLine[] | null {
  const lines: NumberedFileLine[] = []
  for (const row of rows) {
    if (row === '')
      continue
    if (row === ELISION) {
      lines.push({ num: null, text: ELISION })
      continue
    }
    const match = NUMBERED_ROW.exec(row)
    if (match?.[1] === undefined)
      return null
    lines.push({ num: Number(match[1]), text: match[2] ?? '' })
  }
  // A text of elision rows alone numbers no line, so it is not in this shape.
  return lines.some(line => line.num !== null) ? lines : null
}

/**
 * The line number of each row of a structural summary, `null` for an elision row, or
 * null when omp states no number.
 *
 * omp states a summary's display text with `startLine: 1` and no `lineNumbers`, and
 * the text holds only the kept lines, with one elision row for each span that it left
 * out (`formatReadSummary`). Numbering that text from its first line is wrong from the
 * first elision on. The numbers are in the text omp printed for the model, one row for
 * each display row, when a mode numbers it. A row whose text is not its display row
 * states no number: the plain modes print the bare text, whose own lines can look
 * numbered.
 */
function summaryNumbers(displayRows: readonly string[], printedRows: readonly string[]): Array<number | null> | null {
  if (printedRows.length < displayRows.length)
    return null
  const numbers: Array<number | null> = []
  for (const [index, display] of displayRows.entries()) {
    const printed = printedRows[index] ?? ''
    if (display === ELISION && printed === ELISION) {
      numbers.push(null)
      continue
    }
    const match = NUMBERED_ROW.exec(printed)
    if (match?.[1] === undefined || match[2] !== display)
      return null
    numbers.push(Number(match[1]))
  }
  return numbers
}

/**
 * The line number of each display row that omp states, `null` for an elision row, or
 * null when the list does not fit the text.
 *
 * A ranged read adds the header rows of the enclosing block and an elision row, and
 * states the real number of each row in `lineNumbers` (`tools/read.ts`). A result with
 * no list numbers its rows from `startLine`.
 */
function statedNumbers(display: Record<string, unknown>, rowCount: number): Array<number | null> | null {
  const stated = display.lineNumbers
  if (stated === undefined) {
    const startLine = pickNumber(display, 'startLine', 1)
    return Array.from({ length: rowCount }, (_, index) => startLine + index)
  }
  if (!Array.isArray(stated) || stated.length !== rowCount)
    return null
  const numbers: Array<number | null> = []
  for (const entry of stated) {
    if (entry !== null && !(typeof entry === 'number' && Number.isSafeInteger(entry) && entry > 0))
      return null
    numbers.push(entry)
  }
  return numbers
}

/**
 * The numbered lines of the display rows.
 *
 * An elision row stays, with no number, so the reader sees where omp left lines out.
 */
function displayLines(rows: readonly string[], numbers: ReadonlyArray<number | null>): NumberedFileLine[] {
  return rows.map((text, index) => ({ num: numbers[index] ?? null, text }))
}

/**
 * The file one `read` returned.
 *
 * omp states the file's plain text in `details.displayContent`, which is the cleanest
 * source, and the number of each of its rows (see `statedNumbers` and
 * `summaryNumbers`). A text whose numbers omp does not state draws as it is, with no
 * number rather than a wrong one. A result without `displayContent` is read from the
 * numbered text omp printed, and a text in no numbered shape draws as it is. A
 * directory is one such text: omp prints it as an indented tree.
 */
export function ohMyPiReadResult(text: string, details: Record<string, unknown>): ReadFileResult {
  const printed = printedText(text)
  const display = pickObject(details, 'displayContent')
  const displayText = display ? pickString(display, 'text', undefined) : undefined
  const displayRows = displayText ? rowsOf(displayText) : []
  // The display text holds no notice, so a last row that it holds too is the file's own line.
  const lastDisplayRow = displayRows.filter(row => row.trim() !== '').at(-1)?.trim()
  const notice = printed.notice !== undefined && lastDisplayRow !== `[${printed.notice}]` ? printed.notice : undefined
  const trailing: ReadReminder[] = notice !== undefined ? [{ label: 'Notice', text: notice }] : []
  const withTrailing = (result: ReadFileResult): ReadFileResult => trailing.length > 0 ? { ...result, trailing } : result

  if (display && displayText !== undefined) {
    const numbers = isObject(details.summary) ? summaryNumbers(displayRows, printed.rows) : statedNumbers(display, displayRows.length)
    if (numbers === null)
      return withTrailing({ lines: null, fallbackContent: displayText })
    return withTrailing({ lines: displayLines(displayRows, numbers), fallbackContent: text })
  }
  const lines = numberedRows(printed.rows)
  return lines ? withTrailing({ lines, fallbackContent: text }) : { lines: null, fallbackContent: text }
}

/**
 * Whether one `read` result states a web page rather than a file.
 *
 * omp's `read` also takes a URL, and it then states `details.kind` as `url`. Such a
 * call is a fetch, and it draws as one.
 */
export function ohMyPiReadIsUrl(details: Record<string, unknown>, urlKind: string): boolean {
  return pickString(details, 'kind') === urlKind
}
