import type { HeightInput } from './chatHeightEstimator'
import type { StructuredPatchHunk } from './diff'
import { isObject } from '~/lib/jsonPick'
import { countLines } from './chatHeightShared'

/**
 * Diff-geometry primitives: turn a provider-neutral StructuredPatchHunk[] into
 * the row / line-length / gap-separator counts the height estimator needs. Split
 * out of chatHeightShared so the diff math (the largest, most cohesive cluster)
 * lives apart from the text/block primitives; the per-provider `heightMetrics`
 * hooks resolve hunks from their own wire format (reusing each provider's
 * existing fileEdit extractor) and hand them here. A leaf module: it imports only
 * the StructuredPatchHunk shape, the HeightInput type, and `countLines` -- no
 * registry, no provider, no SolidJS -- so it stays cycle-free.
 */

/** Row counts for a set of hunks in unified vs split layout (split aligns sides). */
function diffRowsFromHunks(hunks: StructuredPatchHunk[]): { unified: number, split: number, added: number, removed: number } {
  let unified = 0
  let split = 0
  let added = 0
  let removed = 0
  for (const h of hunks) {
    unified += h.lines.length
    // Split view aligns the old and new sides row-for-row, so its row count is
    // the taller side per hunk. For a well-formed hunk max(oldLines,newLines) <=
    // lines.length (lines holds context+removed+added; each side is context plus
    // one change kind), so clamping to lines.length is a no-op there -- but it
    // neutralizes a malformed hunk whose coords claim far more lines than `lines`
    // actually carries (the gate admits any non-negative integer), which would
    // otherwise feed an absurd row count into diffSplitRows when `lines` is empty.
    split += Math.min(Math.max(h.oldLines, h.newLines), h.lines.length)
    for (const line of h.lines) {
      if (line.startsWith('+'))
        added++
      else if (line.startsWith('-'))
        removed++
    }
  }
  return { unified, split, added, removed }
}

/** Content length of a hunk line with its +/-/space prefix removed (>= 0). */
function lineContentLength(line: string): number {
  return Math.max(0, line.length - 1)
}

/**
 * Shape-check a raw `structuredPatch` entry before trusting it as a
 * StructuredPatchHunk: it must carry a `lines` array of STRINGS plus the numeric
 * coords the diff row-count / gap-separator math reads. Guards against a
 * malformed wire payload throwing inside the height-estimator memo.
 *
 * The coords must be NON-NEGATIVE INTEGERS, not merely `typeof === 'number'`:
 * `typeof NaN` and `typeof Infinity` are both `'number'`, and a NaN
 * `oldLines`/`newLines` would flow through `Math.max(...)` in diffRowsFromHunks
 * into `diffSplitRows`, turning the row estimate -- and every cumulative offset
 * past it -- into NaN. A NEGATIVE count is just as wrong but subtler: it survives
 * `Number.isFinite`, then `Math.max(negative, negative)` UNDER-counts split rows
 * (the drift-accumulating direction) and `oldStart + oldLines` can fall below the
 * next hunk's start, suppressing a real gap separator. `Number.isInteger(x) && x
 * >= 0` rejects NaN, Infinity, fractional, AND negative in one test, keeping a
 * malformed hunk from being trusted as valid geometry in the first place.
 *
 * `lines` elements must be STRINGS too, not just an array: diffRowsFromHunks
 * calls `line.startsWith(...)` and lineContentLength reads `line.length`, so a
 * non-string element (e.g. a number from a malformed payload) would THROW inside
 * the memo (`(42).startsWith` is not a function) or read `undefined.length` as
 * NaN -- the exact "blank the whole list / poison the offset map" failure this
 * gate exists to prevent. Validating element type closes that hole.
 */
function isNonNegativeInteger(n: unknown): boolean {
  return typeof n === 'number' && Number.isInteger(n) && n >= 0
}

