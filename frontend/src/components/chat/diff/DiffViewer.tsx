import type { JSX } from 'solid-js'
import type { RenderContext } from '../messageRenderers'
import type { TokenGate } from '../useAsyncCodeTokens'
import type { DiffGap, DiffGapSummary, DiffLineEntry, SplitLineEntry, StructuredPatchHunk } from './diffTypes'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { CachedToken } from '~/lib/tokenCache'
import ArrowDownFromLine from 'lucide-solid/icons/arrow-down-from-line'
import ArrowUpFromLine from 'lucide-solid/icons/arrow-up-from-line'
import { createMemo, createSignal, For, Show } from 'solid-js'
import { ansiSyncTokenize } from '~/lib/ansiTokenize'
import { guessLanguage } from '~/lib/languageMap'
import { pluralize } from '~/lib/plural'
import { shouldPauseSyntaxHighlighting } from '../messageRenderers'
import { HIGHLIGHT_LINE_LIMIT } from '../results/collapse'
import { useAsyncCodeTokens } from '../useAsyncCodeTokens'
import { computeGapMap, computeSyntheticGapMap, countHunkLines, extractSidesFromHunks, groupByHunk } from './diffBuilder'
import {
  diffAdded,
  diffContainer,
  diffContent,
  diffGapExpandButton,
  diffGapSeparator,
  diffGapSeparatorClickable,
  diffGapSeparatorFirst,
  diffGapSeparatorLast,
  diffGapSeparatorSplit,
  diffHideLineNumbers,
  diffLine,
  diffLineNumber,
  diffLineNumberNew,
  diffPrefix,
  diffRemoved,
  diffSplitContainer,
} from './diffStyles.css'
import { buildSplitLines, buildUnifiedLines, renderTokenizedLine } from './diffTokenRender'

/** Number of context lines to reveal per expand click. */
const GAP_EXPAND_STEP = 10

