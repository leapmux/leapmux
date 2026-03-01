import type { JSX } from 'solid-js'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { CachedToken } from '~/lib/tokenCache'
import { diffLines, diffWordsWithSpace } from 'diff'
import ArrowDownFromLine from 'lucide-solid/icons/arrow-down-from-line'
import ArrowUpFromLine from 'lucide-solid/icons/arrow-up-from-line'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { guessLanguage } from '~/lib/languageMap'
import { tokenizeAsync } from '~/lib/shikiWorkerClient'
import { getCachedTokens } from '~/lib/tokenCache'
import { spinner } from '~/styles/animations.css'
import {
  diffAdded,
  diffAddedInline,
  diffContainer,
  diffContent,
  diffGapExpandButton,
  diffGapSeparator,
  diffGapSeparatorClickable,
  diffGapSeparatorFirst,
  diffGapSeparatorLast,
  diffGapSeparatorSplit,
  diffLine,
  diffLineNumber,
  diffLineNumberNew,
  diffPrefix,
  diffRemoved,
  diffRemovedInline,
  diffSplitContainer,
} from './diffStyles.css'

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

/**
 * Render a line's text using Shiki tokens when available.
 * Falls back to plain text if no tokens are provided.
 */
function renderTokenizedLine(text: string, tokens: CachedToken[] | null): JSX.Element | string {
  if (!tokens)
    return text
  return (
    <For each={tokens}>
      {token => (
        <span style={token.htmlStyle as JSX.CSSProperties}>{token.content}</span>
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
    // No syntax tokens — fall back to plain word-diff rendering
    return (
      <For each={filteredParts}>
        {p => (
          <span class={(p.added || p.removed) ? highlightClass : ''}>{p.value}</span>
        )}
      </For>
    ) as JSX.Element
  }

  // Build a flat list of fragments: each fragment has Shiki style + optional diff class
  const fragments: Array<{ text: string, style?: CachedToken['htmlStyle'], className: string }> = []

  let tokenIdx = 0
  let tokenOffset = 0 // char offset within current token

  for (const part of filteredParts) {
    const className = (part.added || part.removed) ? highlightClass : ''
    let remaining = part.value.length
    let partPos = 0

    while (remaining > 0 && tokenIdx < tokens.length) {
      const token = tokens[tokenIdx]
      const available = token.content.length - tokenOffset
      const take = Math.min(remaining, available)

      fragments.push({
        text: part.value.slice(partPos, partPos + take),
        style: token.htmlStyle,
        className,
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
        style: undefined,
        className,
      })
    }
  }

  return (
    <For each={fragments}>
      {f => (
        <span class={f.className} style={f.style as JSX.CSSProperties}>{f.text}</span>
      )}
    </For>
  ) as JSX.Element
}

/** Render inline word-level highlights for a removed line. */
function renderRemovedInline(
  oldLine: string,
  newLine: string,
  oldTokens: CachedToken[] | null,
): JSX.Element {
  // Use diffWordsWithSpace instead of diffWords so that whitespace runs
  // are preserved as separate tokens. diffWords ignores whitespace during
  // comparison and attaches it to adjacent word tokens from an arbitrary
  // side, which corrupts leading indentation when the two lines differ in
  // indentation level.
  const parts = diffWordsWithSpace(oldLine, newLine)
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
  const parts = diffWordsWithSpace(oldLine, newLine)
  return renderTokenizedWordDiff(
    parts,
    newTokens,
    diffAddedInline,
    p => !p.removed,
  )
}

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
    const rawLines = change.value.replace(/\n$/, '').split('\n')
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

/**
 * Extract old-side and new-side source text from hunks for tokenization.
 * Old side = context lines + removed lines (in order).
 * New side = context lines + added lines (in order).
 */
function extractSidesFromHunks(hunks: StructuredPatchHunk[]): { oldCode: string, newCode: string } {
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

/** A gap between hunks (or before the first / after the last hunk). */
export interface DiffGap {
  /** Lines from the original file that fill this gap. */
  lines: string[]
  /** 1-based line number of the first line in the gap. */
  startLineNumber: number
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
  const lastHunk = hunks[hunks.length - 1]
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

/** Number of context lines to reveal per expand click. */
const GAP_EXPAND_STEP = 10

/** Render a single context line from a gap with optional syntax highlighting. */
function GapContextLine(props: { lineNum: number, text: string, tokens?: CachedToken[] | null, splitView?: boolean }): JSX.Element {
  const content = () => renderTokenizedLine(props.text, props.tokens ?? null)
  return (
    <Show
      when={props.splitView}
      fallback={(
        <div class={diffLine}>
          <span class={diffLineNumber}>{props.lineNum}</span>
          <span class={diffLineNumberNew}>{props.lineNum}</span>
          <span class={diffPrefix}>{' '}</span>
          <span class={diffContent}>{content()}</span>
        </div>
      )}
    >
      <div class={diffLine}>
        <span class={diffLineNumber}>{props.lineNum}</span>
        <span class={diffPrefix}>{' '}</span>
        <span class={diffContent}>{content()}</span>
      </div>
      <div class={diffLine}>
        <span class={diffLineNumber}>{props.lineNum}</span>
        <span class={diffPrefix}>{' '}</span>
        <span class={diffContent}>{content()}</span>
      </div>
    </Show>
  )
}

/** Render a gap separator that incrementally expands context lines (up to 10 at a time). */
function DiffGapSeparator(props: {
  gap: DiffGap
  /** File path used for lazy syntax highlighting of revealed gap lines. */
  filePath?: string
  revealedTop: number
  revealedBottom: number
  onExpandUp: () => void
  onExpandDown: () => void
  onExpandAll: () => void
  /** Whether this is rendered inside a split diff grid (needs gridColumn span). */
  splitView?: boolean
  /** True when this gap is the first element in the diff container. */
  isFirst?: boolean
  /** True when this gap is the last element in the diff container. */
  isLast?: boolean
}): JSX.Element {
  const total = () => props.gap.lines.length
  const hiddenCount = () => total() - props.revealedTop - props.revealedBottom
  const topLines = () => props.gap.lines.slice(0, props.revealedTop)
  const bottomLines = () => props.revealedBottom > 0 ? props.gap.lines.slice(total() - props.revealedBottom) : []

  // Lazy tokenization: only tokenize revealed lines on demand
  const [tokenMap, setTokenMap] = createSignal<Map<number, CachedToken[]>>(new Map())
  const [tokenizing, setTokenizing] = createSignal(false)

  createEffect(on(
    () => [props.revealedTop, props.revealedBottom, props.filePath, props.gap] as const,
    ([revTop, revBottom, fp, gap]) => {
      if ((revTop === 0 && revBottom === 0) || !fp)
        return
      const lang = guessLanguage(fp)
      if (!lang)
        return

      // Collect indices of revealed lines that haven't been tokenized yet
      const currentMap = tokenMap()
      const untokenizedIndices: number[] = []
      for (let i = 0; i < revTop; i++) {
        if (!currentMap.has(i))
          untokenizedIndices.push(i)
      }
      const bottomStart = gap.lines.length - revBottom
      for (let i = bottomStart; i < gap.lines.length; i++) {
        if (!currentMap.has(i))
          untokenizedIndices.push(i)
      }

      if (untokenizedIndices.length === 0)
        return

      // Join only the untokenized lines for a single tokenization call
      const linesToTokenize = untokenizedIndices.map(i => gap.lines[i])
      const code = linesToTokenize.join('\n')

      let cancelled = false

      // Check cache synchronously first
      const cached = getCachedTokens(lang, code)
      if (cached) {
        setTokenMap((prev) => {
          const next = new Map(prev)
          for (let j = 0; j < untokenizedIndices.length; j++)
            next.set(untokenizedIndices[j], cached[j])
          return next
        })
        return
      }

      setTokenizing(true)
      tokenizeAsync(lang, code).then((tokens) => {
        if (cancelled)
          return
        setTokenMap((prev) => {
          const next = new Map(prev)
          for (let j = 0; j < untokenizedIndices.length; j++)
            next.set(untokenizedIndices[j], tokens[j])
          return next
        })
        setTokenizing(false)
      })

      onCleanup(() => {
        cancelled = true
      })
    },
  ))

  /** Get tokens for a gap line by its index within the gap (0-based). */
  const tokensForGapLine = (gapIdx: number) => tokenMap().get(gapIdx) ?? null
  const separatorClass = () => {
    let cls = diffGapSeparator
    if (props.splitView)
      cls += ` ${diffGapSeparatorSplit}`
    if (props.isFirst && props.revealedTop === 0)
      cls += ` ${diffGapSeparatorFirst}`
    if (props.isLast && props.revealedBottom === 0)
      cls += ` ${diffGapSeparatorLast}`
    return cls
  }
  const hiddenLabel = () => `${hiddenCount()} line${hiddenCount() === 1 ? '' : 's'} hidden`

  return (
    <>
      <For each={topLines()}>
        {(line, idx) => (
          <GapContextLine
            lineNum={props.gap.startLineNumber + idx()}
            text={line}
            tokens={tokensForGapLine(idx())}
            splitView={props.splitView}
          />
        )}
      </For>
      <Show when={hiddenCount() > 0}>
        <Show
          when={hiddenCount() > GAP_EXPAND_STEP}
          fallback={(
            <div class={`${separatorClass()} ${diffGapSeparatorClickable}`} onClick={() => props.onExpandAll()}>
              {hiddenLabel()}
            </div>
          )}
        >
          <div class={separatorClass()}>
            <span class={diffGapExpandButton} onClick={() => props.onExpandUp()}>
              <ArrowUpFromLine size={12} />
              Expand up
            </span>
            <span>
              {hiddenLabel()}
              <Show when={tokenizing()}>
                {' '}
                <Icon icon={LoaderCircle} size="xs" class={spinner} />
              </Show>
            </span>
            <span class={diffGapExpandButton} onClick={() => props.onExpandDown()}>
              <ArrowDownFromLine size={12} />
              Expand down
            </span>
          </div>
        </Show>
      </Show>
      <For each={bottomLines()}>
        {(line, idx) => (
          <GapContextLine
            lineNum={props.gap.startLineNumber + total() - props.revealedBottom + idx()}
            text={line}
            tokens={tokensForGapLine(total() - props.revealedBottom + idx())}
            splitView={props.splitView}
          />
        )}
      </For>
    </>
  )
}

/** Line limit for diff syntax highlighting. */
const HIGHLIGHT_LINE_LIMIT = 1000

/** Build unified diff lines from structuredPatch hunks with optional syntax tokens. */
function buildUnifiedLines(
  hunks: StructuredPatchHunk[],
  oldTokens: CachedToken[][] | null,
  newTokens: CachedToken[][] | null,
): DiffLineEntry[] {
  const result: DiffLineEntry[] = []
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

      if (prefix === '-') {
        // Collect consecutive removed lines, then consecutive added lines for pairing
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
        for (let j = 0; j < paired; j++)
          result.push({ oldNum: oldLine++, newNum: null, prefix: '-', content: renderRemovedInline(removedLines[j], addedLines[j], oldTokens?.[removedTokenIndices[j]] ?? null), type: 'removed', hunkIndex: hi })
        for (let j = paired; j < removedLines.length; j++)
          result.push({ oldNum: oldLine++, newNum: null, prefix: '-', content: renderTokenizedLine(removedLines[j], oldTokens?.[removedTokenIndices[j]] ?? null), type: 'removed', hunkIndex: hi })
        for (let j = 0; j < paired; j++)
          result.push({ oldNum: null, newNum: newLine++, prefix: '+', content: renderAddedInline(removedLines[j], addedLines[j], newTokens?.[addedTokenIndices[j]] ?? null), type: 'added', hunkIndex: hi })
        for (let j = paired; j < addedLines.length; j++)
          result.push({ oldNum: null, newNum: newLine++, prefix: '+', content: renderTokenizedLine(addedLines[j], newTokens?.[addedTokenIndices[j]] ?? null), type: 'added', hunkIndex: hi })
      }
      else if (prefix === '+') {
        result.push({ oldNum: null, newNum: newLine++, prefix: '+', content: renderTokenizedLine(lines[i].slice(1), newTokens?.[newTokenLine] ?? null), type: 'added', hunkIndex: hi })
        newTokenLine++
        i++
      }
      else {
        result.push({ oldNum: oldLine++, newNum: newLine++, prefix: ' ', content: renderTokenizedLine(lines[i].slice(1), oldTokens?.[oldTokenLine] ?? null), type: 'context', hunkIndex: hi })
        oldTokenLine++
        newTokenLine++
        i++
      }
    }
  }
  return result
}

/** Build split diff lines from structuredPatch hunks with optional syntax tokens. */
function buildSplitLines(
  hunks: StructuredPatchHunk[],
  oldTokens: CachedToken[][] | null,
  newTokens: CachedToken[][] | null,
): { left: SplitLineEntry[], right: SplitLineEntry[] } {
  const left: SplitLineEntry[] = []
  const right: SplitLineEntry[] = []
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

      if (prefix === '-') {
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
          left.push({ content: renderRemovedInline(removedLines[j], addedLines[j], oldTokens?.[removedTokenIndices[j]] ?? null), type: 'removed', num: oldLine++, hunkIndex: hi })
          right.push({ content: renderAddedInline(removedLines[j], addedLines[j], newTokens?.[addedTokenIndices[j]] ?? null), type: 'added', num: newLine++, hunkIndex: hi })
        }
        for (let j = paired; j < removedLines.length; j++) {
          left.push({ content: renderTokenizedLine(removedLines[j], oldTokens?.[removedTokenIndices[j]] ?? null), type: 'removed', num: oldLine++, hunkIndex: hi })
          right.push({ content: '', type: 'empty', num: null, hunkIndex: hi })
        }
        for (let j = paired; j < addedLines.length; j++) {
          left.push({ content: '', type: 'empty', num: null, hunkIndex: hi })
          right.push({ content: renderTokenizedLine(addedLines[j], newTokens?.[addedTokenIndices[j]] ?? null), type: 'added', num: newLine++, hunkIndex: hi })
        }
      }
      else if (prefix === '+') {
        left.push({ content: '', type: 'empty', num: null, hunkIndex: hi })
        right.push({ content: renderTokenizedLine(lines[i].slice(1), newTokens?.[newTokenLine] ?? null), type: 'added', num: newLine++, hunkIndex: hi })
        newTokenLine++
        i++
      }
      else {
        const text = lines[i].slice(1)
        left.push({ content: renderTokenizedLine(text, oldTokens?.[oldTokenLine] ?? null), type: 'context', num: oldLine++, hunkIndex: hi })
        right.push({ content: renderTokenizedLine(text, newTokens?.[newTokenLine] ?? null), type: 'context', num: newLine++, hunkIndex: hi })
        oldTokenLine++
        newTokenLine++
        i++
      }
    }
  }
  return { left, right }
}

