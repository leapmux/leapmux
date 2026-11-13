import type { Accessor, JSX } from 'solid-js'
import type { MarkdownRenderContext } from '../renderContext'
import type { RenderReadiness } from '../renderReadiness'
import type { TokenGate } from '../useAsyncCodeTokens'
import type { ComputedDiff, WholeReplacementReason, WordDiffs } from './diffCompute'
import type { DiffGap, DiffGapSummary, DiffLineEntry, DiffLineMarks, DiffMarkSources, SplitLineEntry, SplitRowEntry, SplitRowMarks, StructuredPatchHunk } from './diffTypes'
import type { HunkLineGroup } from './progressiveDiffLines'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { CachedToken } from '~/lib/tokenCache'
import ArrowDownFromLine from 'lucide-solid/icons/arrow-down-from-line'
import ArrowUpFromLine from 'lucide-solid/icons/arrow-up-from-line'
import { createComputed, createMemo, createRoot, createSignal, For, getOwner, on, onCleanup, Show, untrack } from 'solid-js'
import { ansiSyncTokenize } from '~/lib/ansiTokenize'
import { guessLanguage } from '~/lib/languageMap'
import { pluralize } from '~/lib/plural'
import { shouldPauseSyntaxHighlighting } from '../renderContext'
import { createRenderReadinessScope, useRenderReadiness, useReplacingReadiness } from '../renderReadiness'
import { canHighlightBySize } from '../results/collapse'
import { useAsyncCodeTokens } from '../useAsyncCodeTokens'
import { computeGapMap, computeSyntheticGapMap, countHunkLines, extractSidesFromHunks, limitDiffLines, sameHunks } from './diffBuilder'
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
  diffLayoutIncoming,
  diffLayoutShown,
  diffLine,
  diffLineNumber,
  diffLineNumberNew,
  diffPrefix,
  diffRemoved,
  diffReplacementNotice,
  diffSplitContainer,
  diffTruncationNotice,
} from './diffStyles.css'
import { buildSplitRows, buildUnifiedLines, renderMarkedLine, renderTokenizedLine, sameLineMarks, sameSplitRowMarks, splitRowMarks, unifiedLineMarks } from './diffTokenRender'
import { createLineGroupElement, createProgressiveHunkLines, createProgressiveLines } from './progressiveDiffLines'

/** Number of context lines to reveal per expand click. */
const GAP_EXPAND_STEP = 10

/**
 * The class list for one gap separator row, for both kinds of separator.
 *
 * The two differ only in what makes them first or last -- an expandable gap is
 * flush with the container edge only while nothing above or below it is
 * revealed -- so the caller answers that and the row's own composition lives
 * here once.
 */
function gapSeparatorClass(opts: { splitView?: boolean, isFirst: boolean, isLast: boolean }): string {
  let cls = diffGapSeparator
  if (opts.splitView)
    cls += ` ${diffGapSeparatorSplit}`
  if (opts.isFirst)
    cls += ` ${diffGapSeparatorFirst}`
  if (opts.isLast)
    cls += ` ${diffGapSeparatorLast}`
  return cls
}

/** One unchanged line of the original file that a gap reveals. */
interface GapContextLine {
  /** The 1-based number of the line in the original file. */
  number: number
  text: string
}

/**
 * Draw one unchanged line that a gap reveals, with its syntax tokens or plain.
 * The split view draws it in both columns, because the two gutters of an
 * unchanged line are the same.
 */
function renderGapContextLine(line: GapContextLine, tokens: Accessor<CachedToken[] | null>, splitView: boolean): JSX.Element {
  const content = () => renderTokenizedLine(line.text, tokens())
  if (!splitView) {
    return (
      <div class={diffLine}>
        <span class={diffLineNumber}>{line.number}</span>
        <span class={diffLineNumberNew}>{line.number}</span>
        <span class={diffPrefix}>{' '}</span>
        <span class={diffContent}>{content()}</span>
      </div>
    )
  }
  const column = () => (
    <div class={diffLine}>
      <span class={diffLineNumber}>{line.number}</span>
      <span class={diffPrefix}>{' '}</span>
      <span class={diffContent}>{content()}</span>
    </div>
  )
  return [
    column(),
    column(),
  ]
}

