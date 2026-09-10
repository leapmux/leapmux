import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import type { FileChangeShape } from '../extractors/fileChange'
import File from 'lucide-solid/icons/file'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import FilePlus from 'lucide-solid/icons/file-plus'
import { createMemo, For, Show } from 'solid-js'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { pickString } from '~/lib/jsonPick'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { diffStatsFromHunks } from '../../../diff'
import { CommandResultBody } from '../../../results/commandResult'
import { FileEditDiffBody, fileEditDiffFromHunks, fileEditDiffFromNewFile } from '../../../results/fileEditDiff'
import { ToolResultMessage, ToolUseLayout } from '../../../toolRenderers'
import {
  toolInputPath,
  toolInputSummary,
  toolInputText,
  toolMessage,
  toolResultPrompt,
} from '../../../toolStyles.css'
import { renderDeleteTitle, renderEditTitle, renderWriteTitle } from '../../../toolTitleRenderers'
import { defineCodexRenderer } from '../defineRenderer'
import {
  buildFileChangeShape,
  codexChangeKind,
  completedFileChangeEntries,
} from '../extractors/fileChange'

interface FileChangeRenderArgs {
  shape: FileChangeShape
  context: RenderContext | undefined
  output: string
}

function renderCompletedFileChange(args: FileChangeRenderArgs): JSX.Element {
  const { shape, context } = args
  if (shape.simpleAdd) {
    return (
      <ToolResultMessage
        resultContent=""
        diffSource={fileEditDiffFromNewFile(shape.simpleAddPath, shape.simpleAddContent)}
        context={context}
      />
    )
  }

  if (shape.simpleDelete) {
    return (
      <ToolResultMessage
        resultContent={`Deleted \`${relativizePath(shape.simpleDeletePath, context?.workingDir, context?.homeDir)}\``}
        displayKind="markdown"
        context={context}
      />
    )
  }

  const completedEntries = completedFileChangeEntries(shape.changes, shape.parsedDiffs)
  if (completedEntries.length > 0) {
    const showPerFileLabels = completedEntries.length > 1
    return (
      <div class={toolMessage}>
        <For each={completedEntries}>
          {(entry) => {
            const stats = diffStatsFromHunks(entry.hunks)
            return (
              <div>
                <Show when={showPerFileLabels}>
                  <div class={toolResultPrompt}>
                    <span class={toolInputPath}>{relativizePath(entry.path, context?.workingDir, context?.homeDir)}</span>
                    {' '}
                    <DiffStatsBadge stats={{ added: stats.added, deleted: stats.deleted, untracked: 0 }} class={toolInputText} />
                  </div>
                </Show>
                <FileEditDiffBody
                  source={fileEditDiffFromHunks(entry.path, entry.hunks)}
                  view={context?.diffView?.() ?? 'unified'}
                  context={context}
                />
              </div>
            )
          }}
        </For>
      </div>
    )
  }

  return (
    <div class={toolMessage}>
      <Show when={shape.changes.length > 0}>
        <div class={toolResultPrompt}>{`${pluralize(shape.changes.length, 'file')} changed`}</div>
      </Show>
    </div>
  )
}

function inProgressSimpleHeader(
  shape: FileChangeShape,
  context: RenderContext | undefined,
): { icon: typeof File, title: JSX.Element, path: string } | null {
  const cwd = context?.workingDir
  const homeDir = context?.homeDir

  if (shape.simpleAdd)
    return { icon: FilePlus, title: renderWriteTitle(shape.simpleAddPath, shape.simpleAddContent, cwd, homeDir), path: shape.simpleAddPath }
  if (shape.simpleDelete)
    return { icon: File, title: renderDeleteTitle(shape.simpleDeletePath, cwd, homeDir), path: shape.simpleDeletePath }
  // simpleUpdateDiff is non-null exactly when simpleUpdate is set (computed together).
  if (shape.simpleUpdate)
    return { icon: FileEdit, title: renderEditTitle(shape.simpleUpdatePath, shape.simpleUpdateDiff!.oldText, shape.simpleUpdateDiff!.newText, false, cwd, homeDir), path: shape.simpleUpdatePath }
  return null
}

function renderInProgressFileChange(args: FileChangeRenderArgs): JSX.Element {
  const { shape, context } = args
  const header = inProgressSimpleHeader(shape, context)
  if (header) {
    const title = header.title || (
      <span class={toolInputPath}>{relativizePath(header.path, context?.workingDir, context?.homeDir)}</span>
    )
    return (
      <ToolUseLayout icon={header.icon} toolName="File Change" title={title} context={context} alwaysVisible>
        <Show when={args.output}>
          <CommandResultBody source={{ output: args.output, isError: false }} context={context} />
        </Show>
      </ToolUseLayout>
    )
  }

  const { changes } = shape
  const titleEl = (
    <span class={toolInputSummary}>
      {changes.length === 1
        ? relativizePath(pickString(changes[0], 'path') || 'file', context?.workingDir, context?.homeDir)
        : `${changes.length} files`}
    </span>
  )

  return (
    <ToolUseLayout icon={FileEdit} toolName="File Change" title={titleEl} context={context} alwaysVisible>
      <Show when={changes.length > 1}>
        <For each={changes}>
          {(change) => {
            const path = relativizePath(pickString(change, 'path') || '(unknown)', context?.workingDir, context?.homeDir)
            const kind = codexChangeKind(change)
            return (
              <div class={toolInputPath}>
                {path}
                {' '}
                <span class={toolInputSummary}>
                  (
                  {kind}
                  )
                </span>
              </div>
            )
          }}
        </For>
      </Show>
      <Show when={args.output}>
        <CommandResultBody source={{ output: args.output, isError: false }} context={context} />
      </Show>
    </ToolUseLayout>
  )
}

// Registry-only: dispatched by `item.type === 'fileChange'` via
// `CODEX_RENDERERS` (loaded from `renderers/registerAll.ts`).
defineCodexRenderer({
  itemTypes: [CODEX_ITEM.FILE_CHANGE],
  render: (props) => {
    // Memo so streaming-chunk re-renders don't rebuild the per-change Map +
    // re-walk every change to recompute simple-add/simple-delete shapes.
    // Re-runs only when `props.item` reference changes.
    const shape = createMemo(() => buildFileChangeShape(props.item))
    const isCompleted = (): boolean => pickString(props.item, 'status') === CODEX_STATUS.COMPLETED
    const renderArgs = (): FileChangeRenderArgs => ({
      shape: shape(),
      context: props.context,
      output: pickString(props.item, 'aggregatedOutput') || '',
    })
    return (
      <Show
        when={isCompleted()}
        fallback={renderInProgressFileChange(renderArgs())}
      >
        {renderCompletedFileChange(renderArgs())}
      </Show>
    )
  },
})