export function isStructuredPatchHunk(h: unknown): h is StructuredPatchHunk {
  return isObject(h)
    && Array.isArray(h.lines)
    && h.lines.every(line => typeof line === 'string')
    && isNonNegativeInteger(h.oldStart)
    && isNonNegativeInteger(h.oldLines)
    && isNonNegativeInteger(h.newStart)
    && isNonNegativeInteger(h.newLines)
}

/**
 * Per displayed hunk-line CONTENT length (the +/-/space prefix is a separate
 * span, so it's excluded), in unified order, for the wrap model.
 */
function diffLineContentLengths(hunks: StructuredPatchHunk[]): number[] {
  const out: number[] = []
  for (const h of hunks) {
    for (const line of h.lines)
      out.push(lineContentLength(line))
  }
  return out
}

/**
 * Per split aligned-row content length: the LONGER of the two side cells, since
 * a split row's height is governed by its taller cell. Mirrors
 * `buildSplitLines`/`walkHunks` exactly (greedy min(removed, added) pairing per
 * consecutive -/+ block): a context line shows the same text on both sides; a
 * paired removal+addition takes the max of the two; a leftover removal or
 * addition occupies one side. The result's LENGTH is therefore the true number
 * of rendered split rows -- which can exceed `max(oldLines, newLines)` when a
 * hunk interleaves several separate change blocks.
 */
function diffSplitLineLengths(hunks: StructuredPatchHunk[]): number[] {
  const out: number[] = []
  for (const h of hunks) {
    const lines = h.lines
    let i = 0
    while (i < lines.length) {
      // A real diff line carries a ' '/'+'/'-' prefix; an empty string only arises
      // from a malformed payload (the shape gate admits any string). `[0]` on '' is
      // undefined, so `|| ' '` treats it as a blank CONTEXT row -- one rendered row
      // of zero content, which is exactly how a blank line renders, so the estimate
      // stays accurate. Intentional, not a fallthrough bug.
      const prefix = lines[i][0] || ' '
      if (prefix === '-' || prefix === '+') {
        const removed: number[] = []
        while (i < lines.length && lines[i][0] === '-') {
          removed.push(lineContentLength(lines[i]))
          i++
        }
        const added: number[] = []
        while (i < lines.length && lines[i][0] === '+') {
          added.push(lineContentLength(lines[i]))
          i++
        }
        const paired = Math.min(removed.length, added.length)
        for (let j = 0; j < paired; j++)
          out.push(Math.max(removed[j], added[j]))
        for (let j = paired; j < removed.length; j++)
          out.push(removed[j])
        for (let j = paired; j < added.length; j++)
          out.push(added[j])
      }
      else {
        out.push(lineContentLength(lines[i]))
        i++
      }
    }
  }
  return out
}

/**
 * Original-file line count AS THE DIFF RENDERER COUNTS IT: split on '\n' with a
 * single trailing-newline blank dropped (DiffViewer's useGapData,
 * DiffViewer.tsx:358-360). `countLines` alone counts that trailing blank as a
 * line, so a newline-terminated file (the common case) would read one line too
 * long -- enough to invent a phantom trailing gap when the last hunk reaches EOF.
 */
function originalFileLineCount(originalFile: string): number {
  const n = countLines(originalFile)
  return n > 0 && originalFile.endsWith('\n') ? n - 1 : n
}

/**
 * Number of gap-separator rows the diff renders, mirroring
 * `computeGapMap`/`computeSyntheticGapMap` (diffBuilder.ts):
 *  - A between-hunk separator renders ONLY where real lines are hidden between
 *    two hunks (`gapEnd >= gapStart`), NOT once per hunk pair -- adjacent hunks
 *    (context 0) show none.
 *  - Leading/trailing context gaps need `originalFile` (the renderer only draws
 *    them when it has the full source): a leading gap when the first hunk starts
 *    past line 1, a trailing gap when the last hunk ends before EOF.
 */
