import type { JSX } from 'solid-js'
import type { MessageUiKey } from '../messageUiKeys'
import type { ToolCallRow } from '../model/row'
import type { ToolCall } from '../model/toolCall'
import type { ToolKind } from '../model/toolKind'
import type { ToolProgressSource, ToolResultRenderContext } from '../renderContext'
import type { ToolCallDispatch } from './tools/index'
import type { ToolRowView } from './tools/renderer'
import { createMemo, Show, untrack } from 'solid-js'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { useSharedExpandedState } from '../messageRenderers'
import { toolRowPosition } from '../model/derivations'
import { toolInputSummary, toolMessage, toolPendingShown } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { createRowBodyContext, rowExpandKey } from './toolRowState'
import { dispatchParts, dispatchToolCall } from './tools/index'
import { PlainTextResult } from './tools/plainTextResult'

/**
 * Whether the pending row of a call that runs has anything to draw: the preview of its
 * kind (see ToolKindRenderer.hasPending), or live output.
 *
 * The pending row takes its top space from this answer, and takes none while it draws
 * nothing, so it must answer true exactly when {@link ToolPendingMessage} draws
 * something. `expandedOf` answers the expand state of the call's message for one key, as
 * the row reads it; this picks the key that the pending row reads.
 */
export function toolCallHasPending(call: ToolCall, expandedOf: (key: MessageUiKey) => boolean, liveOutput: string | undefined): boolean {
  if (liveOutput)
    return true
  const parts = dispatchParts(call)
  const expanded = expandedOf(rowExpandKey(false, parts))
  return dispatchToolCall(call, ({ renderer, parsed }) => renderer.hasPending?.(parsed, expanded) === true)
}

/**
 * Draw the PENDING row of a tool call that still runs: the preview of what it asked,
 * and the live output it printed.
 *
 * The pending row is a row of its own, below the request row. The list places it while
 * the call runs, and removes it when the result row joins the list, in one update. So
 * the request row never changes when the result lands: this row leaves, and the result
 * row takes its place. Like a result row beside its request, it draws no header of its
 * own, and the rail of the call's span runs down its left edge. While it has nothing to
 * draw, it takes no height, and the list leaves no gap above it.
 *
 * It reads the same expand key as the request row of the call, so the request row's
 * Expand opens this row too.
 */
export function ToolPendingMessage(props: { row: ToolCallRow, context?: ToolResultRenderContext, progress?: ToolProgressSource }): JSX.Element {
  const parts = createMemo((): ToolCallDispatch<ToolKind> => dispatchParts(props.row.call))
  const expandKey = untrack(() => rowExpandKey(false, parts()))
  const [expanded, setExpanded] = useSharedExpandedState(() => props.context, expandKey)
  const bodyContext = createRowBodyContext(() => props.context, expanded, value => setExpanded(value))
  const view = createMemo((): ToolRowView => ({
    ...toolRowPosition(props.row),
    context: bodyContext(),
    expanded,
    setExpanded: value => setExpanded(value),
    imageIndexOffset: 0,
    // A pending row draws no summary: the request row above it states the command.
    onSummaryOverflow: () => {},
  }))
  // The Worker broadcasts this output on the ephemeral channel, and the store drops it
  // when the result row lands.
  const liveTail = () => props.progress?.liveTail()
  // Whether the row draws something. Only then does it take the top space that the list
  // leaves out above a pending row (see toolPendingShown).
  const shows = () => toolCallHasPending(props.row.call, () => expanded(), liveTail()?.outputTail) || liveTail()?.outputTruncated === true
  // The root takes `toolMessage` and `data-tool-message`, as every tool row does: the body
  // keeps the type size of the request row, and MessageBubble adds no copy button of its
  // own to a code block inside it.
  return (
    <div
      class={shows()
        ? `${toolMessage} ${toolPendingShown}`
        : toolMessage}
      data-tool-message
      data-testid="tool-pending"
    >
      {parts().renderer.pending?.(parts().parsed, view())}
      <Show when={liveTail()?.outputTail}>{tail => <PlainTextResult text={stripLeadingBlankLines(tail())} view={view()} />}</Show>
      <Show when={liveTail()?.outputTruncated}><div class={toolInputSummary}>{TRUNCATION_NOTICE}</div></Show>
    </div>
  )
}
