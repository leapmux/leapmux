import type { ChatRow, ToolSpanRowRole } from '../../../model/row'
import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { copilotEvent, copilotEventData } from '../protocol'
import { copilotToolCall, copilotToolRow } from './toolCall'

/** The text of one assistant or reasoning event. */
export function copilotEventText(parsed: unknown): string {
  return pickString(copilotEvent(parsed)?.data, 'content')
}

/**
 * Read one Copilot row into the shared row model.
 *
 * Copilot's stream is a single `session.event` envelope whose `type` carries the
 * whole vocabulary, so the classification already answered what this row is; the work
 * here is turning the frame into the neutral shape.
 */
export function copilotExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span, spanType } = input
  switch (category.kind) {
    case 'assistant_text': {
      const text = copilotEventText(parsed.parentObject)
      return text
        ? { kind: 'assistant-text', text }
        : { kind: 'hidden' }
    }
    case 'assistant_thinking': {
      const text = copilotEventText(parsed.parentObject)
      return text
        ? { kind: 'assistant-thinking', text }
        : { kind: 'hidden' }
    }
    case 'tool_use':
    case 'tool_result':
      return copilotToolSpanRow(parsed, span, spanType)
    case 'user_content':
      return leapmuxUserRow(parsed.parentObject)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(parsed.parentObject)
    default:
      return null
  }
}

/**
 * Resolve the current row, and for a result the span's request, into one Copilot tool
 * call.
 *
 * A RESULT event states no tool name and no arguments of its own, so it reads them off
 * the paired start. The REQUEST resolves against ITSELF, which is what a request's own
 * row needs -- passing the span's request there would pair a row with a sibling.
 */
function copilotToolSpanRow(
  parsed: ParsedMessageContent,
  span: RowExtractionInput['span'],
  spanType: string | undefined,
): ChatRow | null {
  // ONE call from this row and the start of THIS call: the start event identifies the
  // tool and the arguments, the completion the outcome. A start for another call would
  // supply the wrong name and the wrong arguments.
  const row = copilotToolRow(parsed.parentObject, { spanType, request: span.request, completion: parsed.completion })
  if (!row)
    return null
  const call: ToolCall = copilotToolCall(row)
  const role: ToolSpanRowRole = row.finished ? 'result' : 'request'
  // Copilot's span sides are events of every kind, so the request must be the start
  // of THIS call before it counts as the row beside this one.
  return toolCallRow(call, role, {
    request: span.visibleRows.request && copilotSideIsStart(span.request, row.toolCallId),
  })
}

/** Whether one side is the START event of the named call. */
function copilotSideIsStart(side: ParsedMessageContent | undefined, toolCallId: string): boolean {
  const data = copilotEventData(side?.parentObject, COPILOT_EVENT.ToolStarted)
  return !!data && pickString(data, 'toolCallId') === toolCallId
}
