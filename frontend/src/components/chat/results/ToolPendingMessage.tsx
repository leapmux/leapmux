import type { JSX } from 'solid-js'
import type { ChatRow, ToolCallRow } from '../model/row'
import type { ToolResultRenderContext } from '../renderContext'
import type { ToolProgressEntry } from '~/stores/chatToolProgress'
import { Show } from 'solid-js'
import { stripLeadingBlankLines } from '~/lib/normalizeProgressOutput'
import { toolMessage, toolPending } from '../toolStyles.css'
import { createToolRowState } from './toolRowState'
import { PlainTextResult } from './tools/plainTextResult'
import { ToolTruncationNotice } from './ToolTruncationNotice'

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
   * The output held by the pending row owner across measurement and remounts.
   */
  preview?: (() => Pick<ToolProgressEntry, 'outputTail' | 'outputTruncated'> | undefined) | undefined
}): JSX.Element {
  const { parts, view } = createToolRowState({
    row: () => props.row,
    context: () => props.context,
    drawsResult: () => false,
    // A pending row draws no summary: the request row above it states the command.
    onSummaryOverflow: () => {},
  })
  // Standalone previews read live output. The list supplies its retained snapshot.
  const liveTail = () => props.preview === undefined
    ? props.context?.toolProgress?.liveTail()
    : props.preview()
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
      <ToolTruncationNotice when={liveTail()?.outputTruncated} />
    </div>
  )
}
