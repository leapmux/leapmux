import type { JSX } from 'solid-js'
import type { StructuredPatchHunk } from '../../../diff'
import type { RenderContext } from '../../../messageRenderers'
import type { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import File from 'lucide-solid/icons/file'
import FileEdit from 'lucide-solid/icons/file-pen-line'
import FilePlus from 'lucide-solid/icons/file-plus'
import { For, Show } from 'solid-js'
import { DiffStatsBadge } from '~/components/tree/gitStatusUtils'
import { isObject, pickString } from '~/lib/jsonPick'
import { relativizePath } from '~/lib/paths'
import { pluralize } from '~/lib/plural'
import { rawDiffToHunks } from '../../../diff'
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
import { codexFileEditFromAdd, codexFileEditFromHunks } from '../extractors/fileEdit'
import { extractItem, LiveStreamOutput } from '../renderHelpers'

const CODEX_DIFF_HEADER_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

interface ParsedCodexDiff {
  hunks: StructuredPatchHunk[]
  oldText: string
  newText: string
}

function parseCodexUnifiedDiff(diff: string): ParsedCodexDiff | null {
  if (!diff.trim())
    return null

  const lines = diff.split('\n')
  const hunks: StructuredPatchHunk[] = []
  const oldLines: string[] = []
  const newLines: string[] = []
  let current: StructuredPatchHunk | null = null

  for (const line of lines) {
    const header = line.match(CODEX_DIFF_HEADER_RE)
    if (header) {
      current = {
        oldStart: Number.parseInt(header[1], 10),
        oldLines: header[2] ? Number.parseInt(header[2], 10) : 1,
        newStart: Number.parseInt(header[3], 10),
        newLines: header[4] ? Number.parseInt(header[4], 10) : 1,
        lines: [],
      }
      hunks.push(current)
      continue
    }
    if (!current)
      continue
    if (line.startsWith('\\ No newline at end of file'))
      continue
    if (!line.startsWith('+') && !line.startsWith('-') && !line.startsWith(' '))
      continue

    current.lines.push(line)
    const prefix = line[0]
    const text = line.slice(1)
    if (prefix === '+' || prefix === ' ')
      newLines.push(text)
    if (prefix === '-' || prefix === ' ')
      oldLines.push(text)
  }

  if (hunks.length === 0)
    return null

  return {
    hunks,
    oldText: oldLines.join('\n'),
    newText: newLines.join('\n'),
  }
}

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

function completedFileChangeEntries(
  changes: Array<Record<string, unknown>>,
  parsedDiffs: Map<Record<string, unknown>, ParsedCodexDiff | null>,
): Array<
  | { kind: 'diff', path: string, hunks: StructuredPatchHunk[] }
  | { kind: 'add', path: string, hunks: StructuredPatchHunk[] }
> {
  return changes.flatMap((change): Array<
    | { kind: 'diff', path: string, hunks: StructuredPatchHunk[] }
    | { kind: 'add', path: string, hunks: StructuredPatchHunk[] }
  > => {
    const path = pickString(change, 'path')
    const diffText = pickString(change, 'diff')
    const parsed = parsedDiffs.get(change) ?? null
    if (parsed) {
      return [{ kind: 'diff' as const, path, hunks: parsed.hunks }]
    }
    if (isSimpleAddChange(change)) {
      return [{ kind: 'add' as const, path, hunks: rawDiffToHunks('', diffText) }]
    }
    return []
  })
}

function diffStatsFromHunks(hunks: StructuredPatchHunk[]): { added: number, deleted: number } {
  let added = 0
  let deleted = 0
  for (const hunk of hunks) {
    for (const line of hunk.lines) {
      if (line.startsWith('+'))
        added++
      else if (line.startsWith('-'))
        deleted++
    }
  }
  return { added, deleted }
}

/** Renders Codex fileChange items using shared ToolUseLayout. */
export function codexFileChangeRenderer(parsed: unknown, _role: MessageRole, context?: RenderContext): JSX.Element | null {
  const item = extractItem(parsed)
  if (!item || item.type !== 'fileChange')
    return null

  const changes = (item.changes as Array<Record<string, unknown>>) || []
  const status = (item.status as string) || ''
  const liveStream = () => context?.commandStream?.() ?? []
  const hasLiveStream = () => liveStream().length > 0
  const parsedDiffs = new Map<Record<string, unknown>, ParsedCodexDiff | null>()
  for (const change of changes) {
    const diffText = typeof change.diff === 'string' ? change.diff : ''
    parsedDiffs.set(change, diffText ? parseCodexUnifiedDiff(diffText) : null)
  }
  const completedEntries = completedFileChangeEntries(changes, parsedDiffs)
  const simpleAdd = changes.length === 1 && isSimpleAddChange(changes[0]) ? changes[0] : null
  const simpleDelete = changes.length === 1 && isSimpleDeleteChange(changes[0]) ? changes[0] : null
  const simpleAddPath = simpleAdd ? ((simpleAdd.path as string) || '') : ''
  const simpleAddContent = simpleAdd ? ((simpleAdd.diff as string) || '') : ''
  const simpleDeletePath = simpleDelete ? ((simpleDelete.path as string) || '') : ''

  if (status === 'completed' && simpleAdd) {
    return (
      <ToolResultMessage
        resultContent=""
        diffSource={codexFileEditFromAdd(simpleAddPath, simpleAddContent)}
        context={context}
      />
    )
  }

  if (status === 'completed' && simpleDelete) {
    return (
      <ToolResultMessage
        resultContent={`Deleted \`${relativizePath(simpleDeletePath, context?.workingDir, context?.homeDir)}\``}
        displayKind="markdown"
        context={context}
      />
    )
  }

  if (status === 'completed' && completedEntries.length > 0) {
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

  if (status === 'completed') {
    return (
      <div class={toolMessage}>
        <Show when={changes.length > 0 && completedEntries.length === 0}>
          <div class={toolResultPrompt}>
            {`${pluralize(changes.length, 'file')} changed`}
          </div>
        </Show>
      </div>
    )
  }

  const onlyChange = changes.length === 1 ? changes[0] : null
  const onlyChangeDiff = onlyChange ? parsedDiffs.get(onlyChange) ?? null : null
  const simpleEdit = onlyChange && codexChangeKind(onlyChange) === 'update' && onlyChangeDiff ? onlyChange : null
  const parsedDiff = simpleEdit ? onlyChangeDiff : null
  const cwd = context?.workingDir
  const homeDir = context?.homeDir
  const inProgressHeader = simpleAdd
    ? { icon: FilePlus, title: renderWriteTitle(simpleAddPath, simpleAddContent, cwd, homeDir), path: simpleAddPath }
    : simpleDelete
      ? { icon: File, title: renderDeleteTitle(simpleDeletePath, cwd, homeDir), path: simpleDeletePath }
      : simpleEdit && parsedDiff
        ? { icon: FileEdit, title: renderEditTitle((simpleEdit.path as string) || '', parsedDiff.oldText, parsedDiff.newText, false, cwd, homeDir), path: (simpleEdit.path as string) || '' }
        : null

  if (inProgressHeader) {
    const title = inProgressHeader.title || (
      <span class={toolInputPath}>{relativizePath(inProgressHeader.path, context?.workingDir, context?.homeDir)}</span>
    )

    return (
      <ToolUseLayout
        icon={inProgressHeader.icon}
        toolName="File Change"
        title={title}
        context={context}
        alwaysVisible={true}
      >
        <Show when={hasLiveStream()}>
          <LiveStreamOutput stream={liveStream} />
        </Show>
      </ToolUseLayout>
    )
  }

  const titleEl = (
    <span class={toolInputSummary}>
      {changes.length === 1
        ? relativizePath(String(changes[0].path || 'file'), context?.workingDir, context?.homeDir)
        : `${changes.length} files`}
    </span>
  )

  return (
    <ToolUseLayout
      icon={FileEdit}
      toolName="File Change"
      title={titleEl}
      context={context}
      alwaysVisible={true}
    >
      <Show when={hasLiveStream()}>
        <LiveStreamOutput stream={liveStream} />
      </Show>
      <For each={changes.length === 1 ? [] : changes}>
        {(change) => {
          const path = relativizePath((change.path as string) || '(unknown)', context?.workingDir, context?.homeDir)
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
    </ToolUseLayout>
  )
}
