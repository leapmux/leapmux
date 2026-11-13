import type { JSX } from 'solid-js'
import type { ComputedDiff } from '../diff/diffCompute'
import type { DiffInput, StructuredPatchHunk } from '../diff/diffTypes'
import type { FileEditDiff } from '../model/fileEditDiff'
import type { DiffRenderActions, ToolResultRenderContext } from '../renderContext'
import { createMemo, Show } from 'solid-js'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { memoizeByObject } from '~/lib/memoizeByObject'
import { relativizePath } from '~/lib/paths'
import { diffStatsFromHunks, DiffView } from '../diff'
import { patchWithoutWordDiffs, replacedWholeHunks } from '../diff/diffCompute'
import { requestComputedDiff } from '../diff/diffWorkerClient'
import { fileEditDiffInput, fileEditOldStr } from '../model/fileEditDiff'
import { useRenderReadiness } from '../renderReadiness'
import { toolInputPath, toolInputText, toolResultPrompt } from '../toolStyles.css'
import { SKELETON_LINE_PITCH_PX, SkeletonLines } from '../widgets/ChatRowSkeleton'
import { fileEditStats, fileEditTitle } from './fileEditDiff.css'

/**
 * The diff of one file edit, or undefined while the diff worker computes it.
 *
 * Reactive (see requestComputedDiff): the body, and the `+N -M` badge of a title
 * over two sides, read it in a memo. The worker's cache keys on content, so a row
 * that streams new source objects with equal content does not compute again.
 */
function fileEditComputedDiff(source: FileEditDiff, context: ToolResultRenderContext | undefined): ComputedDiff | undefined {
  return requestComputedDiff(fileEditDiffInput(source), context?.rowOffscreen)
}

/**
 * The priority of the diff that a Copy text asks for: it may wait behind the
 * diffs that rows draw, because the text draws no line. A row that draws the diff
 * as well asks for it with its own priority.
 */
const COPY_TEXT_MAY_WAIT = () => true

/**
 * The hunks that the Copy text of an edit states, given the input of the edit
 * (see fileEditDiffInput). The Copy text builders of the model take it as their
 * DiffHunksOf.
 *
 * A patch states its own hunks. Two sides state the diff that the diff worker
 * computes, and this asks the worker for it: some rows draw no body that would
 * ask, such as a declined edit, and a Myers diff on the main thread is what the
 * worker exists to avoid. A memo that reads it re-runs when the diff arrives
 * (see requestComputedDiff).
 *
 * Until then the text states the whole old side replaced by the new one. That
 * stand-in is empty exactly when the final text is, so the Copy button shows when
 * it should. {@link fileEditCopyFinal} states which of the two this answers.
 */
export function fileEditCopyHunks(input: DiffInput): StructuredPatchHunk[] {
  if (input.kind === 'hunks')
    return input.hunks
  return requestComputedDiff(input, COPY_TEXT_MAY_WAIT)?.hunks
    ?? replacedWholeHunks(input.oldText, input.newText)
}

/**
 * Whether {@link fileEditCopyHunks} answers the final hunks, not the stand-in.
 * It builds no hunks, and it is reactive in the same way.
 */
export function fileEditCopyFinal(source: FileEditDiff): boolean {
  const input = fileEditDiffInput(source)
  return input.kind === 'hunks' || requestComputedDiff(input, COPY_TEXT_MAY_WAIT) !== undefined
}

/** The badge of a title that states no line counts. */
const NO_STATS = { added: 0, deleted: 0 }

/** Show the paths and statistics of the actual file change. */
export function FileEditDiffTitle(props: { source: FileEditDiff, context?: ToolResultRenderContext }): JSX.Element {
  const path = (value: string) => relativizePath(value, props.context?.workingDir, props.context?.homeDir)
  // A patch states its own counts. Two sides state none while the worker computes
  // their diff, nor for a diff that replaces the whole old text, whose counts would
  // state every line as changed. The badge draws zero counts as nothing.
  const stats = createMemo(() => {
    const input = fileEditDiffInput(props.source)
    if (input.kind === 'hunks')
      return diffStatsFromHunks(input.hunks)
    const diff = fileEditComputedDiff(props.source, props.context)
    return diff === undefined || diff.wholeReplacement !== null
      ? NO_STATS
      : diffStatsFromHunks(diff.hunks)
  })
  return (
    <span class={fileEditTitle}>
      <Show when={props.source.previousPath && props.source.previousPath !== props.source.filePath}>
        <>
          <span class={toolInputPath}>
            {path(props.source.previousPath!)}
            {' '}
            →
          </span>
          {' '}
        </>
      </Show>
      <span class={toolInputPath}>{path(props.source.filePath)}</span>
      <Show when={props.source.operation === 'delete'}>
        {' '}
        <span class={toolInputText}>(deleted)</span>
      </Show>
      <DiffStatsBadge
        stats={{
          ...stats(),
          untracked: 0,
        }}
        class={fileEditStats}
      />
    </span>
  )
}

/** The most skeleton lines a pending diff draws outside a chat row. */
const DIFF_PENDING_MAX_LINES = 6

/**
 * The diff that a body draws for a patch until the diff worker computes its word
 * diffs: the patch's own hunks. One object per input, so that a body that reads
 * it again while the worker computes draws nothing new.
 */
const patchBeforeWordDiffs = memoizeByObject(patchWithoutWordDiffs)

export function FileEditDiffBody(props: {
  source: FileEditDiff
  /** The diff preference as a getter: the toolbar may flip it mid-row. */
  diff: DiffRenderActions
  showLineNumbers?: boolean
  context?: ToolResultRenderContext
}): JSX.Element {
  // A patch draws its own hunks without the diff worker, and its word highlights
  // when the worker computes them. The highlights set only a background color and
  // a corner radius (see diffAddedInline), so the row keeps its height when they
  // land, and it does not wait for them. Two sides have no hunks until the worker
  // computes them, so their row waits for the diff. Either way, DiffView holds
  // the row until its last line mounts.
  const diff = createMemo((): ComputedDiff | undefined => {
    const input = fileEditDiffInput(props.source)
    return fileEditComputedDiff(props.source, props.context)
      ?? (input.kind === 'hunks'
        ? patchBeforeWordDiffs(input)
        : undefined)
  })
  const pending = () => diff() === undefined
  const drawsOwnSkeleton = useRenderReadiness(() => props.context, pending)
  // The gap separators reveal unchanged lines from the text that the hunk numbers
  // index: the original file for a patch, the old side for two sides.
  const gapSource = () => fileEditDiffInput(props.source).kind === 'hunks'
    ? props.source.originalFile
    : fileEditOldStr(props.source) || undefined
  return (
    <>
      <Show when={props.source.notice}><div class={toolResultPrompt}>{props.source.notice}</div></Show>
      <Show
        when={diff()}
        fallback={drawsOwnSkeleton
          ? <SkeletonLines height={DIFF_PENDING_MAX_LINES * SKELETON_LINE_PITCH_PX} seed="diff-pending" testId="diff-pending" />
          : null}
      >
        {computed => (
          <DiffView
            diff={computed()}
            view={props.diff.view()}
            filePath={props.source.filePath}
            {...(gapSource() !== undefined
              ? {
                  originalFile: gapSource()!,
                }
              : {})}
            {...((props.showLineNumbers ?? props.source.showLineNumbers) !== undefined
              ? { showLineNumbers: props.showLineNumbers ?? props.source.showLineNumbers }
              : {})}
            {...(props.context !== undefined
              ? { context: props.context }
              : {})}
          />
        )}
      </Show>
    </>
  )
}
