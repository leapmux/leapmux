import type { JSX } from 'solid-js'
import type { WordDiffPart } from './diffCompute'
import type { DiffLineEntry, DiffLineMarks, DiffMarkSources, LineMarksSource, SplitLineEntry, SplitRowEntry, SplitRowMarks, StructuredPatchHunk } from './diffTypes'
import type { CachedToken } from '~/lib/tokenCache'
import { For } from 'solid-js'
import { joinClassNames } from '~/lib/classNames'
import { changeBlockAt } from './diffBuilder'
import { pairKey } from './diffCompute'
import { diffAddedInline, diffRemovedInline } from './diffStyles.css'

/**
 * Render a line's text using Shiki tokens when available.
 * Falls back to plain text if no tokens are provided.
 */
export function renderTokenizedLine(text: string, tokens: CachedToken[] | null): JSX.Element | string {
  if (!tokens)
    return text
  return (
    <For each={tokens}>
      {token => (
        // `data-shiki-token` marks the span as a syntax token so the diff
        // surfaces' dual-theme color rule targets it (see diffStyles.css.ts);
        // the token's style lives in the shared class (shikiStyleClass).
        <span data-shiki-token class={token.className}>{token.content}</span>
      )}
    </For>
  ) as JSX.Element
}

/**
 * Split Shiki tokens according to word-diff segments and wrap
 * changed segments with the given CSS class.
 *
 * Walks through word-diff parts and Shiki tokens simultaneously,
 * splitting tokens at word-diff boundaries so each resulting fragment
 * gets both the Shiki foreground color and the diff background highlight.
 */
function renderTokenizedWordDiff(
  wordDiffParts: readonly WordDiffPart[],
  tokens: CachedToken[] | null,
  highlightClass: string,
  filterFn: (part: { added?: boolean, removed?: boolean }) => boolean,
): JSX.Element {
  const filteredParts = wordDiffParts.filter(filterFn)

  if (!tokens) {
    // No syntax tokens — fall back to plain word-diff rendering. `undefined`
    // (not '') for the unchanged parts, so Solid omits the class attribute
    // instead of stamping an empty class="".
    return (
      <For each={filteredParts}>
        {p => (
          <span class={(p.added || p.removed)
            ? highlightClass
            : undefined}
          >
            {p.value}
          </span>
        )}
      </For>
    ) as JSX.Element
  }

  // Build a flat list of fragments: each fragment has the Shiki style class + optional diff class
  const fragments: Array<{ text: string, tokenClass: string | undefined, diffClass: string | undefined }> = []

  let tokenIdx = 0
  let tokenOffset = 0 // char offset within current token

  for (const part of filteredParts) {
    const diffClass = (part.added || part.removed)
      ? highlightClass
      : undefined
    let remaining = part.value.length
    let partPos = 0

    while (remaining > 0 && tokenIdx < tokens.length) {
      const token = tokens[tokenIdx]
      if (token === undefined)
        break
      const available = token.content.length - tokenOffset
      const take = Math.min(remaining, available)

      fragments.push({
        text: part.value.slice(partPos, partPos + take),
        tokenClass: token.className,
        diffClass,
      })

      partPos += take
      remaining -= take
      tokenOffset += take

      if (tokenOffset >= token.content.length) {
        tokenIdx++
        tokenOffset = 0
      }
    }

    // If tokens are exhausted but part text remains, render unstyled
    if (remaining > 0) {
      fragments.push({
        text: part.value.slice(partPos),
        tokenClass: undefined,
        diffClass,
      })
    }
  }

  return (
    <For each={fragments}>
      {f => (
        // `data-shiki-token` scopes the diff surfaces' dual-theme color rule to
        // these fragments (see renderTokenizedLine). The class composes the
        // shared Shiki style class with the word-diff highlight; `undefined`
        // omits the attribute entirely when neither applies.
        <span data-shiki-token class={joinClassNames(f.tokenClass, f.diffClass)}>{f.text}</span>
      )}
    </For>
  ) as JSX.Element
}

/**
 * Draw the text of one side of a diff line with its marks. The diff worker
 * computed the word diff of a paired line with the line diff (see diffCompute):
 * a removed line highlights the words that the pair removes, and an added line
 * the words that it adds. A line without a word diff draws whole.
 */
export function renderMarkedLine(text: string, type: SplitLineEntry['type'], marks: DiffLineMarks): JSX.Element | string {
  if (marks.words !== undefined && type === 'removed')
    return renderTokenizedWordDiff(marks.words, marks.tokens, diffRemovedInline, p => !p.added)
  if (marks.words !== undefined && type === 'added')
    return renderTokenizedWordDiff(marks.words, marks.tokens, diffAddedInline, p => !p.removed)
  return renderTokenizedLine(text, marks.tokens)
}

/**
 * Whether two marks draw the same. Each compares by identity: a line keeps the
 * same tokens and the same word diff until the tokenizer or the diff worker
 * answers again.
 */
export function sameLineMarks(a: DiffLineMarks, b: DiffLineMarks): boolean {
  return a.tokens === b.tokens && a.words === b.words
}

