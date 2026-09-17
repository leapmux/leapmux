import type { ChatRowIR, ToolRowRole } from '../../../ir/row'
import type { ToolCallIR } from '../../../ir/toolCall'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { pickString } from '~/lib/jsonPick'
import { toolCallRow } from '../../../ir/row'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { copilotEvent, copilotEventData } from '../protocol'
import { copilotToolCallIR, copilotToolRow } from './toolCall'

/** The text of one assistant or reasoning event. */
export function copilotEventText(parsed: unknown): string {
  return pickString(copilotEvent(parsed)?.data, 'content')
}

/**
 * Read one Copilot row into the shared row IR.
 *
 * Copilot's stream is a single `session.event` envelope whose `type` carries the
 * whole vocabulary, so the classification already answered what this row is; the work
 * here is turning the frame into the neutral shape.
 */
export function copilotExtractRow(input: RowExtractionInput): ChatRowIR | null {
  const { category, parsed, sides, spanType } = input
  switch (category.kind) {
    case 'assistant_text': {
      const text = copilotEventText(parsed.parentObject)
      return text ? { kind: 'assistant-text', text } : { kind: 'hidden' }
    }
    case 'assistant_thinking': {
      const text = copilotEventText(parsed.parentObject)
      return text ? { kind: 'assistant-thinking', text } : { kind: 'hidden' }
    }
    case 'tool_use':
    case 'tool_result':
      return copilotToolSpanRow(input.parsed, sides, spanType)
    case 'user_content':
      return leapmuxUserRow(parsed.parentObject)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(parsed.parentObject)
    default:
      return null
  }
}

/**
 * Resolve all three sides of one Copilot tool span into rows.
 *
 * A RESULT event states no tool name and no arguments of its own, so it reads them off
 * the paired start. The OPENER resolves against ITSELF, which is what an opener's own
 * row needs -- passing the span's request there would pair a row with a sibling.
 */
function copilotToolSpanRow(
  parsed: ParsedMessageContent,
  sides: RowExtractionInput['sides'],
  spanType: string | undefined,
): ChatRowIR | null {
  // ONE call from every side of THIS call the store resolved: the start event
  // identifies the tool and the arguments, the completion the outcome. A start for
  // another call would supply the wrong name and the wrong arguments.
  const row = copilotToolRow(parsed.parentObject, spanType, sides.request, parsed.completion)
  if (!row)
    return null
  const call: ToolCallIR = copilotToolCallIR(row)
  const role: ToolRowRole = row.finished ? 'result' : 'request'
  // Copilot's span sides are events of every kind, so each one must NAME this call
  // before it counts as the row beside it.
  return toolCallRow(call, role, {
    request: copilotSideIsStart(sides.request, row.toolCallId),
    result: copilotSideNames(sides.result, row.toolCallId),
  })
}

/** Whether one side is the START event of the named call. */
function copilotSideIsStart(side: ParsedMessageContent | undefined, toolCallId: string): boolean {
  const data = copilotEventData(side?.parentObject, COPILOT_EVENT.ToolStarted)
  return !!data && pickString(data, 'toolCallId') === toolCallId
}

/**
 * Whether one side states THIS call, whichever frame it is.
 *
 * A turn that ended while a call ran stores the start frame AGAIN as the closing
 * row, so the result side can legitimately be a start event. Asking "is it not a
 * start frame" therefore answered no for exactly that case: the opener then read as
 * having no result row, `rowDrawsResult` returned true for both halves, and a
 * retained subagent launch drew its prompt card twice.
 */
function copilotSideNames(side: ParsedMessageContent | undefined, toolCallId: string): boolean {
  const parent = side?.parentObject
  const data = copilotEventData(parent, COPILOT_EVENT.ToolStarted) ?? copilotEventData(parent, COPILOT_EVENT.ToolCompleted)
  return !!data && pickString(data, 'toolCallId') === toolCallId
}