/** Whether two token lists of a gap line draw the same. */
function sameGapTokens(a: CachedToken[] | null, b: CachedToken[] | null): boolean {
  return a === b
}

/**
 * Render a gap separator that incrementally expands context lines (up to 10 at
 * a time). The revealed lines mount in slices (see createProgressiveLines): the
 * top block grows down from the separator above it, and the bottom block grows
 * up from the hunk below it.
 *
 * The lines that the gap reveals when the separator is created, which the
 * reader revealed in another layout or for an earlier diff, belong to the mount
 * of the layout: the separator reports them to `revealReadiness` until they
 * have mounted. The lines of a later expand mount in view, and report nothing.
 */
function DiffGapSeparator(props: {
  /** The gap. A gap of other lines needs a new separator. */
  gap: DiffGap
  /** File path used for lazy syntax highlighting of revealed gap lines. */
  filePath: string | undefined
  revealedTop: number
  revealedBottom: number
  /** Where the separator reports the lines that it reveals at its creation until they mount. */
  revealReadiness: RenderReadiness
  onExpandUp: () => void
  onExpandDown: () => void
  onExpandAll: () => void
  /** Whether this is rendered inside a split diff grid (needs gridColumn span). */
  splitView: boolean
  /** True when this gap is the first element in the diff container. */
  isFirst: boolean
  /** True when this gap is the last element in the diff container. */
  isLast: boolean
  context: MarkdownRenderContext | undefined
}): JSX.Element {
  const gap = untrack(() => props.gap)
  const total = gap.lines.length
  // The reveal counts live under the key of the gap (see useGapReveals), so a
  // diff of other hunks can give the key a shorter gap than the counts that it
  // keeps. Every reader takes the counts that fit in this gap.
  const revealedTop = () => Math.min(props.revealedTop, total)
  const revealedBottom = () => Math.min(props.revealedBottom, total - revealedTop())
  const hiddenCount = () => total - revealedTop() - revealedBottom()
  // Single memoized source for the revealed top/bottom slices, which the
  // eligibility check and the tokenized `gapCode` read, so that the token
  // array that `gapMarks` indexes by position (top slice first, then bottom
  // slice) cannot drift from the lines.
  const revealedSlices = createMemo(() => ({
    top: gap.lines.slice(0, revealedTop()),
    bottom: gap.lines.slice(total - revealedBottom()),
  }))

  // Tokenize the REVEALED gap lines (top slice + bottom slice) through the shared
  // useAsyncCodeTokens state machine -- the same cache / worker-dispatch / hold-defer /
  // synchronous-seed path every other diff and tool code surface uses, rather than a
  // hand-rolled copy. Each expand changes the revealed set (and thus the joined code), so
  // the hook re-tokenizes the current set (cached per state) and the synchronous seed
  // paints already-cached lines on the first frame instead of flashing plain.
  const revealedGapLines = createMemo(() => [
    ...revealedSlices().top,
    ...revealedSlices().bottom,
  ])
  const gapLang = createMemo(() => props.filePath
    ? guessLanguage(props.filePath)
    : undefined)
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
    gate: () => ({
      premeasure: false,
      hold: shouldPauseSyntaxHighlighting(props.context),
    }),
    // `ansi` (a `.log` file) has no worker grammar -- tokenize it on the main thread so a
    // `.log` diff's gap-context lines highlight like the rest of the file.
    syncTokenize: ansiSyncTokenize,
    rowOffscreen: () => props.context?.rowOffscreen?.() === true,
  })

  /**
   * The tokens of the lines of each block. The tokens index the revealed lines
   * by POSITION: the top block, then the bottom block. They belong to the
   * revealed lines of now, because useAsyncCodeTokens answers no tokens of an
   * earlier code. So a change of the revealed counts changes the tokens as
   * well, and the counts that place the tokens are read with them, untracked.
   */
  const gapMarks = createMemo(() => {
    const tokens = gapTokens()
    const top = untrack(revealedTop)
    const bottomStart = total - untrack(revealedBottom)
    const tokensOfGapLine = (gapIndex: number): CachedToken[] | null => {
      if (tokens === null)
        return null
      if (gapIndex < top)
        return tokens[gapIndex] ?? null
      if (gapIndex >= bottomStart)
        return tokens[top + (gapIndex - bottomStart)] ?? null
      return null
    }
    return {
      /** The tokens of the n-th line of the top block: the n-th line of the gap. */
      top: tokensOfGapLine,
      /** The tokens of the n-th line of the bottom block: the n-th line from the end of the gap. */
      bottom: (index: number) => tokensOfGapLine(total - 1 - index),
    }
  })
  const holdMarks = () => shouldPauseSyntaxHighlighting(props.context)
  const drawLine = (gapIndex: number, tokens: Accessor<CachedToken[] | null>): JSX.Element => renderGapContextLine(
    { number: gap.startLineNumber + gapIndex, text: gap.lines[gapIndex]! },
    tokens,
    props.splitView,
  )

  const topLines = createLineGroupElement()
  const topBlock = createProgressiveLines({
    count: revealedTop,
    marks: () => gapMarks().top,
    sameMarks: sameGapTokens,
    render: drawLine,
    place: (_, nodes) => topLines.append(...nodes),
    holdMarks,
  })
  // The bottom block grows up from the hunk below the gap: each line goes
  // before the lines that mounted before it.
  const bottomLines = createLineGroupElement()
  const bottomBlock = createProgressiveLines({
    count: revealedBottom,
    marks: () => gapMarks().bottom,
    sameMarks: sameGapTokens,
    render: (index, tokens) => drawLine(total - 1 - index, tokens),
    place: (_, nodes) => bottomLines.prepend(...nodes),
    holdMarks,
  })
  // True from the first moment that both blocks hold every line that they
  // reveal, and for good: a later expand mounts its lines in view.
  const firstRevealMounted = createMemo((mounted: boolean) => mounted || (!topBlock.mounting() && !bottomBlock.mounting()), false)
  useRenderReadiness(() => ({ readiness: props.revealReadiness }), () => !firstRevealMounted())

  const separatorClass = () => gapSeparatorClass({
    splitView: props.splitView,
    isFirst: props.isFirst && revealedTop() === 0,
    isLast: props.isLast && revealedBottom() === 0,
  })
  const hiddenLabel = () => `${pluralize(hiddenCount(), 'line')} hidden`

  return (
    <>
      {topLines}
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
      {bottomLines}
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
  const separatorClass = () => gapSeparatorClass({
    ...(props.splitView !== undefined
      ? { splitView: props.splitView }
      : {}),
    isFirst: props.isFirst === true,
    isLast: props.isLast === true,
  })
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
 * hidden remeasure). The shared size limits reject too many lines and one very
 * large line. An oversized diff renders plain. Each side answers null while its
 * tokens belong to an earlier code (see useAsyncCodeTokens).
 */
