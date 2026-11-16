import type { LucideIcon } from 'lucide-solid'
import type { JSX } from 'solid-js'
import type { FileEditDiff } from '../../model/fileEditDiff'
import type { ToolCallStatus } from '../../model/toolCallStatus'
import type { ToolKind } from '../../model/toolKind'
import type { ToolRequestByKind, ToolResultByKind } from '../../model/tools'
import type { FileChangeRequest, FileChangeResult } from '../../model/tools/fileChange'
import type { ToolResultRenderContext } from '../../renderContext'
import type { ParsedCall, ToolKindMeta, ToolKindRenderer, ToolRowView } from './renderer'
import { For, Show } from 'solid-js'
import { relativizePath } from '~/lib/paths'
import { fileEditCopyableText, fileEditHasDiff, requestedFileChangesCopyable } from '../../model/fileEditDiff'
import { toolCallStatusOutcome } from '../../model/toolCallStatus'
import { toolInputText, toolResultPrompt } from '../../toolStyles.css'
import { fileEditCopyFinal, fileEditCopyHunks, FileEditDiffBody, FileEditDiffTitle } from '../fileEditDiff'
import { RequestedFileChanges } from '../requestedFileChanges'

/**
 * The changes a call ASKED for.
 *
 * `delete`, `edit`, `move` and `write` state a file change and share one rule: the
 * PENDING row states the request while the call runs, and it leaves when the result
 * row joins the list. Once the result states the changes that LANDED, those are the ones
 * a reader needs, and the request's copy would repeat them. A call that ended with no
 * typed result -- a result in words alone, or none at all -- states the changes it asked
 * for on the row that ended it, under the heading of requested changes.
 *
 * A call that FAILED, was INTERRUPTED or was DECLINED draws none of it: its result row
 * states the reason, and no row draws a diff that never reached the file. The rule
 * lives in this ONE place rather than in each extractor: Claude used to answer it by
 * emptying its own request, and the Agent Client Protocol family answered the opposite
 * way, so the same failed row drew differently depending on which agent ran it. Keeping
 * the request lets the row's title still name the file the call was about.
 */
export function RequestedChangesBody(props: { request: FileChangeRequest, view: ToolRowView }): JSX.Element {
  return (
    <Show when={props.request.changes.length > 0}>
      <RequestedFileChanges
        sources={props.request.changes}
        {...(props.view.context !== undefined
          ? { context: props.view.context }
          : {})}
      />
    </Show>
  )
}

/**
 * True when the call ended without applying anything, so the file took nothing.
 *
 * `declined` belongs here for the same reason `failed` and `interrupted` do. A reader
 * REFUSED the call, so the tool never ran and no line reached the file.
 */
export function fileChangeFailed(status: ToolCallStatus): boolean {
  const outcome = toolCallStatusOutcome(status)
  return outcome === 'failed' || outcome === 'interrupted' || outcome === 'declined'
}

/** The changes that LANDED, one diff per file operation. */
export function FileChangesBody(props: { changes: FileEditDiff[], view: ToolRowView }): JSX.Element {
  return (
    <For each={props.changes}>
      {source => (
        <>
          <Show when={props.changes.length > 1}>
            <div class={toolResultPrompt}>
              <FileEditDiffTitle
                source={source}
                {...(props.view.context !== undefined
                  ? { context: props.view.context }
                  : {})}
              />
            </div>
          </Show>
          <Show when={fileEditHasDiff(source)}>
            <FileEditDiffBody
              source={source}
              diff={{
                view: () => props.view.context?.diffView?.() ?? 'unified',
              }}
              {...(props.view.context !== undefined
                ? { context: props.view.context }
                : {})}
            />
          </Show>
        </>
      )}
    </For>
  )
}

/**
 * The title a set of file changes states: the one change's own title, a count
 * in one file, or a count of files.
 */
export function fileChangesTitle(changes: FileEditDiff[], replaceAll: boolean | undefined, context: ToolResultRenderContext | undefined): JSX.Element | string | null {
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
            <FileEditDiffTitle
              source={change}
              {...(context !== undefined
                ? { context }
                : {})}
            />
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

/** Whether the Copy text of every change states its final diff; see fileEditCopyHunks. */
const copyHunksFinal = (changes: FileEditDiff[]) => () => changes.every(source => fileEditCopyFinal(source))

/** The meta a landed set of changes offers. */
export function fileChangesMeta(changes: FileEditDiff[]): ToolKindMeta {
  const copyable = () => changes.map(source => fileEditCopyableText(source, fileEditCopyHunks)).filter(Boolean).join('\n\n') || null
  return {
    collapsible: false,
    hasDiff: changes.some(fileEditHasDiff),
    copyableContent: copyable,
    copyableFinal: copyHunksFinal(changes),
    previewText: () => changes.map(source => source.filePath).join('\n') || null,
  }
}

/** The meta a REQUESTED set of changes offers: the request alone, no landed diff. */
export function requestedFileChangesMeta(changes: FileEditDiff[]): ToolKindMeta {
  return {
    collapsible: false,
    hasDiff: changes.some(fileEditHasDiff),
    copyableContent: () => requestedFileChangesCopyable(changes, fileEditCopyHunks) || null,
    copyableFinal: copyHunksFinal(changes),
    previewText: () => changes.map(source => source.filePath).join('\n') || null,
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
  [K in ToolKind]: ToolRequestByKind[K] extends FileChangeRequest
    ? ToolResultByKind[K] extends FileChangeResult ? K : never
    : never
}[ToolKind]

/**
 * The renderer every file-change kind gets.
 *
 * `delete`, `edit`, `move` and `write` differ in an icon, a noun, and whether the kind
 * words a title of its own out of the REQUEST. Everything else is the same for all
 * four: the two bodies, the two metas, and the rule below. Each of the four stated
 * it separately, down to the same two-line comment written out four times.
 *
 * A FAILED call shows only the reason, never the diff it asked for. Nothing it asked
 * for reached the file, so a diff would draw an edit that never happened. Every row
 * that draws `RequestedChangesBody` -- the pending row, a lone call, and the row that
 * ended a call with no typed result -- draws it through this one renderer, so no kind can
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
  title?: (call: ParsedCall<K>, context: ToolResultRenderContext | undefined) => JSX.Element | string | null
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
    pending(call, view) {
      return <RequestedChangesBody request={call.request} view={view} />
    },
    result(call, view) {
      return <FileChangesBody changes={call.result.changes} view={view} />
    },
    // A call that ended with no typed result and did not fail -- a result in words alone,
    // or no result at all -- states the changes it asked for, under the heading of
    // requested changes: the row that ended the call is then the one place that shows
    // them, and nothing on it claims that the file took them. A failed, interrupted or
    // declined call states only its reason, because nothing it asked for reached the file.
    withoutTypedResult(call, view) {
      return <Show when={!(fileChangeFailed(call.status))}><RequestedChangesBody request={call.request} view={view} /></Show>
    },
    withoutTypedResultMeta(call) {
      return fileChangeFailed(call.status)
        ? {}
        : {
            hasDiff: call.request.changes.some(fileEditHasDiff),
          }
    },
    // The request row states the changes it asked for whether or not the call still
    // runs, because it never reads how the call ended. Its diff toggle steers the pending
    // row below it, which reads the same message's diff view, so the row offers the
    // toggle only while that pending row is listed (see MessageBubble's
    // hasToolResultDiff).
    requestMeta(call) {
      return requestedFileChangesMeta(call.request.changes)
    },
    resultMeta(call) {
      return fileChangesMeta(call.result.changes)
    },
  }
}
