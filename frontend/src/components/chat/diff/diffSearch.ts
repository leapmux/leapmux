import type { ComputedDiff, WordDiffPart } from './diffCompute'
import type { DiffInput, StructuredPatchHunk, TextsDiffInput } from './diffTypes'
import { diffWordsWithSpace, structuredPatch } from 'diff'
import { limitDiffLines } from './diffBuilder'
import { diffWithoutSearch, drawnLineLimitOf, forEachPairedLine, maxLineEditsOf, pairKey, wholeReplacementDiff } from './diffCompute'
import { isNoNewlineMarker } from './unifiedDiffParser'

// ---------------------------------------------------------------------------
// The diff search, which only the diff worker runs
//
// The Myers diff costs O((N + M) * D), and nothing limited it: 5,000 re-indented
// lines took 2.2 s, and one rewritten 27 KB line took 2.8 s for its word diff.
// The diff worker runs this module (see diffWorkerClient). Nothing on the main
// thread may import it: jsdiff declares no `sideEffects`, so one import keeps the
// whole library in the main chunk (see diffCompute).
// ---------------------------------------------------------------------------

/** Unchanged lines kept around each change; the gap separators reveal the rest. */
export const DIFF_CONTEXT_LINES = 3

/** The most word edits one paired line takes before it draws as a whole-line change. */
export const WORD_DIFF_MAX_EDITS = 200

/**
 * The hunks between two texts, with DIFF_CONTEXT_LINES of context, or undefined
 * when they need more line edits than the `maxLineEdits` of the input.
 */
function textHunks(input: TextsDiffInput): StructuredPatchHunk[] | undefined {
  const patch = structuredPatch('', '', input.oldText, input.newText, '', '', {
    context: DIFF_CONTEXT_LINES,
    maxEditLength: maxLineEditsOf(input),
  })
  if (patch === undefined)
    return undefined
  return patch.hunks.map(hunk => ({
    oldStart: hunk.oldStart,
    oldLines: hunk.oldLines,
    newStart: hunk.newStart,
    newLines: hunk.newLines,
    lines: hunk.lines.filter(line => !isNoNewlineMarker(line)),
  }))
}

/**
 * The word diff of each paired line of `hunks` that a view draws and that takes
 * at most WORD_DIFF_MAX_EDITS edits. A view draws the lines that limitDiffLines
 * keeps at `drawnLineLimit`, so a pair past them would cost a search that
 * nothing reads.
 */
function drawnWordDiffs(hunks: StructuredPatchHunk[], drawnLineLimit: number): Map<string, WordDiffPart[]> {
  const wordDiffs = new Map<string, WordDiffPart[]>()
  // A pair that repeats, such as a line of a generated block, costs one search,
  // also when that search gives up and stores nothing.
  const searched = new Set<string>()
  forEachPairedLine(limitDiffLines(hunks, drawnLineLimit).hunks, (removed, added) => {
    const key = pairKey(removed, added)
    if (searched.has(key))
      return
    searched.add(key)
    // diffWordsWithSpace (not diffWords) keeps whitespace runs as segments of their
    // own. diffWords attaches them to a neighbouring word from either side, which
    // corrupts the indentation of two lines indented differently.
    const parts = diffWordsWithSpace(removed, added, { maxEditLength: WORD_DIFF_MAX_EDITS })
    if (parts !== undefined)
      wordDiffs.set(key, parts)
  })
  return wordDiffs
}

/** Compute what a diff view draws for `input`. */
export function computeDiff(input: DiffInput): ComputedDiff {
  if (input.kind === 'hunks') {
    return {
      hunks: input.hunks,
      wordDiffs: drawnWordDiffs(input.hunks, drawnLineLimitOf(input)),
      wholeReplacement: null,
      drawnLineLimit: drawnLineLimitOf(input),
    }
  }
  const known = diffWithoutSearch(input)
  if (known !== undefined)
    return known
  const hunks = textHunks(input)
  if (hunks === undefined)
    return wholeReplacementDiff(input, 'too-many-edits')
  return {
    hunks,
    wordDiffs: drawnWordDiffs(hunks, drawnLineLimitOf(input)),
    wholeReplacement: null,
    drawnLineLimit: drawnLineLimitOf(input),
  }
}