function useDiffTokens(
  hunks: () => StructuredPatchHunk[],
  filePath: () => string | undefined,
  context: () => MarkdownRenderContext | undefined,
): {
  oldTokens: () => CachedToken[][] | null
  newTokens: () => CachedToken[][] | null
} {
  // Memoized: each is read via currentKey() from both effects of BOTH per-side hooks,
  // so a plain accessor would re-run guessLanguage / the O(hunks) countHunkLines scan
  // several times per reactive pass.
  const lang = createMemo((): string | undefined => {
    const fp = filePath()
    return fp
      ? guessLanguage(fp)
      : undefined
  })
  // Extract both sides once per hunk change; shared by the two per-side hooks.
  const sides = createMemo(() => extractSidesFromHunks(hunks()))
  const eligible = createMemo((): boolean => countHunkLines(hunks()) > 0
    && canHighlightBySize(sides().oldCode)
    && canHighlightBySize(sides().newCode))
  const gate = (): TokenGate => ({
    premeasure: false,
    hold: shouldPauseSyntaxHighlighting(context()),
  })

  // syncTokenize handles `ansi` (a `.log` file's language) on the main thread -- the
  // worker's Oniguruma core has no `ansi` grammar, so without this a `.log` diff would
  // degrade to plain. Same tokenizer the Read view uses.
  const rowOffscreen = (): boolean => context()?.rowOffscreen?.() === true
  const oldTokens = useAsyncCodeTokens({
    lang,
    code: () => sides().oldCode,
    eligible,
    gate,
    syncTokenize: ansiSyncTokenize,
    rowOffscreen,
  })
  const newTokens = useAsyncCodeTokens({
    lang,
    code: () => sides().newCode,
    eligible,
    gate,
    syncTokenize: ansiSyncTokenize,
    rowOffscreen,
  })
  return { oldTokens, newTokens }
}

