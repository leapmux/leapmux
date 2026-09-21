import { snapUtf16CutBackward, snapUtf16CutForward } from '~/lib/utf16Cut'

/** Maximum source characters that one expanded body retains. */
export const EXPANDED_TEXT_DISPLAY_CHAR_LIMIT = 64 * 1024

/** Maximum source characters that one displayed line retains. */
export const TEXT_DISPLAY_LINE_CHAR_LIMIT = 4 * 1024

/** Maximum source rows that one expanded text body retains. */
export const EXPANDED_TEXT_DISPLAY_LINE_LIMIT = 1_000

/** Maximum Markdown source that the synchronous plain renderer can parse. */
export const MARKDOWN_PARSE_CHAR_LIMIT = 32 * 1024

const CONTENT_OMISSION = '\n… content omitted from display …\n'
const LINES_OMISSION = '… lines omitted from display …'
const LINE_OMISSION = '…'

export const LIMITED_TEXT_DISPLAY_NOTICE = 'Display limited to keep this page responsive. The full content remains available.'
export const PLAIN_TEXT_DISPLAY_NOTICE = 'Large content uses a plain-text display to keep this page responsive.'
export const LARGE_TEXT_DISPLAY_CLASS = 'large-text-display'

export interface SafeTextDisplay {
  text: string
  limited: boolean
}

export interface TextDisplayLimits {
  maxChars?: number
  maxLineChars?: number
  maxLines?: number
}

function positiveInteger(value: number | undefined, fallback: number): number {
  return value !== undefined && Number.isSafeInteger(value) && value > 0 ? value : fallback
}

function limitTotalChars(text: string, maxChars: number): SafeTextDisplay {
  if (text.length <= maxChars)
    return { text, limited: false }

  const available = Math.max(2, maxChars - CONTENT_OMISSION.length)
  const headChars = Math.floor(available * 0.75)
  const tailChars = available - headChars
  const headEnd = snapUtf16CutBackward(text, headChars)
  const tailStart = snapUtf16CutForward(text, text.length - tailChars)
  return {
    text: `${text.slice(0, headEnd)}${CONTENT_OMISSION}${text.slice(tailStart)}`,
    limited: true,
  }
}

export function hasLineLongerThan(text: string, maxLineChars: number): boolean {
  let start = 0
  while (start <= text.length) {
    const newline = text.indexOf('\n', start)
    const end = newline === -1 ? text.length : newline
    if (end - start > maxLineChars)
      return true
    if (newline === -1)
      return false
    start = newline + 1
  }
  return false
}

function hasMoreLinesThan(text: string, maxLines: number): boolean {
  let remaining = maxLines
  let index = 0
  while (remaining > 0) {
    const newline = text.indexOf('\n', index)
    if (newline === -1)
      return false
    remaining--
    index = newline + 1
  }
  return true
}

function limitLineCount(text: string, maxLines: number): SafeTextDisplay {
  if (!hasMoreLinesThan(text, maxLines))
    return { text, limited: false }
  const lines = text.split('\n')
  const available = Math.max(2, maxLines)
  const headLines = Math.floor(available * 0.75)
  const tailLines = available - headLines
  return {
    text: [...lines.slice(0, headLines), LINES_OMISSION, ...lines.slice(-tailLines)].join('\n'),
    limited: true,
  }
}

function limitLineChars(text: string, maxLineChars: number): SafeTextDisplay {
  if (!hasLineLongerThan(text, maxLineChars))
    return { text, limited: false }
  let start = 0
  const parts: string[] = []
  while (start <= text.length) {
    const newline = text.indexOf('\n', start)
    const end = newline === -1 ? text.length : newline
    const length = end - start
    if (length <= maxLineChars) {
      parts.push(text.slice(start, end))
    }
    else {
      const available = Math.max(2, maxLineChars - LINE_OMISSION.length)
      const headChars = Math.floor(available * 0.75)
      const tailChars = available - headChars
      const headEnd = snapUtf16CutBackward(text, start + headChars)
      const tailStart = snapUtf16CutForward(text, end - tailChars)
      parts.push(text.slice(start, headEnd), LINE_OMISSION, text.slice(tailStart, end))
    }
    if (newline === -1)
      break
    parts.push('\n')
    start = newline + 1
  }
  return { text: parts.join(''), limited: true }
}

