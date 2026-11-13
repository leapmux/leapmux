import type { DiffInput, PatchDiffInput, StructuredPatchHunk, TextsDiffInput } from './diffTypes'
import { changeBlockAt, DIFF_DRAWN_LINE_LIMIT } from './diffBuilder'

// ---------------------------------------------------------------------------
// What a diff view draws, and the diffs that need no search
//
// The types of a computed diff, its limits, and every diff that is linear in its
// input: a created, a deleted or an unchanged file, a patch before its word
// diffs, and the fallback of a diff that the worker could not compute. The main
// thread computes these itself (see diffWorkerClient).
//
// The search itself is in diffSearch, which only the diff worker loads. jsdiff
// declares no `sideEffects` in its package.json, so the bundler keeps the whole
// library in every chunk that imports it, used or not. A jsdiff import here
// would put it back in the main chunk.
// ---------------------------------------------------------------------------

/** One word-diff segment, as the `diff` package emits it. */
export interface WordDiffPart {
  value: string
  added?: boolean
  removed?: boolean
}

/** The word diffs of a computed diff, by {@link pairKey}. */
export type WordDiffs = ReadonlyMap<string, WordDiffPart[]>

/**
 * Why the hunks of a diff remove the whole old text and add the whole new one in
 * place of the changed lines:
 *
 * - `too-many-edits`: the texts need more line edits than the `maxLineEdits` of
 *   the input (see DiffInput).
 * - `not-computed`: the diff worker failed, or it ran past its time budget.
 */
export type WholeReplacementReason = 'too-many-edits' | 'not-computed'

/** What a diff view draws: the hunks, and the word diff of each paired line. */
export interface ComputedDiff {
  hunks: StructuredPatchHunk[]
  /**
   * The word diff of each paired removed/added line that a view draws: the lines
   * that limitDiffLines keeps at `drawnLineLimit`. A pair that took more than
   * WORD_DIFF_MAX_EDITS (see diffSearch) edits has none, and draws as one removed line and one
   * added line.
   */
  wordDiffs: WordDiffs
  /**
   * Why the hunks replace the whole old text in place of the changed lines, or
   * null when they state the changed lines. The replacement draws every line as
   * changed, so a view states the reason.
   */
  wholeReplacement: WholeReplacementReason | null
  /**
   * The most diff lines that a view draws: the `drawnLineLimit` of the input (see
   * DiffInput). DiffView draws these lines only, so a view cannot draw a line
   * whose word diff the diff did not compute.
   */
  drawnLineLimit: number
}

/**
 * The most line edits (removals plus additions) that the diff of a chat row looks
 * for, and the default `maxLineEdits` of a DiffInput. Past it the edit is a
 * rewrite, which a minimal diff does not make easier to read, and the search
 * would cost O((N + M) * D): it gives up after 46 ms on 10,000 lines.
 */
export const LINE_DIFF_MAX_EDITS = 1000

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
 * Whether removing the whole old text and adding the whole new one is itself the
 * minimal diff: one side is empty, or the two are equal.
 */
function wholeReplacementIsMinimal(oldText: string, newText: string): boolean {
  return oldText === '' || newText === '' || oldText === newText
}

/**
 * The diff that removes the whole old text and adds the whole new one, which
 * states `reason` unless that replacement is the minimal diff. A rewrite pairs
 * unrelated lines, so its word diffs would mark everything.
 */
export function wholeReplacementDiff(input: TextsDiffInput, reason: WholeReplacementReason): ComputedDiff {
  const { oldText, newText } = input
  return {
    hunks: replacedWholeHunks(oldText, newText),
    wordDiffs: new Map(),
    wholeReplacement: wholeReplacementIsMinimal(oldText, newText)
      ? null
      : reason,
    drawnLineLimit: drawnLineLimitOf(input),
  }
}

/**
 * The diff of `input` when it needs no search, or undefined. Two sides whose
 * whole replacement is the minimal diff (one side is empty, or the two are
 * equal) draw that replacement. It pairs no lines, so it has no word diffs.
 * It is linear in the input, so the calling thread may compute it. The search
 * would also give up on a created or a deleted file of more lines than the
 * `maxLineEdits` of the input.
 */
export function diffWithoutSearch(input: DiffInput): ComputedDiff | undefined {
  if (input.kind !== 'texts' || !wholeReplacementIsMinimal(input.oldText, input.newText))
    return undefined
  return {
    hunks: replacedWholeHunks(input.oldText, input.newText),
    wordDiffs: new Map(),
    wholeReplacement: null,
    drawnLineLimit: drawnLineLimitOf(input),
  }
}

/**
 * The diff of a patch with no word diffs: its own hunks, which need no search. A
 * view draws it until the diff worker computes the word diffs, and in place of a
 * diff that the worker could not compute.
 */
export function patchWithoutWordDiffs(input: PatchDiffInput): ComputedDiff {
  return {
    hunks: input.hunks,
    wordDiffs: new Map(),
    wholeReplacement: null,
    drawnLineLimit: drawnLineLimitOf(input),
  }
}

/**
 * Call `pair` for each removed line that a view draws beside an added one:
 * the n-th removal of each change block with its n-th addition (see changeBlockAt).
 */
export function forEachPairedLine(hunks: readonly StructuredPatchHunk[], pair: (removed: string, added: string) => void): void {
  for (const { lines } of hunks) {
    let i = 0
    while (i < lines.length) {
      const block = changeBlockAt(lines, i)
      if (block === undefined) {
        i++
        continue
      }
      const paired = Math.min(block.addedStart - i, block.end - block.addedStart)
      for (let j = 0; j < paired; j++)
        pair(lines[i + j]!.slice(1), lines[block.addedStart + j]!.slice(1))
      i = block.end
    }
  }
}

/** The most diff lines that the view of `input` draws (see DiffInput). */
export function drawnLineLimitOf(input: DiffInput): number {
  return input.drawnLineLimit ?? DIFF_DRAWN_LINE_LIMIT
}

/** The most line edits that the diff of `input` looks for (see DiffInput). */
export function maxLineEditsOf(input: TextsDiffInput): number {
  return input.maxLineEdits ?? LINE_DIFF_MAX_EDITS
}

/**
 * The diff to draw when the worker could not compute one: the whole old text
 * replaced, or the given hunks without word diffs. Linear in the input.
 */
export function fallbackDiff(input: DiffInput): ComputedDiff {
  return input.kind === 'texts'
    ? wholeReplacementDiff(input, 'not-computed')
    : patchWithoutWordDiffs(input)
}