/** The class list of one drawn line: an added or a removed line draws its tint. */
function diffLineClass(type: SplitLineEntry['type']): string {
  if (type === 'added')
    return `${diffLine} ${diffAdded}`
  if (type === 'removed')
    return `${diffLine} ${diffRemoved}`
  return diffLine
}

/**
 * Render a unified diff line. The line itself stays for its whole life, and only
 * its marks change (see createProgressiveLines).
 */
function UnifiedDiffLine(props: { line: DiffLineEntry, marks: Accessor<DiffLineMarks> }): JSX.Element {
  return (
    <div class={diffLineClass(props.line.type)}>
      <span class={diffLineNumber}>{props.line.oldNum ?? ''}</span>
      <span class={diffLineNumberNew}>{props.line.newNum ?? ''}</span>
      <span class={diffPrefix}>{props.line.prefix}</span>
      <span class={diffContent}>{renderMarkedLine(props.line.text, props.line.type, props.marks())}</span>
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
    // The top block takes every hidden line, and the bottom block keeps the
    // lines that it shows: a block only grows, so no shown line mounts again.
    expandAll: (key, total) => mutate(key, cur => ({ top: total - cur.bottom, bottom: cur.bottom })),
  }
}

function useGapData(props: DiffBodyProps) {
  const originalFileLines = createMemo(() => {
    const source = props.originalFile
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
    const gaps = computeGapMap(props.hunks, ofl)
    // A cut diff's trailing gap starts inside the changes that the cut leaves out,
    // so it would show them as unchanged lines. The gaps before the cut come before
    // every left-out line, so they stay.
    return props.truncated
      ? { ...gaps, trailing: null }
      : gaps
  })
  const syntheticGaps = createMemo(() => computeSyntheticGapMap(props.hunks))
  return { gapData, syntheticGaps }
}

