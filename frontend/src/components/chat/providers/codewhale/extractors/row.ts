import type { ChatRow, ToolSpanRowRole } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { codewhaleMessageText } from './message'
import { codewhaleToolCall } from './toolCall'
import { codewhaleToolFrame, codewhaleToolSpanRole } from './toolCommon'

/** Read one Codewhale row into the shared row model. */
export function codewhaleExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text':
    case 'assistant_thinking': {
      // The classifier hides a message with no text, so every message that reaches
      // here draws.
      const message = codewhaleMessageText(payload)
      if (!message)
        return null
      return { kind: message.kind === 'thinking' ? 'assistant-thinking' : 'assistant-text', text: message.text }
    }
    case 'tool_use':
    case 'tool_result':
      return codewhaleToolSpanRow(input)
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    default:
      return null
  }
}

/**
 * Resolve the current row and its paired rows into one Codewhale tool call.
 *
 * Every row that reaches here draws. The rows that draw nothing are the
 * classifier's to hide (see `codewhaleFrameDrawsNothing`), because an emptied row
 * still takes a slot in the transcript.
 */
function codewhaleToolSpanRow(input: RowExtractionInput): ChatRow | null {
  const { resolved, span, spanType } = input
  const own = codewhaleToolFrame(resolved.parentObject)
  if (!own)
    return null
  // LeapMux's own reading of how the row ended wins over the column the payload
  // carries, as it does for every row `extractChatRow` reads.
  const parsed = input.completion !== undefined ? { ...resolved, completion: input.completion } : resolved
  const call = codewhaleToolCall(own, { request: span.request, result: span.result }, parsed, spanType)
  const role: ToolSpanRowRole = codewhaleToolSpanRole(own, parsed) === 'result' ? 'result' : 'request'
  return toolCallRow(call, role, span.visibleRows)
}