/** Count the total number of lines across all hunks. */
function countHunkLines(hunks: StructuredPatchHunk[]): number {
  let count = 0
  for (const hunk of hunks)
    count += hunk.lines.length
  return count
}

/** Hook to asynchronously tokenize old and new sides of a diff. */
function useDiffTokens(
  hunks: () => StructuredPatchHunk[],
  filePath: () => string | undefined,
): {
  oldTokens: () => CachedToken[][] | null
  newTokens: () => CachedToken[][] | null
} {
  const [oldTokens, setOldTokens] = createSignal<CachedToken[][] | null>(null)
  const [newTokens, setNewTokens] = createSignal<CachedToken[][] | null>(null)

  createEffect(on(
    () => [hunks(), filePath()] as const,
    ([h, fp]) => {
      setOldTokens(null)
      setNewTokens(null)

      if (!fp)
        return
      const lang = guessLanguage(fp)
      if (!lang)
        return
      if (countHunkLines(h) > HIGHLIGHT_LINE_LIMIT)
        return

      const { oldCode, newCode } = extractSidesFromHunks(h)
      let cancelled = false

      // Check cache synchronously for both sides
      const cachedOld = getCachedTokens(lang, oldCode)
      const cachedNew = getCachedTokens(lang, newCode)
      if (cachedOld)
        setOldTokens(cachedOld)
      if (cachedNew)
        setNewTokens(cachedNew)

      // Fetch any uncached sides from worker
      if (!cachedOld) {
        tokenizeAsync(lang, oldCode).then((tokens) => {
          if (!cancelled)
            setOldTokens(tokens)
        })
      }
      if (!cachedNew) {
        tokenizeAsync(lang, newCode).then((tokens) => {
          if (!cancelled)
            setNewTokens(tokens)
        })
      }

      onCleanup(() => {
        cancelled = true
      })
    },
  ))

  return { oldTokens, newTokens }
}