function diffContainerClass(base: string, showLineNumbers?: boolean): string {
  return showLineNumbers === false
    ? `${base} ${diffHideLineNumbers}`
    : base
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
 * container class, the `splitView` flag, and the lines in their groups (see
 * createProgressiveHunkLines). `hunkIndex` of a group indexes into the gap maps.
 *
 * The lines mount in slices, so `groups` holds only the groups whose first line has
 * mounted, and a group shows the separator before it once it mounts. The trailing
 * separator follows the last of all `groupCount` groups. A group draws its element,
 * which the slices fill: the scaffold renders nothing again when a line mounts.
 *
 * The scaffold draws a group in a computation after the slice that starts it, so it
 * reports each group that it has drawn to `trackDrawnGroup`, after the separators of
 * the group exist (see DiffBodyView.mounting).
 */
function DiffGapScaffold(props: {
  containerClass: string
  splitView: boolean
  groups: () => readonly HunkLineGroup[]
  groupCount: () => number
  gapData: () => DiffGapData | null
  syntheticGaps: () => Map<number, DiffGapSummary>
  gapState: GapState
  /** Where each separator reports the lines that it reveals at its creation (see DiffGapSeparator). */
  revealReadiness: RenderReadiness
  /** Report a group that the scaffold has drawn, with its separators, for the life of the caller's owner. */
  trackDrawnGroup: (group: HunkLineGroup) => void
  filePath: string | undefined
  context: MarkdownRenderContext | undefined
}): JSX.Element {
  // One interactive separator for a reveal key (`before-${hi}` or `trailing`). The
  // `<Show>` around each one is keyed, so a gap of other lines (the original file
  // changed) mounts a new separator, whose revealed lines mount anew. The reveal
  // counts live in `gapState` under the key, so the new separator reveals as many.
  const revealSeparator = (opts: {
    key: string
    gap: DiffGap
    isFirst?: () => boolean
    isLast?: boolean
  }): JSX.Element => (
    // `isFirst`/`isLast` stay attribute expressions rather than spreads: `isFirst`
    // reads an accessor that must re-run per reactive pass.
    <DiffGapSeparator
      gap={opts.gap}
      filePath={props.filePath}
      revealedTop={props.gapState.getReveal(opts.key).top}
      revealedBottom={props.gapState.getReveal(opts.key).bottom}
      revealReadiness={props.revealReadiness}
      onExpandDown={() => props.gapState.expandDown(opts.key, opts.gap.lines.length)}
      onExpandUp={() => props.gapState.expandUp(opts.key, opts.gap.lines.length)}
      onExpandAll={() => props.gapState.expandAll(opts.key, opts.gap.lines.length)}
      splitView={props.splitView}
      isFirst={opts.isFirst?.() === true}
      isLast={opts.isLast === true}
      context={props.context}
    />
  )
  return (
    <div class={props.containerClass} data-file-diff data-file-path={props.filePath}>
      <Show
        when={props.gapData()}
        fallback={(
          <For each={props.groups()}>
            {(group, groupIdx) => {
              const gapBefore = () => props.syntheticGaps().get(group.hunkIndex)
              const drawn = (
                <>
                  <Show when={gapBefore()}>
                    {gap => (
                      <DiffGapSummarySeparator
                        gap={gap()}
                        splitView={props.splitView}
                        isFirst={groupIdx() === 0}
                      />
                    )}
                  </Show>
                  {group.element}
                </>
              )
              props.trackDrawnGroup(group)
              return drawn
            }}
          </For>
        )}
      >
        {gd => (
          <For each={props.groups()}>
            {(group, groupIdx) => {
              const gapBefore = () => gd().gaps.get(group.hunkIndex)
              const isTrailing = () => groupIdx() === props.groupCount() - 1
                ? gd().trailing
                : null
              // Building the JSX creates the separators of the group, so the
              // group reports itself after it.
              const drawn = (
                <>
                  <Show when={gapBefore()} keyed>
                    {gap => revealSeparator({
                      key: `before-${group.hunkIndex}`,
                      gap,
                      isFirst: () => groupIdx() === 0,
                    })}
                  </Show>
                  {group.element}
                  <Show when={isTrailing()} keyed>
                    {trailing => revealSeparator({
                      key: 'trailing',
                      gap: trailing,
                      isLast: true,
                    })}
                  </Show>
                </>
              )
              props.trackDrawnGroup(group)
              return drawn
            }}
          </For>
        )}
      </Show>
    </div>
  )
}

/** What the unified view and the split view each draw. */
interface DiffBodyProps {
  hunks: StructuredPatchHunk[]
  wordDiffs: WordDiffs
  /** True when `hunks` stop before the end of the diff (see limitDiffLines). */
  truncated: boolean
  filePath: string | undefined
  originalFile: string | undefined
  showLineNumbers: boolean | undefined
  context: MarkdownRenderContext | undefined
}

/** Render one side of a split diff row. Only its marks change for its whole life. */
function SplitDiffCell(props: { line: SplitLineEntry, marks: Accessor<DiffLineMarks> }): JSX.Element {
  return (
    <div class={diffLineClass(props.line.type)}>
      <span class={diffLineNumber}>{props.line.num ?? ''}</span>
      <span class={diffPrefix}>
        {props.line.type === 'removed'
          ? '-'
          : props.line.type === 'added'
            ? '+'
            : ' '}
      </span>
      <span class={diffContent}>{renderMarkedLine(props.line.text, props.line.type, props.marks())}</span>
    </div>
  )
}

/** Render a single split diff row (left + right lines rendered in the grid). */
function SplitDiffRow(props: { row: SplitRowEntry, marks: Accessor<SplitRowMarks> }): JSX.Element {
  // A change of the marks of one side draws that side again, not the other.
  const leftMarks = createMemo(() => props.marks().left, undefined, { equals: sameLineMarks })
  const rightMarks = createMemo(() => props.marks().right, undefined, { equals: sameLineMarks })
  return (
    <>
      <SplitDiffCell line={props.row.left} marks={leftMarks} />
      <SplitDiffCell line={props.row.right} marks={rightMarks} />
    </>
  )
}

/** What differs between the unified view and the split view. */
interface DiffLayout<L extends { hunkIndex: number }, M> {
  containerClass: string
  splitView: boolean
  /** Build the lines of the hunks, once per diff. */
  buildLines: (hunks: StructuredPatchHunk[]) => L[]
  /** The marks of a line from the latest sources. */
  marksOf: (line: L, sources: DiffMarkSources) => M
  sameMarks: (a: M, b: M) => boolean
  render: (line: L, marks: Accessor<M>) => JSX.Element
}

/** The unified view: one line for each diff line, the removals of a block before its additions. */
const UNIFIED_LAYOUT: DiffLayout<DiffLineEntry, DiffLineMarks> = {
  containerClass: diffContainer,
  splitView: false,
  buildLines: buildUnifiedLines,
  marksOf: unifiedLineMarks,
  sameMarks: sameLineMarks,
  render: (line, marks) => <UnifiedDiffLine line={line} marks={marks} />,
}

/** The split view: the old side on the left, the new side on the right. */
const SPLIT_LAYOUT: DiffLayout<SplitRowEntry, SplitRowMarks> = {
  containerClass: diffSplitContainer,
  splitView: true,
  buildLines: buildSplitRows,
  marksOf: splitRowMarks,
  sameMarks: sameSplitRowMarks,
  render: (row, marks) => <SplitDiffRow row={row} marks={marks} />,
}

/** One layout of a diff, as the view draws it. */
interface DiffBodyView {
  /** The diff container, with its lines and its gap separators. */
  element: JSX.Element
  /**
   * True until the layout shows the whole diff: every line of the hunks has
   * mounted, the scaffold has drawn the group of each one with its gap
   * separators, and each separator has mounted the lines that it revealed at its
   * creation (see DiffGapSeparator). The scaffold draws a group in a computation
   * after the slice that starts it, so the check of the drawn groups keeps a
   * reader that runs between the two from reading the layout as mounted.
   */
  mounting: Accessor<boolean>
}

/**
 * Draw a diff in one layout. The lines mount in slices (see
 * createProgressiveHunkLines). The view builds them once for each diff, and a
 * later change of their tokens or word highlights applies to the drawn lines,
 * in slices too. The gaps reveal what `gapState` states, which every layout of
 * the view shares. Call it within the reactive owner of the layout.
 */
function mountDiffBody<L extends { hunkIndex: number }, M>(props: DiffBodyProps, layout: DiffLayout<L, M>, gapState: GapState): DiffBodyView {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath, () => props.context)
  // A change of a source of the marks gives a new lookup, and reads the marks of no line.
  const marks = createMemo(() => {
    const sources: DiffMarkSources = {
      oldTokens: oldTokens(),
      newTokens: newTokens(),
      wordDiffs: props.wordDiffs,
    }
    return (line: L): M => layout.marksOf(line, sources)
  })
  const holdMarks = () => shouldPauseSyntaxHighlighting(props.context)
  // Each diff builds its lines once, and mounts them anew: the memo disposes the
  // lines of the previous diff when the hunks change.
  const lines = createMemo(() => {
    const built = layout.buildLines(props.hunks)
    return untrack(() => createProgressiveHunkLines<L, M>({
      lines: built,
      marks,
      sameMarks: layout.sameMarks,
      render: layout.render,
      holdMarks,
    }))
  })
  const { gapData, syntheticGaps } = useGapData(props)
  const revealReadiness = createRenderReadinessScope()
  // The groups that the scaffold has drawn. A set that each draw mutates, with a
  // version that notifies its readers: a copy of the set for each group would
  // cost O(groups) for each of the groups of a large diff.
  const drawnGroups = new Set<HunkLineGroup>()
  const [drawnGroupsVersion, setDrawnGroupsVersion] = createSignal(0)
  const trackDrawnGroup = (group: HunkLineGroup): void => {
    drawnGroups.add(group)
    setDrawnGroupsVersion(version => version + 1)
    onCleanup(() => {
      drawnGroups.delete(group)
      setDrawnGroupsVersion(version => version + 1)
    })
  }
  const hasUndrawnGroup = (): boolean => {
    drawnGroupsVersion()
    return lines().groups().some(group => !drawnGroups.has(group))
  }

  return {
    element: (
      <DiffGapScaffold
        containerClass={diffContainerClass(layout.containerClass, props.showLineNumbers)}
        splitView={layout.splitView}
        groups={() => lines().groups()}
        groupCount={() => lines().groupCount}
        gapData={gapData}
        syntheticGaps={syntheticGaps}
        gapState={gapState}
        revealReadiness={revealReadiness}
        trackDrawnGroup={trackDrawnGroup}
        filePath={props.filePath}
        context={props.context}
      />
    ),
    mounting: () => lines().mounting() || revealReadiness.pending() || hasUndrawnGroup(),
  }
}

