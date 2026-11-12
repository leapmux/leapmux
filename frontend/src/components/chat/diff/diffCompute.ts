import type { StructuredPatchHunk } from './diffTypes'
import { diffWordsWithSpace, structuredPatch } from 'diff'

// ---------------------------------------------------------------------------
// Diff computation, shared by the diff worker and the main thread's fallback
//
// The Myers diff costs O((N + M) * D), and nothing limited it: 5,000 re-indented
// lines took 2.2 s, and one rewritten 27 KB line took 2.8 s for its word diff.
// The diff worker runs this module (see diffWorkerClient). Pure TypeScript with no
// JSX, so the worker can import it.
// ---------------------------------------------------------------------------

/** One word-diff segment, as the `diff` package emits it. */
export interface WordDiffPart {
  value: string
  added?: boolean
  removed?: boolean
}

/** What a diff view draws: the hunks, and the word diff of each paired line. */
export interface ComputedDiff {
  hunks: StructuredPatchHunk[]
  /**
   * The word diff of each paired removed/added line, by {@link pairKey}. A pair
   * that took more than WORD_DIFF_MAX_EDITS edits has none, and draws as one
   * removed line and one added line.
   */
  wordDiffs: Map<string, WordDiffPart[]>
  /**
   * True when the two texts needed more than LINE_DIFF_MAX_EDITS line edits, so
   * the hunks remove the whole old text and add the whole new one.
   */
  replacedWhole: boolean
}

/** What to diff: two texts, or the hunks of a patch that a tool already computed. */
export type DiffInput
  = | { kind: 'texts', oldText: string, newText: string }
    | { kind: 'hunks', hunks: StructuredPatchHunk[] }

/** Unchanged lines kept around each change; the gap separators reveal the rest. */
export const DIFF_CONTEXT_LINES = 3

/**
 * The most line edits (removals plus additions) the line diff looks for. Past it
 * the edit is a rewrite, which a minimal diff does not make easier to read, and
 * the search would cost O((N + M) * D): it gives up after 46 ms on 10,000 lines.
 */
export const LINE_DIFF_MAX_EDITS = 1000

/** The most word edits one paired line takes before it draws as a whole-line change. */
export const WORD_DIFF_MAX_EDITS = 200

const NO_NEWLINE_MARKER = '\\'
const TRAILING_NEWLINE_RE = /\n$/

/** The key of one paired line in {@link ComputedDiff.wordDiffs}. The length prefix keeps it unambiguous. */
export function pairKey(removed: string, added: string): string {
  return `${removed.length}\u0000${removed}\u0000${added}`
}

/** The lines of `text`, without the empty entry that a final newline leaves. */
function textLines(text: string): string[] {
  return text === ''
    ? []
    : text.replace(TRAILING_NEWLINE_RE, '').split('\n')
}

/** One hunk that removes every line of `oldText` and adds every line of `newText`; none when they are equal. */
export function replacedWholeHunks(oldText: string, newText: string): StructuredPatchHunk[] {
  if (oldText === newText)
    return []
  const removed = textLines(oldText)
  const added = textLines(newText)
  return [
    {
      oldStart: 1,
      oldLines: removed.length,
      newStart: 1,
      newLines: added.length,
      lines: [
        ...removed.map(line => `-${line}`),
        ...added.map(line => `+${line}`),
      ],
    },
  ]
}

/**
 * The hunks between two texts, with DIFF_CONTEXT_LINES of context, or undefined
 * when they need more than LINE_DIFF_MAX_EDITS line edits.
 */
function textHunks(oldText: string, newText: string): StructuredPatchHunk[] | undefined {
  const patch = structuredPatch('', '', oldText, newText, '', '', {
    context: DIFF_CONTEXT_LINES,
    maxEditLength: LINE_DIFF_MAX_EDITS,
  })
  if (patch === undefined)
    return undefined
  // The "\ No newline at end of file" markers describe the files, not their lines,
  // and the views draw lines only.
  return patch.hunks.map(hunk => ({
    oldStart: hunk.oldStart,
    oldLines: hunk.oldLines,
    newStart: hunk.newStart,
    newLines: hunk.newLines,
    lines: hunk.lines.filter(line => !line.startsWith(NO_NEWLINE_MARKER)),
  }))
}

/**
 * Call `pair` for each removed line that a view draws beside an added one: within
 * each run of removals followed by additions, the n-th removal with the n-th
 * addition, as `walkHunks` pairs them.
 */
export function forEachPairedLine(hunks: readonly StructuredPatchHunk[], pair: (removed: string, added: string) => void): void {
  for (const hunk of hunks) {
    const lines = hunk.lines
    let i = 0
    while (i < lines.length) {
      if (lines[i]?.[0] !== '-') {
        i++
        continue
      }
      const removed: string[] = []
      while (i < lines.length && lines[i]?.[0] === '-')
        removed.push(lines[i++]!.slice(1))
      const added: string[] = []
      while (i < lines.length && lines[i]?.[0] === '+')
        added.push(lines[i++]!.slice(1))
      const paired = Math.min(removed.length, added.length)
      for (let j = 0; j < paired; j++)
        pair(removed[j]!, added[j]!)
    }
  }
}

/** The word diff of each paired line of `hunks` that takes at most WORD_DIFF_MAX_EDITS edits. */
function pairedWordDiffs(hunks: readonly StructuredPatchHunk[]): Map<string, WordDiffPart[]> {
  const wordDiffs = new Map<string, WordDiffPart[]>()
  forEachPairedLine(hunks, (removed, added) => {
    const key = pairKey(removed, added)
    if (wordDiffs.has(key))
      return
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
      wordDiffs: pairedWordDiffs(input.hunks),
      replacedWhole: false,
    }
  }
  const hunks = textHunks(input.oldText, input.newText)
  if (hunks === undefined) {
    // A rewrite pairs unrelated lines, so its word diffs would mark everything.
    return {
      hunks: replacedWholeHunks(input.oldText, input.newText),
      wordDiffs: new Map(),
      replacedWhole: true,
    }
  }
  return {
    hunks,
    wordDiffs: pairedWordDiffs(hunks),
    replacedWhole: false,
  }
}

/**
 * The diff to draw when the worker could not compute one: the whole old text
 * replaced, or the given hunks without word diffs. Linear in the input.
 */
export function fallbackDiff(input: DiffInput): ComputedDiff {
  return input.kind === 'texts'
    ? {
        hunks: replacedWholeHunks(input.oldText, input.newText),
        wordDiffs: new Map(),
        replacedWhole: input.oldText !== input.newText,
      }
    : {
        hunks: input.hunks,
        wordDiffs: new Map(),
        replacedWhole: false,
      }
}