/** Render a unified diff line. */
function UnifiedDiffLine(props: { line: DiffLineEntry }): JSX.Element {
  return (
    <div class={`${diffLine} ${props.line.type === 'added' ? diffAdded : props.line.type === 'removed' ? diffRemoved : ''}`}>
      <span class={diffLineNumber}>{props.line.oldNum ?? ''}</span>
      <span class={diffLineNumberNew}>{props.line.newNum ?? ''}</span>
      <span class={diffPrefix}>{props.line.prefix}</span>
      <span class={diffContent}>{props.line.content}</span>
    </div>
  )
}

/** Render a unified diff view from hunks. */
function UnifiedDiffView(props: { hunks: StructuredPatchHunk[], filePath?: string, originalFile?: string }): JSX.Element {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath)
  const lines = () => buildUnifiedLines(props.hunks, oldTokens(), newTokens())

  const originalFileLines = () => props.originalFile?.split('\n')
  const gapData = () => {
    const ofl = originalFileLines()
    if (!ofl)
      return null
    return computeGapMap(props.hunks, ofl)
  }

  const [gapReveals, setGapReveals] = createSignal<Map<string, { top: number, bottom: number }>>(new Map())
  const getReveal = (key: string) => gapReveals().get(key) ?? { top: 0, bottom: 0 }
  const expandDown = (key: string, total: number) => {
    setGapReveals((prev) => {
      const next = new Map(prev)
      const cur = next.get(key) ?? { top: 0, bottom: 0 }
      const maxMore = total - cur.top - cur.bottom
      next.set(key, { ...cur, top: cur.top + Math.min(GAP_EXPAND_STEP, maxMore) })
      return next
    })
  }
  const expandUp = (key: string, total: number) => {
    setGapReveals((prev) => {
      const next = new Map(prev)
      const cur = next.get(key) ?? { top: 0, bottom: 0 }
      const maxMore = total - cur.top - cur.bottom
      next.set(key, { ...cur, bottom: cur.bottom + Math.min(GAP_EXPAND_STEP, maxMore) })
      return next
    })
  }
  const expandAll = (key: string, total: number) => {
    setGapReveals((prev) => {
      const next = new Map(prev)
      next.set(key, { top: total, bottom: 0 })
      return next
    })
  }

  return (
    <div class={diffContainer}>
      <Show
        when={gapData()}
        fallback={(
          <For each={lines()}>
            {line => <UnifiedDiffLine line={line} />}
          </For>
        )}
      >
        {(gd) => {
          const groups = () => groupByHunk(lines())
          return (
            <For each={groups()}>
              {(group, groupIdx) => {
                const hi = () => group[0]?.hunkIndex ?? groupIdx()
                const gapBefore = () => gd().gaps.get(hi())
                const isTrailing = () => groupIdx() === groups().length - 1 ? gd().trailing : null
                return (
                  <>
                    <Show when={gapBefore()}>
                      {gap => (
                        <DiffGapSeparator
                          gap={gap()}
                          filePath={props.filePath}
                          revealedTop={getReveal(`before-${hi()}`).top}
                          revealedBottom={getReveal(`before-${hi()}`).bottom}
                          onExpandDown={() => expandDown(`before-${hi()}`, gap().lines.length)}
                          onExpandUp={() => expandUp(`before-${hi()}`, gap().lines.length)}
                          onExpandAll={() => expandAll(`before-${hi()}`, gap().lines.length)}
                          isFirst={groupIdx() === 0}
                        />
                      )}
                    </Show>
                    <For each={group}>
                      {line => <UnifiedDiffLine line={line} />}
                    </For>
                    <Show when={isTrailing()}>
                      {trailing => (
                        <DiffGapSeparator
                          gap={trailing()}
                          filePath={props.filePath}
                          revealedTop={getReveal('trailing').top}
                          revealedBottom={getReveal('trailing').bottom}
                          onExpandDown={() => expandDown('trailing', trailing().lines.length)}
                          onExpandUp={() => expandUp('trailing', trailing().lines.length)}
                          onExpandAll={() => expandAll('trailing', trailing().lines.length)}
                          isLast
                        />
                      )}
                    </Show>
                  </>
                )
              }}
            </For>
          )
        }}
      </Show>
    </div>
  )
}