/** What the notice above a whole replacement states, by its reason. */
const WHOLE_REPLACEMENT_NOTICES: Record<WholeReplacementReason, string> = {
  'too-many-edits': 'Too many lines changed to compare them one by one. This diff removes every old line and adds every new line.',
  'not-computed': 'The line-by-line comparison did not finish. This diff removes every old line and adds every new line.',
}

/** A layout of the diff that the view has mounted, with the root that owns it. */
interface MountedLayout {
  view: DiffViewPreference
  body: DiffBodyView
  dispose: () => void
}

/**
 * Renders a computed diff (unified or split) with optional syntax highlighting.
 *
 * The diff worker computed the hunks and the word diffs (see diffWorkerClient),
 * so drawing does no diff of its own. It draws at most the `drawnLineLimit` of
 * the diff, which its input states (see DiffInput), and states how many more
 * lines it leaves out. A diff that replaces the whole old text in place of the
 * changed lines states why, so that a reader does not take it for a change to
 * every line.
 *
 * The lines mount in slices of main-thread work, and a later change of their
 * tokens or word highlights applies in slices too (see createProgressiveLines).
 * The view reports pending to the readiness scope of `context` while the
 * layout that it shows mounts its lines: at first, and after a diff of other
 * lines.
 *
 * A switch between unified and split keeps the layout that the view shows,
 * which has mounted, until the new layout has mounted behind it, invisible.
 * Then the view shows the new layout and drops the old one, whose lines it
 * disposes in later slices. So the switch reports nothing pending to the
 * readiness scope of `context`: it reports that the view replaces its content
 * instead (see RenderReadiness.trackReplacing), for a consumer that must wait
 * for the new layout, such as the row measurer of the chat or the scroll
 * restore of the file viewer.
 */