/** Whether two marks of a split row draw the same on both sides. */
export function sameSplitRowMarks(a: SplitRowMarks, b: SplitRowMarks): boolean {
  return sameLineMarks(a.left, b.left) && sameLineMarks(a.right, b.right)
}

/** The marks of a line that has no tokens and no word diff. */
const PLAIN_MARKS: DiffLineMarks = { tokens: null, words: undefined }

/**
 * The marks of one side of a line, from the latest sources: the tokens of its
 * line, and the word diff of its pair. A side without a source draws plain.
 */
function lineMarks(source: LineMarksSource | null, sources: DiffMarkSources): DiffLineMarks {
  if (source === null)
    return PLAIN_MARKS
  const tokens = source.side === 'old'
    ? sources.oldTokens
    : sources.newTokens
  return {
    tokens: tokens?.[source.tokenLine] ?? null,
    words: source.pair === undefined
      ? undefined
      : sources.wordDiffs.get(source.pair),
  }
}

/** The marks of a line of the unified view, from the latest sources. */
export function unifiedLineMarks(line: DiffLineEntry, sources: DiffMarkSources): DiffLineMarks {
  return lineMarks(line.marksSource, sources)
}

/** The marks of both sides of a row of the split view, from the latest sources. */
export function splitRowMarks(row: SplitRowEntry, sources: DiffMarkSources): SplitRowMarks {
  return {
    left: lineMarks(row.left.marksSource, sources),
    right: lineMarks(row.right.marksSource, sources),
  }
}

/** The cell of a split row that has no line on its side. */
const EMPTY_SPLIT_LINE: SplitLineEntry = {
  text: '',
  type: 'empty',
  num: null,
  marksSource: null,
}

/**
 * Discriminated event yielded by `walkHunks`. Captures the structural shape of
 * a diff line (paired removal/addition, unpaired addition/removal, or context)
 * without committing to a particular output shape — `buildUnifiedLines` and
 * `buildSplitRows` translate the same event stream into different layouts.
 * The token lines index the tokens of each side (see LineMarksSource).
 */
type HunkEvent
  = | { kind: 'context', oldNum: number, newNum: number, content: string, oldTokenLine: number, newTokenLine: number, hunkIndex: number }
    | { kind: 'paired', oldNum: number, newNum: number, removedContent: string, addedContent: string, oldTokenLine: number, newTokenLine: number, pair: string, hunkIndex: number }
    | { kind: 'removed', oldNum: number, content: string, tokenLine: number, hunkIndex: number }
    | { kind: 'added', newNum: number, content: string, tokenLine: number, hunkIndex: number }
  /**
   * Synthetic delimiter emitted after each `-`/`+` block. Lets unified-view
   * consumers flush queued '+' rows so the original ordering ("all removals
   * before all additions within a block") is preserved when two blocks abut
   * with no context line between them.
   */
    | { kind: 'blockEnd' }

/**
 * Walk hunks and emit one event per logical diff line. Owns the
 * cross-hunk token-line counters internally so consumers see a flat stream.
 * Removed lines that pair up with subsequent added lines emit `paired` events
 * (so consumers can render inline word diffs); leftover removals/additions
 * emit `removed`/`added`.
 */
function walkHunks(hunks: readonly StructuredPatchHunk[], emit: (event: HunkEvent) => void): void {
  let oldTokenLine = 0
  let newTokenLine = 0

  for (let hi = 0; hi < hunks.length; hi++) {
    const hunk = hunks[hi]
    if (hunk === undefined)
      continue
    let oldLine = hunk.oldStart
    let newLine = hunk.newStart
    const lines = hunk.lines
    let i = 0

    while (i < lines.length) {
      const block = changeBlockAt(lines, i)
      if (block === undefined) {
        emit({
          kind: 'context',
          oldNum: oldLine++,
          newNum: newLine++,
          content: lines[i]!.slice(1),
          oldTokenLine: oldTokenLine++,
          newTokenLine: newTokenLine++,
          hunkIndex: hi,
        })
        i++
        continue
      }

      // A change block pairs its n-th removal with its n-th addition. A bare `+`
      // block (no preceding `-`) takes the same path with zero removals so that a
      // `blockEnd` is always emitted — the unified-view consumer relies on
      // `blockEnd` to flush queued `+` rows before the next context line.
      const removed = block.addedStart - i
      const added = block.end - block.addedStart
      const paired = Math.min(removed, added)
      for (let j = 0; j < paired; j++) {
        const removedContent = lines[i + j]!.slice(1)
        const addedContent = lines[block.addedStart + j]!.slice(1)
        emit({
          kind: 'paired',
          oldNum: oldLine++,
          newNum: newLine++,
          removedContent,
          addedContent,
          oldTokenLine: oldTokenLine + j,
          newTokenLine: newTokenLine + j,
          pair: pairKey(removedContent, addedContent),
          hunkIndex: hi,
        })
      }
      for (let j = paired; j < removed; j++) {
        emit({
          kind: 'removed',
          oldNum: oldLine++,
          content: lines[i + j]!.slice(1),
          tokenLine: oldTokenLine + j,
          hunkIndex: hi,
        })
      }
      for (let j = paired; j < added; j++) {
        emit({
          kind: 'added',
          newNum: newLine++,
          content: lines[block.addedStart + j]!.slice(1),
          tokenLine: newTokenLine + j,
          hunkIndex: hi,
        })
      }
      emit({ kind: 'blockEnd' })
      oldTokenLine += removed
      newTokenLine += added
      i = block.end
    }
  }
}

