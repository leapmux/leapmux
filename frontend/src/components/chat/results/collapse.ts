export const COLLAPSED_RESULT_ROWS = 3

/**
 * Default size caps above which a code surface skips syntax highlighting and
 * renders plain instead: highlighting a huge body wastes a worker round-trip
 * and floods the DOM with token spans. Single-sourced here so every surface
 * (Read view, diff sides, Bash/JSON tool bodies, ANSI output) shares one
 * budget rather than re-declaring 1000/20000 in each file, where the copies
 * could silently drift.
 */
export const HIGHLIGHT_LINE_LIMIT = 1000
export const HIGHLIGHT_CHAR_LIMIT = 20000

/** Per-surface overrides for {@link canHighlightBySize}; absent fields use the defaults above. */
export interface HighlightSizeLimits {
  maxChars?: number
  maxLines?: number
}

/**
 * Whether `text` is small enough to syntax-highlight, against the char + line
 * caps (per-surface overridable). Shared by the Bash/JSON tool bodies and the
 * ANSI body so their size gate can't drift.
 */
export function canHighlightBySize(text: string, limits: HighlightSizeLimits = {}): boolean {
  return text.length <= (limits.maxChars ?? HIGHLIGHT_CHAR_LIMIT)
    && !hasMoreLinesThan(text, limits.maxLines ?? HIGHLIGHT_LINE_LIMIT)
}

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
