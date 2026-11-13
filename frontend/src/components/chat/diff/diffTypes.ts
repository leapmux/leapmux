import type { WordDiffPart, WordDiffs } from './diffCompute'
import type { CachedToken } from '~/lib/tokenCache'

/** A hunk from the structuredPatch format in tool_use_result. */
export interface StructuredPatchHunk {
  oldStart: number
  oldLines: number
  newStart: number
  newLines: number
  lines: string[]
}

/**
 * What to diff: two texts, or the hunks of a patch that a tool already computed.
 *
 * `drawnLineLimit` is the most diff lines that the view of the diff draws, and
 * DIFF_DRAWN_LINE_LIMIT when absent. The diff computes the word diffs of those
 * lines only (see limitDiffLines), and carries the limit to DiffView, which draws
 * those lines only.
 *
 * `maxLineEdits` is the most line edits (removals plus additions) that the diff
 * of two texts looks for, and LINE_DIFF_MAX_EDITS when absent. Past it, the diff
 * removes the whole old text and adds the whole new one (see
 * WholeReplacementReason). The search costs O((N + M) * D) for D edits, and the
 * diff worker answers one request at a time, so the limit also caps how long
 * one diff holds every other diff back.
 */
export type DiffInput = TextsDiffInput | PatchDiffInput

/** The diff of two texts (see DiffInput). */
export interface TextsDiffInput {
  kind: 'texts'
  oldText: string
  newText: string
  drawnLineLimit?: number
  maxLineEdits?: number
}

/** The diff of a patch that a tool already computed (see DiffInput). */
export interface PatchDiffInput {
  kind: 'hunks'
  hunks: StructuredPatchHunk[]
  drawnLineLimit?: number
}

/**
 * What one side of a diff line draws over its text: its syntax tokens and its
 * word highlights. They set only colors and backgrounds, so a change of the
 * marks changes neither the text of the line nor its height.
 */
export interface DiffLineMarks {
  /** The syntax tokens of the line, or null to draw it plain. */
  tokens: CachedToken[] | null
  /**
   * The word diff of a removed line and the added line that it pairs with, or
   * undefined to draw the line whole: an unpaired line, a pair whose word diff
   * the diff worker did not compute, or a line that is neither removed nor added.
   */
  words: WordDiffPart[] | undefined
}

/**
 * Where one side of a diff line finds its marks (see DiffLineMarks). The tokens
 * and the word diffs land after the hunks, so a line keeps where its marks are,
 * and a view reads them from the latest DiffMarkSources when it draws the line.
 */
export interface LineMarksSource {
  /** The side whose syntax tokens the line draws. */
  side: 'old' | 'new'
  /** The index of the line in the tokens of its side (see extractSidesFromHunks). */
  tokenLine: number
  /**
   * The key of the word diff of the removed line and the added line that pair
   * with each other (see pairKey), or undefined for a line without a pair.
   */
  pair: string | undefined
}

/** What the marks of the lines of a diff view come from. Each part lands on its own. */
export interface DiffMarkSources {
  /** The syntax tokens of each line of the old side, or null to draw the old side plain. */
  oldTokens: CachedToken[][] | null
  /** The syntax tokens of each line of the new side, or null to draw the new side plain. */
  newTokens: CachedToken[][] | null
  wordDiffs: WordDiffs
}

/** One line of the unified view. */
export interface DiffLineEntry {
  oldNum: number | null
  newNum: number | null
  prefix: string
  text: string
  type: 'added' | 'removed' | 'context'
  /** Index of the hunk this line belongs to (for gap separators). */
  hunkIndex: number
  marksSource: LineMarksSource
}

/** One side of a row of the split view. */
export interface SplitLineEntry {
  text: string
  type: 'removed' | 'added' | 'context' | 'empty'
  num: number | null
  /** Where the side finds its marks, or null for the empty side of an unpaired line. */
  marksSource: LineMarksSource | null
}

/** One row of the split view: the old side on the left, the new side on the right. */
export interface SplitRowEntry {
  left: SplitLineEntry
  right: SplitLineEntry
  /** Index of the hunk this row belongs to (for gap separators). */
  hunkIndex: number
}

/** The marks of both sides of a row of the split view. */
export interface SplitRowMarks {
  left: DiffLineMarks
  right: DiffLineMarks
}

/** A gap between hunks (or before the first / after the last hunk). */
export interface DiffGap {
  /** Lines from the original file that fill this gap. */
  lines: string[]
  /** 1-based line number of the first line in the gap. */
  startLineNumber: number
}

/** A synthetic gap computed from hunk coordinates when original file content is unavailable. */
export interface DiffGapSummary {
  /** Number of hidden old-file lines between hunks. */
  lineCount: number
  /** 1-based line number of the first hidden line. */
  startLineNumber: number
}