function limitSingleLineChars(text: string, maxChars: number): SafeTextDisplay {
  if (text.length <= maxChars)
    return { text, limited: false }
  const retainedChars = Math.max(0, maxChars - LINE_OMISSION.length)
  const headChars = Math.floor(retainedChars * 0.75)
  const tailChars = retainedChars - headChars
  const headEnd = snapUtf16CutBackward(text, headChars)
  const tailStart = snapUtf16CutForward(text, text.length - tailChars)
  return {
    text: `${text.slice(0, headEnd)}${LINE_OMISSION}${text.slice(tailStart)}`,
    limited: true,
  }
}

export interface TextLinesDisplay<T> extends SafeTextDisplay {
  lines: T[]
}

/** Limit ordered text lines under one shared row and character budget. */
export function limitTextLinesForDisplay<T extends { text: string }>(
  sourceLines: readonly T[],
  limits: TextDisplayLimits = {},
): TextLinesDisplay<T> {
  const maxChars = positiveInteger(limits.maxChars, EXPANDED_TEXT_DISPLAY_CHAR_LIMIT)
  const maxLineChars = positiveInteger(limits.maxLineChars, TEXT_DISPLAY_LINE_CHAR_LIMIT)
  const maxLines = positiveInteger(limits.maxLines, EXPANDED_TEXT_DISPLAY_LINE_LIMIT)
  const lines: T[] = []
  const text: string[] = []
  let usedChars = 0
  let limited = false
  for (const line of sourceLines) {
    if (lines.length >= maxLines) {
      limited = true
      break
    }
    const separatorChars = lines.length === 0 ? 0 : 1
    const remainingChars = maxChars - usedChars - separatorChars
    if (remainingChars <= 0) {
      limited = true
      break
    }
    const display = limitSingleLineChars(line.text, Math.min(maxLineChars, remainingChars))
    const displayedLine = display.limited ? { ...line, text: display.text } : line
    lines.push(displayedLine)
    text.push(displayedLine.text)
    usedChars += separatorChars + displayedLine.text.length
    limited ||= display.limited
  }
  limited ||= lines.length < sourceLines.length
  return { lines, text: text.join('\n'), limited }
}

/** Limit total characters, rows, and the layout cost of one unbroken line. */
export function limitTextForDisplay(text: string, limits: TextDisplayLimits = {}): SafeTextDisplay {
  const total = limitTotalChars(text, positiveInteger(limits.maxChars, EXPANDED_TEXT_DISPLAY_CHAR_LIMIT))
  const rows = limitLineCount(total.text, positiveInteger(limits.maxLines, EXPANDED_TEXT_DISPLAY_LINE_LIMIT))
  const lines = limitLineChars(rows.text, positiveInteger(limits.maxLineChars, TEXT_DISPLAY_LINE_CHAR_LIMIT))
  return { text: lines.text, limited: total.limited || rows.limited || lines.limited }
}

/** Report whether Markdown must use a plain-text display. */
export function markdownNeedsPlainTextDisplay(text: string): boolean {
  if (text.length > MARKDOWN_PARSE_CHAR_LIMIT)
    return true
  return hasMoreLinesThan(text, EXPANDED_TEXT_DISPLAY_LINE_LIMIT)
    || hasLineLongerThan(text, TEXT_DISPLAY_LINE_CHAR_LIMIT)
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

/** Build safe HTML for Markdown source that is too large to parse. */
export function largeMarkdownPlainHtml(text: string): string {
  const display = limitTextForDisplay(text)
  const notice = display.limited ? LIMITED_TEXT_DISPLAY_NOTICE : PLAIN_TEXT_DISPLAY_NOTICE
  return `<pre class="${LARGE_TEXT_DISPLAY_CLASS}" data-large-text-display>${escapeHtml(display.text)}</pre><p>${escapeHtml(notice)}</p>`
}
