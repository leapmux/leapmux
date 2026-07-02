import type { JSX } from 'solid-js'
import type { DiffLineEntry, SplitLineEntry, StructuredPatchHunk } from './diffTypes'
import type { CachedToken } from '~/lib/tokenCache'
import { For } from 'solid-js'
import { diffAddedInline, diffRemovedInline } from './diffStyles.css'
import { pairedWordDiff } from './wordDiffCache'

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
  wordDiffParts: Array<{ value: string, added?: boolean, removed?: boolean }>,
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
          <span class={(p.added || p.removed) ? highlightClass : undefined}>{p.value}</span>
        )}
      </For>
    ) as JSX.Element
  }

  // Build a flat list of fragments: each fragment has the Shiki style class + optional diff class
  const fragments: Array<{ text: string, tokenClass: string | undefined, diffClass: string | undefined }> = []

  let tokenIdx = 0
  let tokenOffset = 0 // char offset within current token

  for (const part of filteredParts) {
    const diffClass = (part.added || part.removed) ? highlightClass : undefined
    let remaining = part.value.length
    let partPos = 0

    while (remaining > 0 && tokenIdx < tokens.length) {
      const token = tokens[tokenIdx]
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
        <span data-shiki-token class={joinClasses(f.tokenClass, f.diffClass)}>{f.text}</span>
      )}
    </For>
  ) as JSX.Element
}

/** Join optional class names, or undefined so Solid omits the attribute. */
function joinClasses(a: string | undefined, b: string | undefined): string | undefined {
  if (a && b)
    return `${a} ${b}`
  return a || b || undefined
}

/**
 * Render inline word-level highlights for a removed line. The word diff is
 * computed via pairedWordDiff (see wordDiffCache): memoized, so the added-
 * side renderer's identical call and every later re-render (premeasure +
 * visible double mount, token arrival, view toggles) reuse it.
 */
function renderRemovedInline(
  oldLine: string,
  newLine: string,
  oldTokens: CachedToken[] | null,
): JSX.Element {
  const parts = pairedWordDiff(oldLine, newLine)
  return renderTokenizedWordDiff(
    parts,
    oldTokens,
    diffRemovedInline,
    p => !p.added,
  )
}

/** Render inline word-level highlights for an added line. */
function renderAddedInline(
  oldLine: string,
  newLine: string,
  newTokens: CachedToken[] | null,
): JSX.Element {
  const parts = pairedWordDiff(oldLine, newLine)
  return renderTokenizedWordDiff(
    parts,
    newTokens,
    diffAddedInline,
    p => !p.removed,
  )
}

/**
 * Discriminated event yielded by `walkHunks`. Captures the structural shape of
 * a diff line (paired removal/addition, unpaired addition/removal, or context)
 * without committing to a particular output shape — `buildUnifiedLines` and
 * `buildSplitLines` translate the same event stream into different layouts.
 */
type HunkEvent
  = | { kind: 'context', oldNum: number, newNum: number, content: string, oldTokens: CachedToken[] | null, newTokens: CachedToken[] | null, hunkIndex: number }
    | { kind: 'paired', oldNum: number, newNum: number, removedContent: string, addedContent: string, oldTokens: CachedToken[] | null, newTokens: CachedToken[] | null, hunkIndex: number }
    | { kind: 'removed', oldNum: number, content: string, tokens: CachedToken[] | null, hunkIndex: number }
    | { kind: 'added', newNum: number, content: string, tokens: CachedToken[] | null, hunkIndex: number }
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
function walkHunks(
  hunks: StructuredPatchHunk[],
  oldTokens: CachedToken[][] | null,
  newTokens: CachedToken[][] | null,
  emit: (event: HunkEvent) => void,
): void {
  let oldTokenLine = 0
  let newTokenLine = 0

  for (let hi = 0; hi < hunks.length; hi++) {
    const hunk = hunks[hi]
    let oldLine = hunk.oldStart
    let newLine = hunk.newStart
    const lines = hunk.lines
    let i = 0

    while (i < lines.length) {
      const prefix = lines[i][0] || ' '

      if (prefix === '-' || prefix === '+') {
        // Collect consecutive removed lines, then consecutive added lines for
        // pairing. A bare `+` block (no preceding `-`) takes the same path
        // with zero removals so that a `blockEnd` is always emitted — the
        // unified-view consumer relies on `blockEnd` to flush queued `+`
        // rows before the next context line.
        const removedLines: string[] = []
        const removedTokenIndices: number[] = []
        while (i < lines.length && lines[i][0] === '-') {
          removedLines.push(lines[i].slice(1))
          removedTokenIndices.push(oldTokenLine++)
          i++
        }
        const addedLines: string[] = []
        const addedTokenIndices: number[] = []
        while (i < lines.length && lines[i][0] === '+') {
          addedLines.push(lines[i].slice(1))
          addedTokenIndices.push(newTokenLine++)
          i++
        }

        const paired = Math.min(removedLines.length, addedLines.length)
        for (let j = 0; j < paired; j++) {
          emit({
            kind: 'paired',
            oldNum: oldLine++,
            newNum: newLine++,
            removedContent: removedLines[j],
            addedContent: addedLines[j],
            oldTokens: oldTokens?.[removedTokenIndices[j]] ?? null,
            newTokens: newTokens?.[addedTokenIndices[j]] ?? null,
            hunkIndex: hi,
          })
        }
        for (let j = paired; j < removedLines.length; j++) {
          emit({
            kind: 'removed',
            oldNum: oldLine++,
            content: removedLines[j],
            tokens: oldTokens?.[removedTokenIndices[j]] ?? null,
            hunkIndex: hi,
          })
        }
        for (let j = paired; j < addedLines.length; j++) {
          emit({
            kind: 'added',
            newNum: newLine++,
            content: addedLines[j],
            tokens: newTokens?.[addedTokenIndices[j]] ?? null,
            hunkIndex: hi,
          })
        }
        emit({ kind: 'blockEnd' })
      }
      else {
        const text = lines[i].slice(1)
        emit({
          kind: 'context',
          oldNum: oldLine++,
          newNum: newLine++,
          content: text,
          oldTokens: oldTokens?.[oldTokenLine] ?? null,
          newTokens: newTokens?.[newTokenLine] ?? null,
          hunkIndex: hi,
        })
        oldTokenLine++
        newTokenLine++
        i++
      }
    }
  }
}

