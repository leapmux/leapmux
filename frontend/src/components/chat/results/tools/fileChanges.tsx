import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { FileEditDiff } from '../../ir/fileEditDiff'
import type { ToolKind } from '../../ir/toolKind'
import type { ToolRowStatus } from '../../ir/toolRowStatus'
import type { ToolRequests, ToolResults } from '../../ir/tools'
import type { FileChangeRequest, FileChangeResult } from '../../ir/tools/fileChange'
import type { RenderContext } from '../../messageRenderers'
import type { ParsedCall, ToolKindMeta, ToolKindRenderer, ToolRowView } from './renderer'
import { For, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { fileEditCopyableText, fileEditHasDiff, requestedFileChangesCopyable } from '../../ir/fileEditDiff'
import { toolRowStatusOutcome } from '../../ir/toolRowStatus'
import { toolInputSummary, toolInputText } from '../../toolStyles.css'
import { FileEditDiffBody, FileEditDiffTitle } from '../fileEditDiff'
import { RequestedFileChanges } from '../requestedFileChanges'

/**
 * The changes a call ASKED for, drawn until its answer lands.
 *
 * `delete`, `edit`, `move` and `write` state a file change and share one rule: the
 * row that draws the RESULT is the row that states the request, and it states it
 * only while the result is absent. Once the result exists, the changes that LANDED
 * are the ones a reader needs, and the request's copy would repeat them.
 *
 * A call that FAILED or was INTERRUPTED draws none of it. Nothing it asked for
 * reached the file, so a diff here would show an edit that never happened. The rule lives in this ONE place
 * rather than in each extractor: Claude used to answer it by emptying its own
 * request, and the Agent Client Protocol family answered the opposite way, so the
 * same failed row drew differently depending on which agent ran it. Keeping the
 * request lets the row's title still name the file the call was about.
 */
export function RequestedChangesBody(props: { request: FileChangeRequest, view: ToolRowView, hasResult: boolean, status: ToolRowStatus }): JSX.Element {
  return (
    <Show when={props.view.drawsResult && !props.hasResult && !fileChangeFailed(props.status) && props.request.changes.length > 0}>
      <RequestedFileChanges sources={props.request.changes} {...(props.view.context !== undefined ? { context: props.view.context } : {})} />
    </Show>
  )
}

/** The changes that LANDED, one diff per file operation. */
export function FileChangesBody(props: { changes: FileEditDiff[], view: ToolRowView }): JSX.Element {
  return (
    <For each={props.changes}>
      {source => (
        <>
          <Show when={props.changes.length > 1}>
            <div class={toolInputSummary}><FileEditDiffTitle source={source} {...(props.view.context !== undefined ? { context: props.view.context } : {})} /></div>
          </Show>
          <Show when={fileEditHasDiff(source)}><FileEditDiffBody source={source} diff={{ view: () => props.view.context?.diffView?.() ?? 'unified' }} {...(props.view.context !== undefined ? { context: props.view.context } : {})} /></Show>
        </>
      )}
    </For>
  )
}

/**
 * The title a set of file changes states: the one change's own title, a count
 * in one file, or a count of files.
 */
export function fileChangesTitle(changes: FileEditDiff[], replaceAll: boolean | undefined, context: RenderContext | undefined): JSX.Element | string | null {
  if (changes.length === 0)
    return null
  if (changes.length === 1) {
    const change = changes[0]
    // The count gate above holds exactly one change; the undefined arm is the
    // type-level guard alone.
    return change === undefined
      ? null
      : (
          <>
            <FileEditDiffTitle source={change} {...(context !== undefined ? { context } : {})} />
            <Show when={replaceAll}><span class={toolInputText}>{' (replace all)'}</span></Show>
          </>
        )
  }
  const paths = new Set(changes.map(source => source.filePath))
  // A non-empty list's first change always exists; the undefined arm is the
  // type-level guard alone.
  const first = changes[0]
  return paths.size === 1 && first !== undefined
    ? `${changes.length} changes in ${relativizePath(first.filePath, context?.workingDir, context?.homeDir)}`
    : `${paths.size} files changed`
}

/** The meta a landed set of changes offers. */
export function fileChangesMeta(changes: FileEditDiff[]): ToolKindMeta {
  const copyable = () => changes.map(fileEditCopyableText).filter(Boolean).join('\n\n') || null
  return {
    collapsible: false,
    hasDiff: changes.some(fileEditHasDiff),
    copyableContent: copyable,
    previewText: () => changes.map(source => source.filePath).join('\n') || null,
  }
}

/** The meta a REQUESTED set of changes offers: the request alone, no landed diff. */
export function requestedFileChangesMeta(changes: FileEditDiff[]): ToolKindMeta {
  return {
    collapsible: false,
    hasDiff: changes.some(fileEditHasDiff),
    copyableContent: () => requestedFileChangesCopyable(changes) || null,
    previewText: () => changes.map(source => source.filePath).join('\n') || null,
  }
}

/**
 * True when the call ended without applying anything, so the file took nothing.
 *
 * `declined` belongs here for the same reason `failed` and `interrupted` do. A
 * reader REFUSED the call, so the tool never ran and no line reached the file. A
 * declined row that drew its changes drew the identical body a PENDING row draws,
 * which states a diff the file never took.
 */
export function fileChangeFailed(status: ToolRowStatus): boolean {
  const outcome = toolRowStatusOutcome(status)
  return outcome === 'failed' || outcome === 'interrupted' || outcome === 'declined'
}

/**
 * The meta a FAILED call's requested changes offer: the toolbar states nothing
 * about a diff, because the file took none -- but the words it asked to change
 * stay copyable for a reader who wants them.
 */
export function failedFileChangesMeta(changes: FileEditDiff[]): ToolKindMeta {
  return {
    collapsible: false,
    hasDiff: false,
    copyableContent: () => requestedFileChangesCopyable(changes) || null,
  }
}

/**
 * Every kind whose request and result are file changes.
 *
 * Derived from the two tables rather than listed, so a kind that starts stating file
 * changes -- or stops -- moves in and out of the factory below by its declaration
 * alone. `ProseKind` reads its own table the same way.
 */
export type FileChangeKind = {
  [K in ToolKind]: ToolRequests[K] extends FileChangeRequest
    ? ToolResults[K] extends FileChangeResult ? K : never
    : never
}[ToolKind]

/**
 * The renderer every file-change kind gets.
 *
 * `delete`, `edit`, `move` and `write` differ in an icon, a noun, and whether the kind
 * words a title of its own out of the REQUEST. Everything else is the same for all
 * four: the two bodies, the three metas, and the rule below. Each of the four stated
 * it separately, down to the same two-line comment written out four times.
 *
 * A FAILED call shows only the reason, never the diff it asked for. Nothing it asked
 * for reached the file, so a diff would draw an edit that never happened.
 * `RequestedChangesBody` holds that rule and this is the only caller, so no kind can
 * answer it differently -- which is what used to happen, with Claude emptying its own
 * request and the Agent Client Protocol family keeping theirs.
 */
export function fileChangeRenderer<K extends FileChangeKind>(options: {
  icon: LucideIcon
  label: string
  /**
   * A title this kind words out of its own REQUEST, tried before the shared one.
   *
   * Null falls through, as `fileChangesTitle` does, so a kind states the one case it
   * words differently and nothing else.
   */
  title?: (call: ParsedCall<K>, context: RenderContext | undefined) => JSX.Element | string | null
}): ToolKindRenderer<K> {
  return {
    icon: options.icon,
    label: options.label,
    // The REQUEST leads, then the call's own title, then the kind's label: the order
    // `renderer.ts` states for every kind.
    title(call, context) {
      return options.title?.(call, context)
        ?? fileChangesTitle(call.result?.changes ?? call.request.changes, call.request.replaceAll, context)
        ?? call.title
        ?? options.label
    },
    request(call, view) {
      return <RequestedChangesBody request={call.request} view={view} hasResult={call.result !== undefined} status={call.status} />
    },
    result(call, view) {
      return <FileChangesBody changes={call.result.changes} view={view} />
    },
    requestMeta(call) {
      return fileChangeFailed(call.status)
        ? failedFileChangesMeta(call.request.changes)
        : requestedFileChangesMeta(call.request.changes)
    },
    resultMeta(call) {
      return fileChangesMeta(call.result.changes)
    },
  }
}