/**
 * Build the lines of the unified view from structuredPatch hunks. Each line
 * states where its marks are (see LineMarksSource), and a view reads them with
 * unifiedLineMarks, so the lines of a diff stay the same when its tokens or its
 * word diffs land.
 */
export function buildUnifiedLines(hunks: readonly StructuredPatchHunk[]): DiffLineEntry[] {
  const result: DiffLineEntry[] = []
  // Within a `-`/`+` block, emit all '-' rows first, then all '+' rows. Queue
  // pending '+' rows and flush on `blockEnd`.
  const pendingAdded: DiffLineEntry[] = []
  const flushPending = () => {
    for (const added of pendingAdded)
      result.push(added)
    pendingAdded.length = 0
  }
  const added = (newNum: number, text: string, marksSource: LineMarksSource, hunkIndex: number): DiffLineEntry => ({
    oldNum: null,
    newNum,
    prefix: '+',
    text,
    type: 'added',
    hunkIndex,
    marksSource,
  })

  walkHunks(hunks, (e) => {
    if (e.kind === 'blockEnd') {
      flushPending()
      return
    }
    if (e.kind === 'context') {
      result.push({
        oldNum: e.oldNum,
        newNum: e.newNum,
        prefix: ' ',
        text: e.content,
        type: 'context',
        hunkIndex: e.hunkIndex,
        marksSource: { side: 'old', tokenLine: e.oldTokenLine, pair: undefined },
      })
      return
    }
    if (e.kind === 'paired') {
      result.push({
        oldNum: e.oldNum,
        newNum: null,
        prefix: '-',
        text: e.removedContent,
        type: 'removed',
        hunkIndex: e.hunkIndex,
        marksSource: { side: 'old', tokenLine: e.oldTokenLine, pair: e.pair },
      })
      pendingAdded.push(added(e.newNum, e.addedContent, { side: 'new', tokenLine: e.newTokenLine, pair: e.pair }, e.hunkIndex))
      return
    }
    if (e.kind === 'removed') {
      result.push({
        oldNum: e.oldNum,
        newNum: null,
        prefix: '-',
        text: e.content,
        type: 'removed',
        hunkIndex: e.hunkIndex,
        marksSource: { side: 'old', tokenLine: e.tokenLine, pair: undefined },
      })
      return
    }
    // Bare '+' (without preceding '-') queues so it follows any pending '-' rows in the same block.
    pendingAdded.push(added(e.newNum, e.content, { side: 'new', tokenLine: e.tokenLine, pair: undefined }, e.hunkIndex))
  })
  flushPending()
  return result
}

/**
 * Build the rows of the split view from structuredPatch hunks, as
 * buildUnifiedLines builds the lines of the unified view. A row pairs a removed
 * line with the added line beside it, and draws an empty cell on the side that
 * has no line. A view reads the marks of a row with splitRowMarks.
 */
export function buildSplitRows(hunks: readonly StructuredPatchHunk[]): SplitRowEntry[] {
  const rows: SplitRowEntry[] = []

  walkHunks(hunks, (e) => {
    if (e.kind === 'blockEnd')
      return
    if (e.kind === 'context') {
      rows.push({
        left: {
          text: e.content,
          type: 'context',
          num: e.oldNum,
          marksSource: { side: 'old', tokenLine: e.oldTokenLine, pair: undefined },
        },
        right: {
          text: e.content,
          type: 'context',
          num: e.newNum,
          marksSource: { side: 'new', tokenLine: e.newTokenLine, pair: undefined },
        },
        hunkIndex: e.hunkIndex,
      })
      return
    }
    if (e.kind === 'paired') {
      rows.push({
        left: {
          text: e.removedContent,
          type: 'removed',
          num: e.oldNum,
          marksSource: { side: 'old', tokenLine: e.oldTokenLine, pair: e.pair },
        },
        right: {
          text: e.addedContent,
          type: 'added',
          num: e.newNum,
          marksSource: { side: 'new', tokenLine: e.newTokenLine, pair: e.pair },
        },
        hunkIndex: e.hunkIndex,
      })
      return
    }
    if (e.kind === 'removed') {
      rows.push({
        left: {
          text: e.content,
          type: 'removed',
          num: e.oldNum,
          marksSource: { side: 'old', tokenLine: e.tokenLine, pair: undefined },
        },
        right: EMPTY_SPLIT_LINE,
        hunkIndex: e.hunkIndex,
      })
      return
    }
    rows.push({
      left: EMPTY_SPLIT_LINE,
      right: {
        text: e.content,
        type: 'added',
        num: e.newNum,
        marksSource: { side: 'new', tokenLine: e.tokenLine, pair: undefined },
      },
      hunkIndex: e.hunkIndex,
    })
  })

  return rows
}