/** Build unified diff lines from structuredPatch hunks with optional syntax tokens. */
export function buildUnifiedLines(
  hunks: StructuredPatchHunk[],
  oldTokens: CachedToken[][] | null,
  newTokens: CachedToken[][] | null,
): DiffLineEntry[] {
  const result: DiffLineEntry[] = []
  // Within a `-`/`+` block, emit all '-' rows first, then all '+' rows. Queue
  // pending '+' rows and flush on `blockEnd`.
  const pendingAdded: Array<{ newNum: number, content: JSX.Element, hunkIndex: number }> = []
  const flushPending = () => {
    for (const a of pendingAdded)
      result.push({ oldNum: null, newNum: a.newNum, prefix: '+', content: a.content, type: 'added', hunkIndex: a.hunkIndex })
    pendingAdded.length = 0
  }

  walkHunks(hunks, oldTokens, newTokens, (e) => {
    if (e.kind === 'blockEnd') {
      flushPending()
      return
    }
    if (e.kind === 'context') {
      result.push({ oldNum: e.oldNum, newNum: e.newNum, prefix: ' ', content: renderTokenizedLine(e.content, e.oldTokens), type: 'context', hunkIndex: e.hunkIndex })
      return
    }
    if (e.kind === 'paired') {
      result.push({ oldNum: e.oldNum, newNum: null, prefix: '-', content: renderRemovedInline(e.removedContent, e.addedContent, e.oldTokens), type: 'removed', hunkIndex: e.hunkIndex })
      pendingAdded.push({ newNum: e.newNum, content: renderAddedInline(e.removedContent, e.addedContent, e.newTokens), hunkIndex: e.hunkIndex })
      return
    }
    if (e.kind === 'removed') {
      result.push({ oldNum: e.oldNum, newNum: null, prefix: '-', content: renderTokenizedLine(e.content, e.tokens), type: 'removed', hunkIndex: e.hunkIndex })
      return
    }
    // Bare '+' (without preceding '-') queues so it follows any pending '-' rows in the same block.
    pendingAdded.push({ newNum: e.newNum, content: renderTokenizedLine(e.content, e.tokens), hunkIndex: e.hunkIndex })
  })
  flushPending()
  return result
}

/** Build split diff lines from structuredPatch hunks with optional syntax tokens. */
export function buildSplitLines(
  hunks: StructuredPatchHunk[],
  oldTokens: CachedToken[][] | null,
  newTokens: CachedToken[][] | null,
): { left: SplitLineEntry[], right: SplitLineEntry[] } {
  const left: SplitLineEntry[] = []
  const right: SplitLineEntry[] = []

  walkHunks(hunks, oldTokens, newTokens, (e) => {
    if (e.kind === 'blockEnd')
      return
    if (e.kind === 'context') {
      left.push({ content: renderTokenizedLine(e.content, e.oldTokens), type: 'context', num: e.oldNum, hunkIndex: e.hunkIndex })
      right.push({ content: renderTokenizedLine(e.content, e.newTokens), type: 'context', num: e.newNum, hunkIndex: e.hunkIndex })
      return
    }
    if (e.kind === 'paired') {
      left.push({ content: renderRemovedInline(e.removedContent, e.addedContent, e.oldTokens), type: 'removed', num: e.oldNum, hunkIndex: e.hunkIndex })
      right.push({ content: renderAddedInline(e.removedContent, e.addedContent, e.newTokens), type: 'added', num: e.newNum, hunkIndex: e.hunkIndex })
      return
    }
    if (e.kind === 'removed') {
      left.push({ content: renderTokenizedLine(e.content, e.tokens), type: 'removed', num: e.oldNum, hunkIndex: e.hunkIndex })
      right.push({ content: '', type: 'empty', num: null, hunkIndex: e.hunkIndex })
      return
    }
    left.push({ content: '', type: 'empty', num: null, hunkIndex: e.hunkIndex })
    right.push({ content: renderTokenizedLine(e.content, e.tokens), type: 'added', num: e.newNum, hunkIndex: e.hunkIndex })
  })

  return { left, right }
}
