import type { ChatRow, ToolSpanRowRole } from '../../../model/row'
import type { ToolCall } from '../../../model/toolCall'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { isObject } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { createToolCall } from '../../../model/createToolCall'
import { toolCallRow } from '../../../model/row'
import { SYNTHETIC_TOOL_LIFECYCLE } from '../../../model/toolCallLifecycle'
import { piContentText } from '../messageContent'
import { piSubagentNotifications } from './customMessage'
import { piPlanStatement } from './plan'
import { piToolCall, piToolRow, piToolSpanRowRole } from './toolCall'
import { piExtractTool } from './toolCommon'

/**
 * Read one Pi row into the shared row model.
 *
 * Pi's stream is flat JSONL, so the row's own `type` and the classification already
 * agree on what this is; the work here is turning the frame into the neutral shape.
 */
export function piExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text': {
      // A `plan_mode_complete` row states its answer in the TOOL result, not in a
      // message content block, so it is read before the block scan below.
      const plan = piPlanStatement(payload)
      if (plan)
        return plan.kind === 'text' ? { kind: 'assistant-text', text: plan.text } : { kind: 'assistant-plan', text: plan.text }
      // A message with no text block is a row with nothing to show, not one this
      // provider failed to read.
      const text = isObject(payload) ? piContentText(payload, 'text') : ''
      return text ? { kind: 'assistant-text', text } : { kind: 'hidden' }
    }
    case 'assistant_thinking': {
      const text = isObject(payload) ? piContentText(payload, 'thinking') : ''
      return { kind: 'assistant-thinking', text }
    }
    case 'assistant_plan': {
      // The same reader the classifier used, so the two layers state one answer.
      const plan = piPlanStatement(payload)
      return plan?.kind === 'plan' ? { kind: 'assistant-plan', text: plan.text } : null
    }
    case 'tool_use':
      return piToolSpanRow(payload, parsed, span)
    case 'tool_result':
      return isObject(payload) ? piResultRow(payload, parsed, span) : null
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    default:
      return null
  }
}
/** The span id a side carries, or null when the side states none. */
function piSideCallId(side: ParsedMessageContent | undefined): string | null {
  const id = side ? piExtractTool(side.parentObject)?.toolCallId : ''
  return id || null
}

/** Whether this side belongs to the call the row itself names. */
function piSideIsMine(callId: string, side: ParsedMessageContent | undefined): boolean {
  const sideId = piSideCallId(side)
  return !!sideId && sideId === callId
}

/**
 * The row a Pi tool event becomes, with both span sides resolved.
 *
 * A PLAN never reaches here: `plan_mode_complete` proposes one rather than returning
 * a tool result, and `classify` already routed that frame to `assistant_plan` or
 * `assistant_text` -- see `piPlanStatement`. A `plan_mode_complete` row that
 * states NEITHER does reach here, and draws the ordinary tool row.
 */
function piToolSpanRow(
  payload: unknown,
  parsed: ParsedMessageContent,
  span: RowExtractionInput['span'],
): ChatRow | null {
  if (!isObject(payload))
    return null
  const own = piExtractTool(payload)
  if (!own)
    return null
  // ONE call from every side of THIS call the store resolved: the request names
  // the arguments, the end event the payload. A sibling from another call is no
  // side of this one at all -- one turn can run several calls at once.
  const mine = (side: ParsedMessageContent | undefined) => !!side && piSideIsMine(own.toolCallId, side)
  const request = mine(span.request) ? span.request : undefined
  const result = mine(span.result) ? span.result : undefined
  const row = piToolRow(payload, request, result, parsed.completion)
  if (!row)
    return null
  const call: ToolCall = piToolCall(row, parsed.completion)
  const role: ToolSpanRowRole = piToolSpanRowRole(row)
  return toolCallRow(call, role, span.visibleRows)
}

/**
 * A Pi tool RESULT row.
 *
 * A consolidated subagent notification classifies as a tool result and carries no tool
 * call at all, so it draws the subagent cards directly rather than through a span.
 */
function piResultRow(
  payload: Record<string, unknown>,
  parsed: ParsedMessageContent,
  span: RowExtractionInput['span'],
): ChatRow | null {
  const notifications = piSubagentNotifications(payload)
  if (notifications) {
    // Each notification is one finished subagent. They share a row, so the first one
    // carries the row and the rest follow in its body -- which is what the agent body
    // already draws for a single one.
    const [first, ...rest] = notifications
    if (!first)
      return null
    const call = createToolCall(
      { id: '', name: PI_TOOL.Agent, lifecycle: SYNTHETIC_TOOL_LIFECYCLE },
      { kind: 'agent', title: first.description || 'Subagent', request: { description: first.description || 'Subagent', prompt: '' }, result: { agents: [first, ...rest] } },
    )
    return toolCallRow(call, 'result', { request: false, result: false })
  }
  return piToolSpanRow(payload, parsed, span)
}
