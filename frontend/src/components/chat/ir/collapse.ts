/** The number of rows a collapsed tool result shows before its expand control. */
export const COLLAPSED_RESULT_ROWS = 3

/**
 * Equivalent to `text.split('\n').length > threshold` but stops scanning as
 * soon as the threshold is exceeded, avoiding full-array allocation for large
 * tool outputs where only the count matters.
 */
export function hasMoreLinesThan(text: string, threshold: number): boolean {
  let needed = threshold
  let idx = 0
  while (needed > 0) {
    const next = text.indexOf('\n', idx)
    if (next === -1)
      return false
    needed--
    idx = next + 1
  }
  return true
}

/**
 * How many lines one text holds, without building the array of them.
 *
 * `split('\n').length` copies the whole body and allocates one string per line.
 * A title that states "(N lines)" re-runs on every reactive pass, so a large file
 * paid for that copy on every streamed frame of the write that produced it.
 */
export function countLines(text: string): number {
  if (text === '')
    return 0
  let lines = 1
  for (let i = text.indexOf('\n'); i !== -1; i = text.indexOf('\n', i + 1))
    lines++
  return text.endsWith('\n') ? lines - 1 : lines
}
