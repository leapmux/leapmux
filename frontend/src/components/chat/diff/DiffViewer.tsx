import type { JSX } from 'solid-js'
import type { DiffGap, DiffGapSummary, DiffLineEntry, SplitLineEntry, StructuredPatchHunk } from './diffTypes'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import type { CachedToken } from '~/lib/tokenCache'
import ArrowDownFromLine from 'lucide-solid/icons/arrow-down-from-line'
import ArrowUpFromLine from 'lucide-solid/icons/arrow-up-from-line'
import LoaderCircle from 'lucide-solid/icons/loader-circle'
import { createEffect, createMemo, createSignal, For, on, onCleanup, Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { guessLanguage } from '~/lib/languageMap'
import { pluralize } from '~/lib/plural'
import { tokenizeAsync } from '~/lib/shikiWorkerClient'
import { getCachedTokens } from '~/lib/tokenCache'
import { spinner } from '~/styles/animations.css'
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

/** Line limit for diff syntax highlighting. */
const HIGHLIGHT_LINE_LIMIT = 1000

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
            next.set(untokenizedIndices[j], tokens![j])
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

/** Render a unified diff view from hunks. */
function UnifiedDiffView(props: { hunks: StructuredPatchHunk[], filePath?: string, originalFile?: string, showLineNumbers?: boolean }): JSX.Element {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath)
  const lines = createMemo(() => buildUnifiedLines(props.hunks, oldTokens(), newTokens()))
  const groups = createMemo(() => groupByHunk(lines()))

  const { gapData, syntheticGaps } = useGapData(() => props.hunks, () => props.originalFile)
  const { getReveal, expandDown, expandUp, expandAll } = useGapReveals()

  return (
    <div class={diffContainerClass(diffContainer, props.showLineNumbers)}>
      <Show
        when={gapData()}
        fallback={(
          <For each={groups()}>
            {(group, groupIdx) => {
              const hi = () => group[0]?.hunkIndex ?? groupIdx()
              const gapBefore = () => syntheticGaps().get(hi())
              return (
                <>
                  <Show when={gapBefore()}>
                    {gap => (
                      <DiffGapSummarySeparator
                        gap={gap()}
                        isFirst={groupIdx() === 0}
                      />
                    )}
                  </Show>
                  <For each={group}>
                    {line => <UnifiedDiffLine line={line} />}
                  </For>
                </>
              )
            }}
          </For>
        )}
      >
        {gd => (
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
        )}
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
function SplitDiffView(props: { hunks: StructuredPatchHunk[], filePath?: string, originalFile?: string, showLineNumbers?: boolean }): JSX.Element {
  const { oldTokens, newTokens } = useDiffTokens(() => props.hunks, () => props.filePath)
  const splitLines = createMemo(() => buildSplitLines(props.hunks, oldTokens(), newTokens()))

  const { gapData, syntheticGaps } = useGapData(() => props.hunks, () => props.originalFile)
  const { getReveal, expandDown, expandUp, expandAll } = useGapReveals()
  const leftGroups = createMemo(() => groupByHunk(splitLines().left))
  const rightGroups = createMemo(() => groupByHunk(splitLines().right))

  return (
    <div class={diffContainerClass(diffSplitContainer, props.showLineNumbers)}>
      <Show
        when={gapData()}
        fallback={(
          <For each={leftGroups()}>
            {(leftGroup, groupIdx) => {
              const hi = () => leftGroup[0]?.hunkIndex ?? groupIdx()
              const rightGroup = () => rightGroups()[groupIdx()] ?? []
              const gapBefore = () => syntheticGaps().get(hi())
              return (
                <>
                  <Show when={gapBefore()}>
                    {gap => (
                      <DiffGapSummarySeparator
                        gap={gap()}
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
                </>
              )
            }}
          </For>
        )}
      >
        {(gd) => {
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
export function DiffView(props: { hunks: StructuredPatchHunk[], view: DiffViewPreference, filePath?: string, originalFile?: string, showLineNumbers?: boolean }): JSX.Element {
  return (
    <Show when={props.view === 'unified'} fallback={<SplitDiffView hunks={props.hunks} filePath={props.filePath} originalFile={props.originalFile} showLineNumbers={props.showLineNumbers} />}>
      <UnifiedDiffView hunks={props.hunks} filePath={props.filePath} originalFile={props.originalFile} showLineNumbers={props.showLineNumbers} />
    </Show>
  )
}
