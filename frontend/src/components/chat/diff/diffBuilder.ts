import type { DiffGap, DiffGapSummary, StructuredPatchHunk } from './diffTypes'
import { diffLines } from 'diff'

const TRAILING_NEWLINE_RE = /\n$/

/**
 * Convert raw old/new strings into StructuredPatchHunk[] format,
 * normalizing the input for the shared diff builders.
 */
export function rawDiffToHunks(oldStr: string, newStr: string): StructuredPatchHunk[] {
  const changes = diffLines(oldStr, newStr)
  const lines: string[] = []
  let oldLines = 0
  let newLines = 0
  for (const change of changes) {
    const prefix = change.added ? '+' : change.removed ? '-' : ' '
    const rawLines = change.value.replace(TRAILING_NEWLINE_RE, '').split('\n')
    for (const line of rawLines) {
      lines.push(prefix + line)
      if (change.added) {
        newLines++
      }
      else if (change.removed) {
        oldLines++
      }
      else {
        oldLines++
        newLines++
      }
    }
  }
  return [{ oldStart: 1, oldLines, newStart: 1, newLines, lines }]
}

/** Format structuredPatch hunks as a unified diff string suitable for copying. */
export function formatUnifiedDiffText(hunks: StructuredPatchHunk[], filePath?: string): string {
  const header = filePath
    ? `--- a/${filePath}\n+++ b/${filePath}\n`
    : ''
  const parts: string[] = [header]
  for (const hunk of hunks) {
    parts.push(`@@ -${hunk.oldStart},${hunk.oldLines} +${hunk.newStart},${hunk.newLines} @@\n`)
    for (const line of hunk.lines) {
      parts.push(`${line}\n`)
    }
  }
  return parts.join('')
}

/**
 * Extract old-side and new-side source text from hunks for tokenization.
 * Old side = context lines + removed lines (in order).
 * New side = context lines + added lines (in order).
 */
export function extractSidesFromHunks(hunks: StructuredPatchHunk[]): { oldCode: string, newCode: string } {
  const oldLines: string[] = []
  const newLines: string[] = []
  for (const hunk of hunks) {
    for (const line of hunk.lines) {
      const prefix = line[0] || ' '
      const text = line.slice(1)
      if (prefix === '-') {
        oldLines.push(text)
      }
      else if (prefix === '+') {
        newLines.push(text)
      }
      else {
        oldLines.push(text)
        newLines.push(text)
      }
    }
  }
  return { oldCode: oldLines.join('\n'), newCode: newLines.join('\n') }
}

/**
 * Compute a map of gaps between hunks using the original file content.
 * Returns a Map keyed by hunk index (gap *before* that hunk) plus an optional trailing gap.
 *
 * Gap at key 0 = lines before the first hunk.
 * Gap at key N = lines between hunk N-1 and hunk N.
 * Trailing gap is returned separately.
 */
export function computeGapMap(
  hunks: StructuredPatchHunk[],
  originalFileLines: string[],
): { gaps: Map<number, DiffGap>, trailing: DiffGap | null } {
  const gaps = new Map<number, DiffGap>()
  let trailing: DiffGap | null = null

  if (hunks.length === 0)
    return { gaps, trailing }

  // Gap before the first hunk (lines 1..firstHunk.oldStart-1)
  const firstHunk = hunks[0]
  if (firstHunk.oldStart > 1) {
    const endLine = firstHunk.oldStart - 1 // 1-based inclusive
    gaps.set(0, {
      lines: originalFileLines.slice(0, endLine),
      startLineNumber: 1,
    })
  }

  // Gaps between consecutive hunks
  for (let i = 1; i < hunks.length; i++) {
    const prev = hunks[i - 1]
    const curr = hunks[i]
    const gapStart = prev.oldStart + prev.oldLines // 1-based, first line after prev hunk
    const gapEnd = curr.oldStart - 1 // 1-based inclusive
    if (gapEnd >= gapStart) {
      gaps.set(i, {
        lines: originalFileLines.slice(gapStart - 1, gapEnd),
        startLineNumber: gapStart,
      })
    }
  }

  // Trailing gap (lines after the last hunk)
  // hunks.length > 0 is guaranteed by the early return above
  const lastHunk = hunks.at(-1)!
  const trailingStart = lastHunk.oldStart + lastHunk.oldLines // 1-based
  if (trailingStart <= originalFileLines.length) {
    trailing = {
      lines: originalFileLines.slice(trailingStart - 1),
      startLineNumber: trailingStart,
    }
  }

  return { gaps, trailing }
}

/**
 * Compute synthetic gap summaries from hunk coordinates only.
 * This supports non-expandable "N lines hidden" separators even when the
 * original file content is not available.
 *
 * Gap at key N = lines between hunk N-1 and hunk N.
 * Leading and trailing gaps are intentionally omitted without original file
 * content because they only indicate omitted outer context around the diff.
 */
export function computeSyntheticGapMap(hunks: StructuredPatchHunk[]): Map<number, DiffGapSummary> {
  const gaps = new Map<number, DiffGapSummary>()

  if (hunks.length === 0)
    return gaps

  for (let i = 1; i < hunks.length; i++) {
    const prev = hunks[i - 1]
    const curr = hunks[i]
    const gapStart = prev.oldStart + prev.oldLines
    const gapEnd = curr.oldStart - 1
    if (gapEnd >= gapStart) {
      gaps.set(i, {
        lineCount: gapEnd - gapStart + 1,
        startLineNumber: gapStart,
      })
    }
  }

  return gaps
}

/**
 * Group diff line entries by their `hunkIndex` field.
 * Returns an array of groups in hunk-index order.
 */
export function groupByHunk<T extends { hunkIndex: number }>(entries: T[]): T[][] {
  if (entries.length === 0)
    return []

  const groups: T[][] = []
  let currentIndex = entries[0].hunkIndex
  let currentGroup: T[] = []

  for (const entry of entries) {
    if (entry.hunkIndex !== currentIndex) {
      groups.push(currentGroup)
      currentGroup = []
      currentIndex = entry.hunkIndex
    }
    currentGroup.push(entry)
  }
  groups.push(currentGroup)
  return groups
}

/** Count the total number of lines across all hunks. */
export function countHunkLines(hunks: StructuredPatchHunk[]): number {
  let count = 0
  for (const hunk of hunks)
    count += hunk.lines.length
  return count
}

/** Sum added and deleted line counts from hunk content (one pass). */
export function diffStatsFromHunks(hunks: StructuredPatchHunk[]): { added: number, deleted: number } {
  let added = 0
  let deleted = 0
  for (const hunk of hunks) {
    for (const line of hunk.lines) {
      if (line.startsWith('+'))
        added++
      else if (line.startsWith('-'))
        deleted++
    }
  }
  return { added, deleted }
}
