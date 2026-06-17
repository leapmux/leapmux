import type { HeightInput } from './chatHeightEstimator'

// ---------------------------------------------------------------------------
// Wrap model
//
// The pure, DOM-free wrap-math engine behind the height estimator: how many visual
// rows a body of text occupies when wrapped at a given content width. Extracted
// from chatHeightEstimator so the wrap calibration has its own focused test surface
// (mirroring the chatDiffGeometry leaf the windowing work split out). Every function
// here is pure -- it takes plain numbers (and the per-line metrics off a HeightInput)
// and returns counts, reading no DOM and no module state.
// ---------------------------------------------------------------------------

/** Visual row + block-gap counts a prose body wraps into at a given width. */
export interface WrapMetrics {
  /** Total visual rows -- wrapped prose lines PLUS code-block lines (1 row each). */
  rows: number
  /** Block-gap margins (blank-line breaks between prose blocks). */
  gaps: number
  /** Of `rows`, how many are fenced code-block lines (sized at codeBlockLinePx, not wrapped). */
  codeRows: number
}

/**
 * Visual row count for a text body of `textLength` chars across
 * `logicalLineCount` hard lines wrapped at `widthPx`. Takes the MAX of the
 * hard-line count and the total-wrap count so neither many-short-lines nor
 * few-long-lines is under-counted (the bias-up rule). Never below 1.
 */
export function visualRows(textLength: number, logicalLineCount: number, charWidthPx: number, widthPx: number): number {
  // Route the wrap math through the shared per-line primitive so a calibration
  // change lands in one place. When widthPx <= 0 wrapRowsForLine returns 1, which
  // the outer Math.max(1, logicalLineCount, ...) already covers -- identical to
  // the old `byWrap = logicalLineCount` fallback after the max.
  return Math.max(1, logicalLineCount, wrapRowsForLine(textLength, charWidthPx, widthPx))
}

/**
 * Visual rows a single hard line of `len` chars occupies when wrapped at
 * `widthPx`. The shared per-line wrap primitive behind the prose, tool, and diff
 * models, so a wrap-math calibration change lands in one place.
 *
 * Floors the wrap width at a few characters, never 1px (or a negative width, as
 * the split-diff caller's `contentWidth/2 - gutter` can produce on a degenerately
 * narrow pane): a sub-character divisor would explode `ceil(len * charWidthPx /
 * width)` into thousands of phantom rows -- a wild over-estimate, not the gentle
 * bias-up cushion we want. charWidthPx is a positive calibration constant, so the
 * floor is always a safe positive divisor.
 */
export function wrapRowsForLine(len: number, charWidthPx: number, widthPx: number): number {
  const width = Math.max(charWidthPx * 4, widthPx)
  return Math.max(1, Math.ceil((len * charWidthPx) / width))
}

/**
 * Wrapped row + block-gap counts for a prose body given its per hard-line
 * lengths. Each non-blank line contributes `ceil(len*charWidth/width)` rows
 * (>=1); each INTERIOR run of blank lines (a markdown block break) contributes
 * one block gap (a paragraph margin, not a full text row). Summing per line
 * rather than maxing a flat line count against a flat total-wrap is what keeps
 * mixed short/long markdown from under-counting.
 */
export function proseRowsFromLines(lineLengths: number[], charWidthPx: number, widthPx: number): WrapMetrics {
  let rows = 0
  let codeRows = 0
  let gaps = 0
  let seenContent = false
  let pendingGap = false
  for (const len of lineLengths) {
    if (len < 0) {
      // Fenced code line (encoded negative by toMarkdownLineLengths): exactly ONE
      // row, NOT char-wrapped -- a code block scrolls horizontally, it never wraps.
      if (pendingGap) {
        gaps++
        pendingGap = false
      }
      seenContent = true
      codeRows++
      rows++
      continue
    }
    if (len === 0) {
      // Blank prose line: a block break, but only count gaps BETWEEN content (leading
      // and trailing blank runs don't render a margin that grows the row).
      if (seenContent)
        pendingGap = true
      continue
    }
    if (pendingGap) {
      gaps++
      pendingGap = false
    }
    seenContent = true
    rows += wrapRowsForLine(len, charWidthPx, widthPx)
  }
  return { rows: Math.max(1, rows), gaps, codeRows }
}

/**
 * Wrapped-row + block-gap counts for a prose/thinking body. Prefers the precise
 * per-line model when `lineLengths` is present; otherwise falls back to the
 * coarse flat model from `textLength`/`logicalLineCount` (gaps = 0).
 */
export function proseRowMetrics(input: HeightInput, charWidthPx: number, widthPx: number): WrapMetrics {
  const lineLengths = input.lineLengths
  if (lineLengths && lineLengths.length > 0) {
    const metrics = proseRowsFromLines(lineLengths, charWidthPx, widthPx)
    // toLineLengths folds a >MAX_LINE_SAMPLES tail into ONE trailing entry, so for
    // a very long body the array holds fewer entries than the true hard-line
    // count and proseRowsFromLines under-counts -- it sizes the fold as a single
    // wrapping line, not one row per dropped hard line. Each dropped line still
    // renders >= 1 row, so floor the row count at the true hard-line count
    // (minus the blank-run gaps already counted separately) -- the bias-up
    // direction. No-op when the array wasn't folded (length === logicalLineCount).
    const logical = input.logicalLineCount ?? lineLengths.length
    if (lineLengths.length < logical)
      return { ...metrics, rows: Math.max(metrics.rows, logical - metrics.gaps) }
    return metrics
  }
  return { rows: visualRows(input.textLength ?? 0, input.logicalLineCount ?? 1, charWidthPx, widthPx), gaps: 0, codeRows: 0 }
}

/**
 * Per-line wrapped row count for a MONOSPACE pre-wrap tool body (Read/Grep/Bash
 * output). Each hard line wraps INDEPENDENTLY (`white-space: pre-wrap`), so SUM each
 * line's wrap (>= 1; a blank line is a blank row, not a block-gap margin). The flat
 * `visualRows` max() under-counts a body of several long lines because it ignores
 * the end-of-line slack wasted on every wrapped line -- the under-estimate this
 * fixes. Code-encoded negatives are DECODED and wrapped like any other line (a mono
 * body renders ``` literally). Falls back to the flat model when per-line lengths
 * are absent; floors at the true hard-line count when the tail was folded.
 */
export function monoRowMetrics(input: HeightInput, charWidthPx: number, widthPx: number): number {
  const lineLengths = input.lineLengths
  if (lineLengths && lineLengths.length > 0) {
    let rows = 0
    for (const v of lineLengths)
      rows += wrapRowsForLine(v < 0 ? -v - 1 : v, charWidthPx, widthPx)
    const logical = input.logicalLineCount ?? lineLengths.length
    // Folded tail (>MAX_LINE_SAMPLES): each dropped line still renders >= 1 row.
    return Math.max(1, rows, lineLengths.length < logical ? logical : 0)
  }
  return visualRows(input.textLength ?? 0, input.logicalLineCount ?? 1, charWidthPx, widthPx)
}

/**
 * Wrapped row count for a set of diff lines at a given content width (mono).
 * The narrow-/negative-width floor lives in wrapRowsForLine, shared with the
 * prose and tool models.
 */
export function diffWrappedRows(lineLengths: number[], charWidthPx: number, widthPx: number): number {
  let rows = 0
  for (const len of lineLengths)
    rows += wrapRowsForLine(len, charWidthPx, widthPx)
  return rows
}
