import type { JSX } from 'solid-js'
import type { ChatRow, ToolCallRow } from '../model/row'
import type { ToolResultRenderContext } from '../renderContext'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { createMemo, Show } from 'solid-js'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { toolInputSummary, toolMessage, toolPending } from '../toolStyles.css'
import { TRUNCATION_NOTICE } from '../truncationNotice'
import { createToolRowState } from './toolRowState'
import { PlainTextResult } from './tools/plainTextResult'

/**
 * The tool REQUEST row that a pending row draws for: the one row that the list places a
 * pending row after (see chatPendingRows). Null for every other row.
 */
export function pendingRowRequest(row: ChatRow | null): ToolCallRow | null {
  return row?.kind === 'tool' && row.role === 'request'
    ? row
    : null
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
export function ToolPendingMessage(props: {
  row: ToolCallRow
  context?: ToolResultRenderContext
  /**
   * Whether the browser holds the result message of the call. This row then stays only
   * until the result row is measured (see chatPendingRows).
   */
  resultHeld?: () => boolean
}): JSX.Element {
  const { parts, view } = createToolRowState({
    row: () => props.row,
    context: () => props.context,
    drawsResult: () => false,
    // A pending row draws no summary: the request row above it states the command.
    onSummaryOverflow: () => {},
  })
  // The Worker broadcasts this output on the ephemeral channel. The store drops it the
  // moment the result message arrives, but this row stays until the result row is
  // measured, and the result row then replaces it in one update. So the row keeps the
  // last output that it drew while the result is held: the swap then moves nothing. A
  // turn that ends clears the output of a call that never answered, and no result row
  // replaces the row then, so it keeps nothing.
  const liveTail = createMemo((last: ToolProgressEntry | undefined) =>
    props.context?.toolProgress?.liveTail()
    ?? (props.resultHeld?.() === true
      ? last
      : undefined))
  // The output that the row draws: the Worker can send line breaks before the first
  // printed line.
  const liveText = () => stripLeadingBlankLines(liveTail()?.outputTail ?? '')
  // The root takes `toolMessage` and `data-tool-message`, as every tool row does: the body
  // keeps the type size of the request row, and MessageBubble adds no copy button of its
  // own to a code block inside it.
  return (
    <div
      class={`${toolMessage} ${toolPending}`}
      data-tool-message
      data-testid="tool-pending"
    >
      {parts().renderer.pending?.(parts().parsed, view())}
      <Show when={liveText()}>{text => <PlainTextResult text={text()} view={view()} />}</Show>
      <Show when={liveTail()?.outputTruncated}><div class={toolInputSummary}>{TRUNCATION_NOTICE}</div></Show>
    </div>
  )
}
