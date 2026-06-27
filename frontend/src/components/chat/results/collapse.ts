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
