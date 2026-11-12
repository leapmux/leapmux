import type { JSX } from 'solid-js'
import type { ComputedDiff } from '../diff/diffCompute'
import type { StructuredPatchHunk } from '../diff/diffTypes'
import type { FileEditDiff } from '../model/fileEditDiff'
import type { DiffRenderActions, ToolResultRenderContext } from '../renderContext'
import { createMemo, Show, untrack } from 'solid-js'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { relativizePath } from '~/lib/paths'
import { diffStatsFromHunks, DiffView } from '../diff'
import { replacedWholeHunks } from '../diff/diffCompute'
import { computedDiffIfReady, requestComputedDiff } from '../diff/diffWorkerClient'
import { fileEditDiffInput, fileEditOldStr } from '../model/fileEditDiff'
import { toolInputPath, toolInputText, toolResultPrompt } from '../toolStyles.css'
import { ChatRowSkeleton, SKELETON_LINE_PITCH_PX } from '../widgets/ChatRowSkeleton'
import { fileEditStats, fileEditTitle } from './fileEditDiff.css'

/**
 * The diff one file edit draws, or undefined while the diff worker computes it.
 *
 * Reactive (see requestComputedDiff): the title's `+N -M` badge and the body both
 * read it in a memo, and the worker's cache keys on content, so a row that streams
 * new source objects with equal content does not compute again.
 */
function fileEditComputedDiff(source: FileEditDiff, context: ToolResultRenderContext | undefined): ComputedDiff | undefined {
  return requestComputedDiff(fileEditDiffInput(source), context?.rowOffscreen)
}

/**
 * The hunks that the Copy text of an edit states, and whether they are final.
 *
 * The Copy text reads the diff the body's worker computed, from the cache. The
 * toolbar asks for the text when the row mounts, before the worker answers, and a
 * Myers diff on the main thread is what the worker exists to avoid. Until then the
 * text states the whole old side replaced by the new one: that is empty exactly
 * when the final text is, so the Copy button shows when it should, and the toolbar
 * builds the text again until it is final (see `ToolKindMeta.copyableFinal`). The
 * row stays hidden until its diff is ready, so a reader copies the final text.
 */
export function fileEditCopyHunks(source: FileEditDiff): { hunks: StructuredPatchHunk[], final: boolean } {
  const input = fileEditDiffInput(source)
  if (input.kind === 'hunks')
    return { hunks: input.hunks, final: true }
  const diff = computedDiffIfReady(input)
  return diff === undefined
    ? {
        hunks: replacedWholeHunks(input.oldText, input.newText),
        final: false,
      }
    : { hunks: diff.hunks, final: true }
}

/** Show the paths and statistics of the actual file change. */
export function FileEditDiffTitle(props: { source: FileEditDiff, context?: ToolResultRenderContext }): JSX.Element {
  const path = (value: string) => relativizePath(value, props.context?.workingDir, props.context?.homeDir)
  // Zero while the worker computes the diff, which the badge draws as nothing.
  const stats = createMemo(() => diffStatsFromHunks(fileEditComputedDiff(props.source, props.context)?.hunks ?? []))
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

export function FileEditDiffBody(props: {
  source: FileEditDiff
  /** The diff preference as a getter: the toolbar may flip it mid-row. */
  diff: DiffRenderActions
  showLineNumbers?: boolean
  context?: ToolResultRenderContext
}): JSX.Element {
  const diff = createMemo(() => fileEditComputedDiff(props.source, props.context))
  const pending = () => diff() === undefined
  // Inside a chat row, the row stays hidden behind its own skeleton, with its
  // height uncommitted, until the diff arrives (see renderReadiness). Elsewhere,
  // the body draws a skeleton of its own.
  const readiness = untrack(() => props.context?.readiness)
  // eslint-disable-next-line solid/reactivity -- the scope reads the accessor inside its own memo
  readiness?.track(pending)
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
        fallback={readiness === undefined
          ? <ChatRowSkeleton height={DIFF_PENDING_MAX_LINES * SKELETON_LINE_PITCH_PX} seed="diff-pending" testId="diff-pending" />
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
