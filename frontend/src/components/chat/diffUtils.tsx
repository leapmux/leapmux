import type { JSX } from 'solid-js'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { CachedToken } from '~/lib/tokenCache'
import { diffLines, diffWordsWithSpace } from 'diff'
import { createEffect, createSignal, For, on, onCleanup, Show } from 'solid-js'
import { guessLanguage } from '~/lib/languageMap'
import { tokenizeAsync } from '~/lib/shikiWorkerClient'
import { getCachedTokens } from '~/lib/tokenCache'
import {
  diffAdded,
  diffAddedInline,
  diffContainer,
  diffContent,
  diffLine,
  diffLineNumber,
  diffLineNumberNew,
  diffPrefix,
  diffRemoved,
  diffRemovedInline,
  diffSplitColumn,
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
interface DiffLineEntry {
  oldNum: number | null
  newNum: number | null
  prefix: string
  content: JSX.Element | string
  type: 'added' | 'removed' | 'context'
}

/** A single split diff line with optional JSX content. */
interface SplitLineEntry {
  content: JSX.Element | string
  type: 'removed' | 'added' | 'context' | 'empty'
  num: number | null
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

  for (const hunk of hunks) {
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
          result.push({ oldNum: oldLine++, newNum: null, prefix: '-', content: renderRemovedInline(removedLines[j], addedLines[j], oldTokens?.[removedTokenIndices[j]] ?? null), type: 'removed' })
        for (let j = paired; j < removedLines.length; j++)
          result.push({ oldNum: oldLine++, newNum: null, prefix: '-', content: renderTokenizedLine(removedLines[j], oldTokens?.[removedTokenIndices[j]] ?? null), type: 'removed' })
        for (let j = 0; j < paired; j++)
          result.push({ oldNum: null, newNum: newLine++, prefix: '+', content: renderAddedInline(removedLines[j], addedLines[j], newTokens?.[addedTokenIndices[j]] ?? null), type: 'added' })
        for (let j = paired; j < addedLines.length; j++)
          result.push({ oldNum: null, newNum: newLine++, prefix: '+', content: renderTokenizedLine(addedLines[j], newTokens?.[addedTokenIndices[j]] ?? null), type: 'added' })
      }
      else if (prefix === '+') {
        result.push({ oldNum: null, newNum: newLine++, prefix: '+', content: renderTokenizedLine(lines[i].slice(1), newTokens?.[newTokenLine] ?? null), type: 'added' })
        newTokenLine++
        i++
      }
      else {
        result.push({ oldNum: oldLine++, newNum: newLine++, prefix: ' ', content: renderTokenizedLine(lines[i].slice(1), oldTokens?.[oldTokenLine] ?? null), type: 'context' })
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

  for (const hunk of hunks) {
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
          left.push({ content: renderRemovedInline(removedLines[j], addedLines[j], oldTokens?.[removedTokenIndices[j]] ?? null), type: 'removed', num: oldLine++ })
          right.push({ content: renderAddedInline(removedLines[j], addedLines[j], newTokens?.[addedTokenIndices[j]] ?? null), type: 'added', num: newLine++ })
        }
        for (let j = paired; j < removedLines.length; j++) {
          left.push({ content: renderTokenizedLine(removedLines[j], oldTokens?.[removedTokenIndices[j]] ?? null), type: 'removed', num: oldLine++ })
          right.push({ content: '', type: 'empty', num: null })
        }
        for (let j = paired; j < addedLines.length; j++) {
          left.push({ content: '', type: 'empty', num: null })
          right.push({ content: renderTokenizedLine(addedLines[j], newTokens?.[addedTokenIndices[j]] ?? null), type: 'added', num: newLine++ })
        }
      }
      else if (prefix === '+') {
        left.push({ content: '', type: 'empty', num: null })
        right.push({ content: renderTokenizedLine(lines[i].slice(1), newTokens?.[newTokenLine] ?? null), type: 'added', num: newLine++ })
        newTokenLine++
        i++
      }
      else {
        const text = lines[i].slice(1)
        left.push({ content: renderTokenizedLine(text, oldTokens?.[oldTokenLine] ?? null), type: 'context', num: oldLine++ })
        right.push({ content: renderTokenizedLine(text, newTokens?.[newTokenLine] ?? null), type: 'context', num: newLine++ })
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

/** Render a unified diff view from hunks. */
function UnifiedDiffView(props: { hunks: StructuredPatchHunk[], filePath?: string }): JSX.Element {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath)
  const lines = () => buildUnifiedLines(props.hunks, oldTokens(), newTokens())
  return (
    <div class={diffContainer}>
      <For each={lines()}>
        {line => (
          <div class={`${diffLine} ${line.type === 'added' ? diffAdded : line.type === 'removed' ? diffRemoved : ''}`}>
            <span class={diffLineNumber}>{line.oldNum ?? ''}</span>
            <span class={diffLineNumberNew}>{line.newNum ?? ''}</span>
            <span class={diffPrefix}>{line.prefix}</span>
            <span class={diffContent}>{line.content}</span>
          </div>
        )}
      </For>
    </div>
  )
}

/** Render a split diff view from hunks (removed on left, added on right). */
function SplitDiffView(props: { hunks: StructuredPatchHunk[], filePath?: string }): JSX.Element {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath)
  const splitLines = () => buildSplitLines(props.hunks, oldTokens(), newTokens())
  return (
    <div class={diffSplitContainer}>
      <div class={diffSplitColumn}>
        <For each={splitLines().left}>
          {line => (
            <div class={`${diffLine} ${line.type === 'removed' ? diffRemoved : ''}`}>
              <span class={diffLineNumber}>{line.num ?? ''}</span>
              <span class={diffPrefix}>{line.type === 'removed' ? '-' : ' '}</span>
              <span class={diffContent}>{line.content}</span>
            </div>
          )}
        </For>
      </div>
      <div class={diffSplitColumn}>
        <For each={splitLines().right}>
          {line => (
            <div class={`${diffLine} ${line.type === 'added' ? diffAdded : ''}`}>
              <span class={diffLineNumber}>{line.num ?? ''}</span>
              <span class={diffPrefix}>{line.type === 'added' ? '+' : ' '}</span>
              <span class={diffContent}>{line.content}</span>
            </div>
          )}
        </For>
      </div>
    </div>
  )
}

/** Renders a diff view (unified or split) from hunks with optional syntax highlighting. */
export function DiffView(props: { hunks: StructuredPatchHunk[], view: DiffViewPreference, filePath?: string }): JSX.Element {
  return (
    <Show when={props.view === 'unified'} fallback={<SplitDiffView hunks={props.hunks} filePath={props.filePath} />}>
      <UnifiedDiffView hunks={props.hunks} filePath={props.filePath} />
    </Show>
  )
}
