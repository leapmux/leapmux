import type { JSX } from 'solid-js'

/** A hunk from the structuredPatch format in tool_use_result. */
export interface StructuredPatchHunk {
  oldStart: number
  oldLines: number
  newStart: number
  newLines: number
  lines: string[]
}

/** A single diff line with optional JSX content for inline highlights. */
export interface DiffLineEntry {
  oldNum: number | null
  newNum: number | null
  prefix: string
  content: JSX.Element | string
  type: 'added' | 'removed' | 'context'
  /** Index of the hunk this line belongs to (for gap separators). */
  hunkIndex: number
}

/** A single split diff line with optional JSX content. */
export interface SplitLineEntry {
  content: JSX.Element | string
  type: 'removed' | 'added' | 'context' | 'empty'
  num: number | null
  /** Index of the hunk this line belongs to (for gap separators). */
  hunkIndex: number
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
