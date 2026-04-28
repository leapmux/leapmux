import type { JSX } from 'solid-js'
import type { ParsedUnifiedDiff, StructuredPatchHunk } from '../../../diff'
import type { RenderContext } from '../../../messageRenderers'
import type { CommandStreamSegment } from '~/stores/chat.store'
import File from 'lucide-solid/icons/file'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import FilePlus from 'lucide-solid/icons/file-plus'
import { createMemo, For, Show } from 'solid-js'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { isObject, pickString } from '~/lib/jsonPick'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { CODEX_ITEM, CODEX_STATUS } from '~/types/toolMessages'
import { diffStatsFromHunks, parseUnifiedDiffCached, rawDiffToHunks } from '../../../diff'
import { FileEditDiffBody } from '../../../results/fileEditDiff'
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
import { codexFileEditFromAdd, codexFileEditFromHunks } from '../extractors/fileEdit'
import { LiveStreamOutput } from '../renderHelpers'
import { readLiveStream } from '../status'

function codexChangeKind(change: Record<string, unknown>): string {
  const kind = change.kind
  if (typeof kind === 'string')
    return kind
  if (isObject(kind) && typeof kind.type === 'string')
    return kind.type as string
  return ''
}

function isSimpleAddChange(change: Record<string, unknown>): boolean {
  return codexChangeKind(change) === 'add' && typeof change.diff === 'string' && (change.diff as string).length > 0
}

function isSimpleDeleteChange(change: Record<string, unknown>): boolean {
  return codexChangeKind(change) === 'delete'
}

type CompletedFileChangeEntry
  = | { kind: 'diff', path: string, hunks: StructuredPatchHunk[] }
    | { kind: 'add', path: string, hunks: StructuredPatchHunk[] }

function completedFileChangeEntries(
  changes: Array<Record<string, unknown>>,
  parsedDiffs: Map<Record<string, unknown>, ParsedUnifiedDiff | null>,
): CompletedFileChangeEntry[] {
  return changes.flatMap((change): CompletedFileChangeEntry[] => {
    const path = pickString(change, 'path')
    const diffText = pickString(change, 'diff')
    const parsed = parsedDiffs.get(change) ?? null
    if (parsed) {
      return [{ kind: 'diff', path, hunks: parsed.hunks }]
    }
    if (isSimpleAddChange(change)) {
      return [{ kind: 'add', path, hunks: rawDiffToHunks('', diffText) }]
    }
    return []
  })
}

interface FileChangeShape {
  changes: Array<Record<string, unknown>>
  parsedDiffs: Map<Record<string, unknown>, ParsedUnifiedDiff | null>
  simpleAdd: Record<string, unknown> | null
  simpleAddPath: string
  simpleAddContent: string
  simpleDelete: Record<string, unknown> | null
  simpleDeletePath: string
}

function buildFileChangeShape(item: Record<string, unknown>): FileChangeShape {
  const changes = (item.changes as Array<Record<string, unknown>>) || []
  const parsedDiffs = new Map<Record<string, unknown>, ParsedUnifiedDiff | null>()
  for (const change of changes) {
    const diffText = typeof change.diff === 'string' ? change.diff : ''
    parsedDiffs.set(change, diffText ? parseUnifiedDiffCached(diffText) : null)
  }
  const simpleAdd = changes.length === 1 && isSimpleAddChange(changes[0]) ? changes[0] : null
  const simpleDelete = changes.length === 1 && isSimpleDeleteChange(changes[0]) ? changes[0] : null
  return {
    changes,
    parsedDiffs,
    simpleAdd,
    simpleAddPath: simpleAdd ? pickString(simpleAdd, 'path') : '',
    simpleAddContent: simpleAdd ? pickString(simpleAdd, 'diff') : '',
    simpleDelete,
    simpleDeletePath: simpleDelete ? pickString(simpleDelete, 'path') : '',
  }
}

interface FileChangeRenderArgs {
  shape: FileChangeShape
  context: RenderContext | undefined
  liveStream: () => CommandStreamSegment[]
  hasLiveStream: () => boolean
}

function renderCompletedFileChange(args: FileChangeRenderArgs): JSX.Element {
  const { shape, context } = args
  if (shape.simpleAdd) {
    return (
      <ToolResultMessage
        resultContent=""
        diffSource={codexFileEditFromAdd(shape.simpleAddPath, shape.simpleAddContent)}
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
                  source={codexFileEditFromHunks(entry.path, entry.hunks)}
                  view={context?.diffView?.() ?? 'unified'}
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

  const onlyChange = shape.changes.length === 1 ? shape.changes[0] : null
  const onlyChangeDiff = onlyChange ? shape.parsedDiffs.get(onlyChange) ?? null : null
  if (onlyChange && codexChangeKind(onlyChange) === 'update' && onlyChangeDiff) {
    const editPath = pickString(onlyChange, 'path')
    return { icon: FileEdit, title: renderEditTitle(editPath, onlyChangeDiff.oldText, onlyChangeDiff.newText, false, cwd, homeDir), path: editPath }
  }
  return null
}

function renderInProgressFileChange(args: FileChangeRenderArgs): JSX.Element {
  const { shape, context, liveStream, hasLiveStream } = args
  const header = inProgressSimpleHeader(shape, context)
  if (header) {
    const title = header.title || (
      <span class={toolInputPath}>{relativizePath(header.path, context?.workingDir, context?.homeDir)}</span>
    )
    return (
      <ToolUseLayout icon={header.icon} toolName="File Change" title={title} context={context} alwaysVisible>
        <Show when={hasLiveStream()}>
          <LiveStreamOutput stream={liveStream} />
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
      <Show when={hasLiveStream()}>
        <LiveStreamOutput stream={liveStream} />
      </Show>
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
    const liveStream = () => readLiveStream(props.context)
    const hasLiveStream = (): boolean => liveStream().length > 0
    const renderArgs = (): FileChangeRenderArgs => ({
      shape: shape(),
      context: props.context,
      liveStream,
      hasLiveStream,
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