/** Render a single context line from a gap with optional syntax highlighting. */
function GapContextLine(props: { lineNum: number, text: string, tokens?: CachedToken[] | null, splitView?: boolean }): JSX.Element {
  const content = () => renderTokenizedLine(props.text, props.tokens ?? null)
  // One single-gutter context row. Split view renders it in BOTH the left and right
  // columns (the two gutters are identical for an unchanged context line), so it lives
  // here once rather than as two byte-identical inline blocks that could drift.
  const gutterRow = () => (
    <div class={diffLine}>
      <span class={diffLineNumber}>{props.lineNum}</span>
      <span class={diffPrefix}>{' '}</span>
      <span class={diffContent}>{content()}</span>
    </div>
  )
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
      {gutterRow()}
      {gutterRow()}
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
  context?: RenderContext
}): JSX.Element {
  const total = () => props.gap.lines.length
  const hiddenCount = () => total() - props.revealedTop - props.revealedBottom
  // Single memoized source for the revealed top/bottom slices. The render loops
  // (topLines/bottomLines), the eligibility check, and the tokenized `gapCode` all read
  // from here, so the rendered rows and the token array `tokensForGapLine` indexes by
  // position (top slice first, then bottom slice) can't drift apart.
  const revealedSlices = createMemo(() => ({
    top: props.gap.lines.slice(0, props.revealedTop),
    bottom: props.revealedBottom > 0 ? props.gap.lines.slice(total() - props.revealedBottom) : [],
  }))
  const topLines = () => revealedSlices().top
  const bottomLines = () => revealedSlices().bottom

  // Tokenize the REVEALED gap lines (top slice + bottom slice) through the shared
  // useAsyncCodeTokens state machine -- the same cache / worker-dispatch / hold-defer /
  // synchronous-seed path every other diff and tool code surface uses, rather than a
  // hand-rolled copy. Each expand changes the revealed set (and thus the joined code), so
  // the hook re-tokenizes the current set (cached per state) and the synchronous seed
  // paints already-cached lines on the first frame instead of flashing plain.
  const revealedGapLines = createMemo(() => [...revealedSlices().top, ...revealedSlices().bottom])
  const gapLang = createMemo(() => props.filePath ? guessLanguage(props.filePath) : undefined)
  // Memoized (like the other consumers' `code`) so the O(revealed) join isn't rebuilt on
  // each of the hook's per-pass reads (both effects' currentKey() + the seed).
  const gapCode = createMemo(() => revealedGapLines().join('\n'))
  const gapTokens = useAsyncCodeTokens({
    lang: gapLang,
    code: gapCode,
    // Any revealed line is eligible; the reveal count is user-bounded (10 per expand, or
    // the whole gap on "expand all"), matching the prior path which tokenized whatever was
    // revealed without a separate line cap.
    eligible: () => revealedGapLines().length > 0,
    // Treat premeasure / scroll-pause / active-selection alike (hold), mirroring
    // useDiffTokens: keep applied tokens steady and defer newly-computed ones.
    gate: () => ({ premeasure: false, hold: shouldPauseSyntaxHighlighting(props.context) }),
    // `ansi` (a `.log` file) has no worker grammar -- tokenize it on the main thread so a
    // `.log` diff's gap-context lines highlight like the rest of the file.
    syncTokenize: ansiSyncTokenize,
  })

  // The hook indexes tokens by POSITION within revealedGapLines (top slice, then bottom
  // slice). Map a gap-line index back to that position; lines outside the revealed slices
  // have no tokens.
  const tokensForGapLine = (gapIdx: number): CachedToken[] | null => {
    const tokens = gapTokens()
    if (!tokens)
      return null
    if (gapIdx < props.revealedTop)
      return tokens[gapIdx] ?? null
    const bottomStart = total() - props.revealedBottom
    if (gapIdx >= bottomStart)
      return tokens[props.revealedTop + (gapIdx - bottomStart)] ?? null
    return null
  }
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
  const hiddenLabel = () => `${pluralize(hiddenCount(), 'line')} hidden`

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
            <span class={diffGapExpandButton} onClick={() => props.onExpandAll()}>
              {hiddenLabel()}
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

/** Render a non-interactive gap separator when only the hidden line count is known. */
function DiffGapSummarySeparator(props: {
  gap: DiffGapSummary
  splitView?: boolean
  isFirst?: boolean
  isLast?: boolean
}): JSX.Element {
  const separatorClass = () => {
    let cls = diffGapSeparator
    if (props.splitView)
      cls += ` ${diffGapSeparatorSplit}`
    if (props.isFirst)
      cls += ` ${diffGapSeparatorFirst}`
    if (props.isLast)
      cls += ` ${diffGapSeparatorLast}`
    return cls
  }
  const hiddenLabel = () => `${pluralize(props.gap.lineCount, 'line')} hidden`

  return (
    <div class={separatorClass()}>
      {hiddenLabel()}
    </div>
  )
}

/**
 * Hook to asynchronously tokenize the old and new sides of a diff via the shared
 * {@link useAsyncCodeTokens} state machine -- one instance per side. Each side gets
 * the same cache-check -> worker-dispatch -> cancel -> hold-deferral machinery the
 * chat code surfaces use, instead of a hand-rolled copy.
 *
 * The diff viewer treats premeasure / scroll-pause / active-selection alike: keep
 * the already-applied tokens steady and defer any newly-computed ones (the `hold`
 * gate), rather than the hard `premeasure` skip (which drops applied tokens on a
 * hidden remeasure). The line-count cap is a coarse perf guard keyed off the whole
 * hunk set; an oversized diff renders plain.
 */
function useDiffTokens(
  hunks: () => StructuredPatchHunk[],
  filePath: () => string | undefined,
  context: () => RenderContext | undefined,
): {
  oldTokens: () => CachedToken[][] | null
  newTokens: () => CachedToken[][] | null
} {
  // Memoized: each is read via currentKey() from both effects of BOTH per-side hooks,
  // so a plain accessor would re-run guessLanguage / the O(hunks) countHunkLines scan
  // several times per reactive pass.
  const lang = createMemo((): string | undefined => {
    const fp = filePath()
    return fp ? guessLanguage(fp) : undefined
  })
  // Extract both sides once per hunk change; shared by the two per-side hooks.
  const sides = createMemo(() => extractSidesFromHunks(hunks()))
  const eligible = createMemo((): boolean => countHunkLines(hunks()) <= HIGHLIGHT_LINE_LIMIT)
  const gate = (): TokenGate => ({ premeasure: false, hold: shouldPauseSyntaxHighlighting(context()) })

  // syncTokenize handles `ansi` (a `.log` file's language) on the main thread -- the
  // worker's Oniguruma core has no `ansi` grammar, so without this a `.log` diff would
  // degrade to plain. Same tokenizer the Read view uses.
  const oldTokens = useAsyncCodeTokens({ lang, code: () => sides().oldCode, eligible, gate, syncTokenize: ansiSyncTokenize })
  const newTokens = useAsyncCodeTokens({ lang, code: () => sides().newCode, eligible, gate, syncTokenize: ansiSyncTokenize })
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

interface GapState {
  getReveal: (key: string) => { top: number, bottom: number }
  expandDown: (key: string, total: number) => void
  expandUp: (key: string, total: number) => void
  expandAll: (key: string, total: number) => void
}

function useGapReveals(): GapState {
  const [gapReveals, setGapReveals] = createSignal<Map<string, { top: number, bottom: number }>>(new Map())
  const getReveal = (key: string) => gapReveals().get(key) ?? { top: 0, bottom: 0 }
  const mutate = (key: string, fn: (cur: { top: number, bottom: number }) => { top: number, bottom: number }) => {
    setGapReveals((prev) => {
      const next = new Map(prev)
      next.set(key, fn(next.get(key) ?? { top: 0, bottom: 0 }))
      return next
    })
  }
  return {
    getReveal,
    expandDown: (key, total) => mutate(key, (cur) => {
      const maxMore = total - cur.top - cur.bottom
      return { ...cur, top: cur.top + Math.min(GAP_EXPAND_STEP, maxMore) }
    }),
    expandUp: (key, total) => mutate(key, (cur) => {
      const maxMore = total - cur.top - cur.bottom
      return { ...cur, bottom: cur.bottom + Math.min(GAP_EXPAND_STEP, maxMore) }
    }),
    expandAll: (key, total) => mutate(key, () => ({ top: total, bottom: 0 })),
  }
}

function useGapData(getHunks: () => StructuredPatchHunk[], getOriginalFile: () => string | undefined) {
  const originalFileLines = createMemo(() => {
    const source = getOriginalFile()
    if (!source)
      return undefined
    const lines = source.split('\n')
    if (lines.length > 0 && lines.at(-1) === '')
      lines.pop()
    return lines
  })
  const gapData = createMemo(() => {
    const ofl = originalFileLines()
    if (!ofl)
      return null
    return computeGapMap(getHunks(), ofl)
  })
  const syntheticGaps = createMemo(() => computeSyntheticGapMap(getHunks()))
  return { gapData, syntheticGaps }
}

function diffContainerClass(base: string, showLineNumbers?: boolean): string {
  return showLineNumbers === false ? `${base} ${diffHideLineNumbers}` : base
}

/** Gap data for a diff with a known original file: real per-hunk gaps + a trailing gap. */
interface DiffGapData {
  gaps: Map<number, DiffGap>
  trailing: DiffGap | null
}

/**
 * Shared gap+group scaffold for BOTH diff views. Owns the container, the
 * synthetic-vs-real gap branch, and each group's gap-before + trailing separators --
 * the reveal-key wiring (`before-${hi}` / `trailing`), the first/last flags, and the
 * expand handlers that were previously written out twice (unified and split), four
 * near-identical `DiffGapSeparator` prop bags in all. The two views differ ONLY in the
 * container class, the `splitView` flag, the group element type, and how each group's
 * lines render, so those are the props; the per-group line rendering is delegated to
 * `renderGroup`. `hunkIndex` on the first line of a group indexes into the gap maps
 * (falling back to the positional group index for an empty leading group).
 */
function DiffGapScaffold<E extends { hunkIndex: number }>(props: {
  containerClass: string
  splitView?: boolean
  groups: () => E[][]
  gapData: () => DiffGapData | null
  syntheticGaps: () => Map<number, DiffGapSummary>
  gapState: GapState
  filePath?: string
  context?: RenderContext
  renderGroup: (group: E[], groupIdx: () => number) => JSX.Element
}): JSX.Element {
  const hunkIndexOf = (group: E[], groupIdx: number): number => group[0]?.hunkIndex ?? groupIdx
  // One interactive separator for a reveal key (`before-${hi}` or `trailing`). Every input is
  // an accessor read reactively in the JSX: `<Show>` keeps its child mounted across a
  // truthy->truthy change (a new gap object when originalFile changes, or a shifted groupIdx),
  // so a snapshot would freeze the separator on the stale gap/key -- it must re-read them.
  const revealSeparator = (opts: {
    key: () => string
    gap: () => DiffGap
    isFirst?: () => boolean
    isLast?: boolean
  }): JSX.Element => (
    <DiffGapSeparator
      gap={opts.gap()}
      filePath={props.filePath}
      revealedTop={props.gapState.getReveal(opts.key()).top}
      revealedBottom={props.gapState.getReveal(opts.key()).bottom}
      onExpandDown={() => props.gapState.expandDown(opts.key(), opts.gap().lines.length)}
      onExpandUp={() => props.gapState.expandUp(opts.key(), opts.gap().lines.length)}
      onExpandAll={() => props.gapState.expandAll(opts.key(), opts.gap().lines.length)}
      splitView={props.splitView}
      isFirst={opts.isFirst?.()}
      isLast={opts.isLast}
      context={props.context}
    />
  )

  return (
    <div class={props.containerClass}>
      <Show
        when={props.gapData()}
        fallback={(
          <For each={props.groups()}>
            {(group, groupIdx) => {
              const gapBefore = () => props.syntheticGaps().get(hunkIndexOf(group, groupIdx()))
              return (
                <>
                  <Show when={gapBefore()}>
                    {gap => <DiffGapSummarySeparator gap={gap()} splitView={props.splitView} isFirst={groupIdx() === 0} />}
                  </Show>
                  {props.renderGroup(group, groupIdx)}
                </>
              )
            }}
          </For>
        )}
      >
        {gd => (
          <For each={props.groups()}>
            {(group, groupIdx) => {
              const hi = () => hunkIndexOf(group, groupIdx())
              const gapBefore = () => gd().gaps.get(hi())
              const isTrailing = () => groupIdx() === props.groups().length - 1 ? gd().trailing : null
              return (
                <>
                  <Show when={gapBefore()}>
                    {gap => revealSeparator({ key: () => `before-${hi()}`, gap, isFirst: () => groupIdx() === 0 })}
                  </Show>
                  {props.renderGroup(group, groupIdx)}
                  <Show when={isTrailing()}>
                    {trailing => revealSeparator({ key: () => 'trailing', gap: trailing, isLast: true })}
                  </Show>
                </>
              )
            }}
          </For>
        )}
      </Show>
    </div>
  )
}

/** Render a unified diff view from hunks. */
function UnifiedDiffView(props: { hunks: StructuredPatchHunk[], filePath?: string, originalFile?: string, showLineNumbers?: boolean, context?: RenderContext }): JSX.Element {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath, () => props.context)
  const lines = createMemo(() => buildUnifiedLines(props.hunks, oldTokens(), newTokens()))
  const groups = createMemo(() => groupByHunk(lines()))
  const { gapData, syntheticGaps } = useGapData(() => props.hunks, () => props.originalFile)
  const gapState = useGapReveals()

  return (
    <DiffGapScaffold
      containerClass={diffContainerClass(diffContainer, props.showLineNumbers)}
      groups={groups}
      gapData={gapData}
      syntheticGaps={syntheticGaps}
      gapState={gapState}
      filePath={props.filePath}
      context={props.context}
      renderGroup={group => <For each={group}>{line => <UnifiedDiffLine line={line} />}</For>}
    />
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
function SplitDiffView(props: { hunks: StructuredPatchHunk[], filePath?: string, originalFile?: string, showLineNumbers?: boolean, context?: RenderContext }): JSX.Element {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath, () => props.context)
  const splitLines = createMemo(() => buildSplitLines(props.hunks, oldTokens(), newTokens()))
  const { gapData, syntheticGaps } = useGapData(() => props.hunks, () => props.originalFile)
  const gapState = useGapReveals()
  const leftGroups = createMemo(() => groupByHunk(splitLines().left))
  const rightGroups = createMemo(() => groupByHunk(splitLines().right))

  return (
    <DiffGapScaffold
      containerClass={diffContainerClass(diffSplitContainer, props.showLineNumbers)}
      splitView
      groups={leftGroups}
      gapData={gapData}
      syntheticGaps={syntheticGaps}
      gapState={gapState}
      filePath={props.filePath}
      context={props.context}
      renderGroup={(leftGroup, groupIdx) => {
        // The right side is paired positionally with the left group; a left line with no
        // right counterpart renders an empty cell (carrying the group's hunk index).
        const hi = () => leftGroup[0]?.hunkIndex ?? groupIdx()
        const rightGroup = () => rightGroups()[groupIdx()] ?? []
        return (
          <For each={leftGroup}>
            {(leftLine, i) => {
              const rightLine = () => rightGroup()[i()] ?? { content: '', type: 'empty' as const, num: null, hunkIndex: hi() }
              return <SplitDiffRow left={leftLine} right={rightLine()} />
            }}
          </For>
        )
      }}
    />
  )
}

/** Renders a diff view (unified or split) from hunks with optional syntax highlighting. */
export function DiffView(props: { hunks: StructuredPatchHunk[], view: DiffViewPreference, filePath?: string, originalFile?: string, showLineNumbers?: boolean, context?: RenderContext }): JSX.Element {
  return (
    <Show
      when={props.view === 'unified'}
      fallback={(
        <SplitDiffView
          hunks={props.hunks}
          filePath={props.filePath}
          originalFile={props.originalFile}
          showLineNumbers={props.showLineNumbers}
          context={props.context}
        />
      )}
    >
      <UnifiedDiffView
        hunks={props.hunks}
        filePath={props.filePath}
        originalFile={props.originalFile}
        showLineNumbers={props.showLineNumbers}
        context={props.context}
      />
    </Show>
  )
}