function diffSeparatorCount(hunks: StructuredPatchHunk[], originalFile: string): number {
  if (hunks.length === 0)
    return 0
  let count = 0
  for (let i = 1; i < hunks.length; i++) {
    const gapStart = hunks[i - 1].oldStart + hunks[i - 1].oldLines // first line after prev hunk
    const gapEnd = hunks[i].oldStart - 1 // last line before curr hunk (inclusive)
    if (gapEnd >= gapStart)
      count++
  }
  if (originalFile) {
    const total = originalFileLineCount(originalFile)
    const first = hunks[0]
    const last = hunks[hunks.length - 1]
    if (first.oldStart > 1)
      count++ // leading gap: lines 1..oldStart-1 hidden before the first hunk
    if (last.oldStart + last.oldLines <= total)
      count++ // trailing gap: lines after the last hunk hidden before EOF
  }
  return count
}

/**
 * Distill a set of diff hunks into the `diff*` slice of a HeightInput. The
 * single seam every provider's `heightMetrics` hook funnels its
 * (provider-specifically extracted) hunks through, so the diff geometry math
 * lives in ONE place rather than once per provider.
 *
 * Filters the hunks through `isStructuredPatchHunk` first, so a malformed wire
 * payload can't throw inside the estimator memo -- and returns null when no
 * valid hunk remains (an empty / unparseable diff), letting the caller fall
 * back to text sizing rather than rendering a 0-row diff shell.
 *
 * `originalFile` (when the provider carries it) lets the gap-separator count
 * include leading/trailing context gaps, matching DiffViewer.
 */
export function diffHeightFields(rawHunks: StructuredPatchHunk[], originalFile?: string): Partial<HeightInput> | null {
  const hunks = rawHunks.filter(isStructuredPatchHunk)
  if (hunks.length === 0)
    return null
  const { unified, split, added, removed } = diffRowsFromHunks(hunks)
  return {
    diffUnifiedRows: unified,
    diffSplitRows: split,
    diffHunkCount: hunks.length,
    diffAdded: added,
    diffRemoved: removed,
    diffLineLengths: diffLineContentLengths(hunks),
    diffSplitLineLengths: diffSplitLineLengths(hunks),
    diffSeparatorRows: diffSeparatorCount(hunks, originalFile ?? ''),
  }
}

/**
 * Merge per-block diff field slices (a multi-file edit rendered as N stacked
 * diff containers) into one HeightInput slice. Rows / added / removed / hunks /
 * separators / line-length arrays sum, and diffBlockCount records the block
 * count so estimateDiffRow charges container chrome PER block. Summing the
 * SEPARATOR counts per block is the point: concatenating hunks from different
 * files into one diffHeightFields call would let an unrelated cross-file hunk
 * boundary spuriously trip the between-hunk separator test, and would under-count
 * chrome by (N-1) blocks. Returns null for an empty input.
 */
export function mergeDiffHeightFields(slices: Array<Partial<HeightInput>>): Partial<HeightInput> | null {
  if (slices.length === 0)
    return null
  if (slices.length === 1)
    return { ...slices[0], diffBlockCount: 1 }
  const merged: Partial<HeightInput> = {
    diffUnifiedRows: 0,
    diffSplitRows: 0,
    diffHunkCount: 0,
    diffAdded: 0,
    diffRemoved: 0,
    diffLineLengths: [],
    diffSplitLineLengths: [],
    diffSeparatorRows: 0,
    diffBlockCount: slices.length,
  }
  for (const s of slices) {
    merged.diffUnifiedRows! += s.diffUnifiedRows ?? 0
    merged.diffSplitRows! += s.diffSplitRows ?? 0
    merged.diffHunkCount! += s.diffHunkCount ?? 0
    merged.diffAdded! += s.diffAdded ?? 0
    merged.diffRemoved! += s.diffRemoved ?? 0
    merged.diffSeparatorRows! += s.diffSeparatorRows ?? 0
    if (s.diffLineLengths)
      merged.diffLineLengths!.push(...s.diffLineLengths)
    if (s.diffSplitLineLengths)
      merged.diffSplitLineLengths!.push(...s.diffSplitLineLengths)
  }
  return merged
}