/** Render a single split diff row (left + right lines rendered in the grid). */
function SplitDiffRow(props: { left: SplitLineEntry, right: SplitLineEntry }): JSX.Element {
  return (
    <>
      <div class={`${diffLine} ${props.left.type === 'removed' ? diffRemoved : ''}`}>
        <span class={diffLineNumber}>{props.left.num ?? ''}</span>
        <span class={diffPrefix}>{props.left.type === 'removed' ? '-' : ' '}</span>
        <span class={diffContent}>{props.left.content}</span>
      </div>
      <div class={`${diffLine} ${props.right.type === 'added' ? diffAdded : ''}`}>
        <span class={diffLineNumber}>{props.right.num ?? ''}</span>
        <span class={diffPrefix}>{props.right.type === 'added' ? '+' : ' '}</span>
        <span class={diffContent}>{props.right.content}</span>
      </div>
    </>
  )
}

/** Render a split diff view from hunks (removed on left, added on right). */
function SplitDiffView(props: { hunks: StructuredPatchHunk[], filePath?: string, originalFile?: string }): JSX.Element {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath)
  const splitLines = () => buildSplitLines(props.hunks, oldTokens(), newTokens())

  const originalFileLines = () => props.originalFile?.split('\n')
  const gapData = () => {
    const ofl = originalFileLines()
    if (!ofl)
      return null
    return computeGapMap(props.hunks, ofl)
  }

  const [gapReveals, setGapReveals] = createSignal<Map<string, { top: number, bottom: number }>>(new Map())
  const getReveal = (key: string) => gapReveals().get(key) ?? { top: 0, bottom: 0 }
  const expandDown = (key: string, total: number) => {
    setGapReveals((prev) => {
      const next = new Map(prev)
      const cur = next.get(key) ?? { top: 0, bottom: 0 }
      const maxMore = total - cur.top - cur.bottom
      next.set(key, { ...cur, top: cur.top + Math.min(GAP_EXPAND_STEP, maxMore) })
      return next
    })
  }
  const expandUp = (key: string, total: number) => {
    setGapReveals((prev) => {
      const next = new Map(prev)
      const cur = next.get(key) ?? { top: 0, bottom: 0 }
      const maxMore = total - cur.top - cur.bottom
      next.set(key, { ...cur, bottom: cur.bottom + Math.min(GAP_EXPAND_STEP, maxMore) })
      return next
    })
  }
  const expandAll = (key: string, total: number) => {
    setGapReveals((prev) => {
      const next = new Map(prev)
      next.set(key, { top: total, bottom: 0 })
      return next
    })
  }

  return (
    <div class={diffSplitContainer}>
      <Show
        when={gapData()}
        fallback={(
          <For each={splitLines().left}>
            {(leftLine, i) => {
              const rightLine = () => splitLines().right[i()]
              return <SplitDiffRow left={leftLine} right={rightLine()} />
            }}
          </For>
        )}
      >
        {(gd) => {
          const leftGroups = () => groupByHunk(splitLines().left)
          const rightGroups = () => groupByHunk(splitLines().right)
          return (
            <For each={leftGroups()}>
              {(leftGroup, groupIdx) => {
                const hi = () => leftGroup[0]?.hunkIndex ?? groupIdx()
                const rightGroup = () => rightGroups()[groupIdx()] ?? []
                const gapBefore = () => gd().gaps.get(hi())
                const isTrailing = () => groupIdx() === leftGroups().length - 1 ? gd().trailing : null
                return (
                  <>
                    <Show when={gapBefore()}>
                      {gap => (
                        <DiffGapSeparator
                          gap={gap()}
                          filePath={props.filePath}
                          revealedTop={getReveal(`before-${hi()}`).top}
                          revealedBottom={getReveal(`before-${hi()}`).bottom}
                          onExpandDown={() => expandDown(`before-${hi()}`, gap().lines.length)}
                          onExpandUp={() => expandUp(`before-${hi()}`, gap().lines.length)}
                          onExpandAll={() => expandAll(`before-${hi()}`, gap().lines.length)}
                          splitView
                          isFirst={groupIdx() === 0}
                        />
                      )}
                    </Show>
                    <For each={leftGroup}>
                      {(leftLine, i) => {
                        const rightLine = () => rightGroup()[i()] ?? { content: '', type: 'empty' as const, num: null, hunkIndex: hi() }
                        return <SplitDiffRow left={leftLine} right={rightLine()} />
                      }}
                    </For>
                    <Show when={isTrailing()}>
                      {trailing => (
                        <DiffGapSeparator
                          gap={trailing()}
                          filePath={props.filePath}
                          revealedTop={getReveal('trailing').top}
                          revealedBottom={getReveal('trailing').bottom}
                          onExpandDown={() => expandDown('trailing', trailing().lines.length)}
                          onExpandUp={() => expandUp('trailing', trailing().lines.length)}
                          onExpandAll={() => expandAll('trailing', trailing().lines.length)}
                          splitView
                          isLast
                        />
                      )}
                    </Show>
                  </>
                )
              }}
            </For>
          )
        }}
      </Show>
    </div>
  )
}

/** Renders a diff view (unified or split) from hunks with optional syntax highlighting. */
export function DiffView(props: { hunks: StructuredPatchHunk[], view: DiffViewPreference, filePath?: string, originalFile?: string }): JSX.Element {
  return (
    <Show when={props.view === 'unified'} fallback={<SplitDiffView hunks={props.hunks} filePath={props.filePath} originalFile={props.originalFile} />}>
      <UnifiedDiffView hunks={props.hunks} filePath={props.filePath} originalFile={props.originalFile} />
    </Show>
  )
}
