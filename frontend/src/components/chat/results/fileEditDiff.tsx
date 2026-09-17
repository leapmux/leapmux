import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from '../diff/diffTypes'
import type { FileEditDiff } from '../ir/fileEditDiff'
import type { RenderContext } from '../messageRenderers'
import type { DiffRenderActions } from '../renderContext'
import { createMemo, Show } from 'solid-js'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { relativizePath } from '~/lib/paths'
import { diffStatsFromHunks, DiffView, rawDiffToHunks } from '../diff'
import { fileEditNewStr, fileEditOldStr, nonEmptyStructuredPatch } from '../ir/fileEditDiff'
import { cachedRenderValueForStrings } from '../messageRenderCache'
import { toolInputPath, toolInputText, toolResultPrompt } from '../toolStyles.css'

/**
 * The hunks one file edit draws, computed once per distinct content.
 *
 * A source with no structured patch -- every whole-file write, and every provider
 * that states two sides -- reaches `rawDiffToHunks`, which runs the Myers diff. The
 * title needs them for its `+N -M` badge and the body needs them to draw, and each
 * one's own memo keys on the source OBJECT, which the store replaces on every
 * streamed frame. Keying the shared render cache on the three STRINGS they derive
 * from holds the diff to once per content change, for both.
 */
function fileEditDiffHunksCached(source: FileEditDiff, context: RenderContext | undefined): StructuredPatchHunk[] {
  // The IR's own predicate, NOT a second copy of it. This inlined
  // `normalizeStructuredPatchHunks` and re-decided "does the patch win", which is the
  // rule `fileEditDiffHunks` states for the Copy action -- so a change to it landed in
  // one of the two and the drawn diff and the copied diff disagreed. It also memoizes
  // on the source, so the per-line walk happens once per row revision rather than once
  // per reader.
  const structuredPatch = nonEmptyStructuredPatch(source)
  if (structuredPatch)
    return structuredPatch
  const oldStr = fileEditOldStr(source)
  const newStr = fileEditNewStr(source)
  return cachedRenderValueForStrings(
    context,
    'fileEditDiff.hunks',
    [source.filePath, oldStr, newStr],
    () => rawDiffToHunks(oldStr, newStr),
  )
}

/** Show the paths and statistics of the actual file change. */
export function FileEditDiffTitle(props: { source: FileEditDiff, context?: RenderContext }): JSX.Element {
  const path = (value: string) => relativizePath(value, props.context?.workingDir, props.context?.homeDir)
  const stats = createMemo(() => diffStatsFromHunks(fileEditDiffHunksCached(props.source, props.context)))
  return (
    <>
      <Show when={props.source.previousPath && props.source.previousPath !== props.source.filePath}>
        <span class={toolInputPath}>
          {path(props.source.previousPath!)}
          {' '}
          →
          {' '}
        </span>
      </Show>
      <span class={toolInputPath}>{path(props.source.filePath)}</span>
      <Show when={props.source.operation === 'delete'}><span class={toolInputText}> (deleted)</span></Show>
      <DiffStatsBadge stats={{ ...stats(), untracked: 0 }} class={toolInputText} />
    </>
  )
}

export function FileEditDiffBody(props: {
  source: FileEditDiff
  /** The diff preference as a getter: the toolbar may flip it mid-row. */
  diff: DiffRenderActions
  showLineNumbers?: boolean
  context?: RenderContext
}): JSX.Element {
  // Memo: DiffView reads `hunks` from several effects/memos during a single
  // render pass; without this, `rawDiffToHunks` (and the underlying
  // `diffLines`) would re-run on every read.
  const hunks = createMemo(() => fileEditDiffHunksCached(props.source, props.context))
  return (
    <>
      <Show when={props.source.notice}><div class={toolResultPrompt}>{props.source.notice}</div></Show>
      <DiffView
        hunks={hunks()}
        view={props.diff.view()}
        filePath={props.source.filePath}
        {...(props.source.originalFile !== undefined ? { originalFile: props.source.originalFile } : {})}
        {...((props.showLineNumbers ?? props.source.showLineNumbers) !== undefined ? { showLineNumbers: props.showLineNumbers ?? props.source.showLineNumbers } : {})}
        {...(props.context !== undefined ? { context: props.context } : {})}
      />
    </>
  )
}