export function DiffView(props: {
  diff: ComputedDiff
  view: DiffViewPreference
  filePath?: string
  originalFile?: string
  showLineNumbers?: boolean
  context?: MarkdownRenderContext
}): JSX.Element {
  // Equal hunks keep their identity, so that a diff object that a row builds
  // again from an equal source mounts no line again. Only the word diffs and the
  // tokens of such a diff can differ, and the drawn lines take them in place.
  const hunks = createMemo(() => props.diff.hunks, undefined, { equals: sameHunks })
  const drawnLineLimit = createMemo(() => props.diff.drawnLineLimit)
  const drawn = createMemo(() => limitDiffLines(hunks(), drawnLineLimit()))
  // Each part that a layout reads changes only with its own value: the lines
  // build again only for other drawn hunks, and the drawn lines read their
  // marks again only for other word diffs.
  const drawnHunks = createMemo(() => drawn().hunks)
  const truncated = createMemo(() => drawn().hiddenLines > 0)
  const wordDiffs = createMemo(() => props.diff.wordDiffs)
  const bodyProps: DiffBodyProps = {
    get hunks() {
      return drawnHunks()
    },
    get wordDiffs() {
      return wordDiffs()
    },
    get truncated() {
      return truncated()
    },
    get filePath() {
      return props.filePath
    },
    get originalFile() {
      return props.originalFile
    },
    get showLineNumbers() {
      return props.showLineNumbers
    },
    get context() {
      return props.context
    },
  }

  // The layouts share the lines that the reader revealed, so that a switch
  // between unified and split keeps them.
  const gapState = useGapReveals()
  const owner = getOwner()
  const mountLayout = (view: DiffViewPreference): MountedLayout => createRoot(dispose => ({
    view,
    body: view === 'unified'
      ? mountDiffBody(bodyProps, UNIFIED_LAYOUT, gapState)
      : mountDiffBody(bodyProps, SPLIT_LAYOUT, gapState),
    dispose,
  }), owner)
  // A memo of the value: a consumer can notify `view` with the value that it
  // holds, as a tab patch of another field does, and each notification would
  // otherwise drop the new layout that mounts and start it again.
  const requestedView = createMemo(() => props.view)
  const [shown, setShown] = createSignal(untrack(() => mountLayout(requestedView())))
  const [incoming, setIncoming] = createSignal<MountedLayout>()

  createComputed(on(requestedView, (nextView) => {
    untrack(incoming)?.dispose()
    setIncoming(undefined)
    const current = untrack(shown)
    if (current.view === nextView)
      return
    const next = mountLayout(nextView)
    // A layout that has not mounted every line keeps nothing worth showing:
    // the new one takes its place at once, and the readiness scope of the
    // context stays pending until it mounts.
    if (untrack(current.body.mounting)) {
      current.dispose()
      setShown(next)
    }
    else {
      setIncoming(next)
    }
  }, { defer: true }))
  // Show the new layout once it shows the whole diff (see DiffBodyView.mounting),
  // and drop the old one: it leaves the document at once, and later slices
  // dispose its lines (see createProgressiveLines).
  createComputed(() => {
    const next = incoming()
    if (next === undefined || next.body.mounting())
      return
    untrack(shown).dispose()
    setShown(next)
    setIncoming(undefined)
  })
  onCleanup(() => {
    untrack(shown).dispose()
    untrack(incoming)?.dispose()
  })

  // Outside a readiness scope the view draws no skeleton: it shows its lines as
  // they mount.
  useRenderReadiness(() => props.context, () => shown().body.mounting())
  useReplacingReadiness(() => props.context, () => incoming() !== undefined)

  const layouts = createMemo((): readonly MountedLayout[] => {
    const next = incoming()
    return next === undefined
      ? [
          shown(),
        ]
      : [
          shown(),
          next,
        ]
  })
  return (
    <>
      <Show when={props.diff.wholeReplacement}>
        {reason => (
          <div class={diffReplacementNotice} data-testid="diff-replacement-notice">
            {WHOLE_REPLACEMENT_NOTICES[reason()]}
          </div>
        )}
      </Show>
      <For each={layouts()}>
        {layout => (
          <div class={layout === shown()
            ? diffLayoutShown
            : diffLayoutIncoming}
          >
            {layout.body.element}
          </div>
        )}
      </For>
      <Show when={drawn().hiddenLines > 0}>
        <div class={diffTruncationNotice} data-testid="diff-truncation-notice">
          {`${pluralize(drawn().hiddenLines, 'more line')} of this diff ${drawn().hiddenLines === 1
            ? 'is'
            : 'are'} not shown.`}
        </div>
      </Show>
    </>
  )
}
