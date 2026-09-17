import type { ChatRowIR, ToolRowRole } from '../../../ir/row'
import type { ToolCallIR } from '../../../ir/toolCall'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { toolCallRow } from '../../../ir/row'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { zcodeAssistantText } from '../messageContent'
import { zcodePlanText } from './plan'
import { zcodeToolCallIR } from './toolCall'
import { zcodeExtractTool, zcodeRow, zcodeToolSpanRole } from './toolCommon'

/**
 * Read one ZCode row into the shared row IR.
 *
 * A PLAN leaves the tool path twice over: `ExitPlanMode` proposes a plan rather than
 * returning a tool result, and a saved plan-approval control row carries the same
 * text. `classify` routes both to `assistant_plan` through the same reader this
 * case calls, so the row the list measures and the row drawn here agree.
 */
export function zcodeExtractRow(input: RowExtractionInput): ChatRowIR | null {
  const { category, parsed, spanType } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text': {
      // A model response with no text is a row with nothing to show, not one this
      // provider failed to read.
      const text = zcodeAssistantText(payload)
      return text ? { kind: 'assistant-text', text } : { kind: 'hidden' }
    }
    case 'assistant_plan': {
      // The same reader the classifier used, so the two layers state one answer.
      const plan = zcodePlanText(payload, spanType, parsed.supplementalContent)
      return plan === null ? null : { kind: 'assistant-plan', text: plan }
    }
    case 'tool_use':
    case 'tool_result':
      return zcodeToolSpanRow(input)
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    default:
      return null
  }
}

/**
 * Resolve all three sides of one ZCode tool span into rows.
 *
 * A SCHEDULED row IS the request of its own span, and the store resolves no separate
 * one for it -- so the row itself stands in, which is what lets a still-open call
 * carry the input the `model.streaming` fragments delivered before it.
 */
function zcodeToolSpanRow(input: RowExtractionInput): ChatRowIR | null {
  const { parsed, sides, spanType } = input
  // A SCHEDULED row IS the request of its own span, and the store resolves no
  // separate one for it -- so the row itself stands in, which is what lets a
  // still-open call carry the input the `model.streaming` fragments delivered.
  const spanRequest = sides.request
    ?? (zcodeExtractTool(parsed.parentObject)?.kind === ZCODE_TOOL_KIND.Scheduled ? sides.current : undefined)
  const own = zcodeRow(parsed.parentObject, spanType, spanRequest, parsed.supplementalContent)
  const call: ToolCallIR | null = zcodeToolCallIR({ ...own, ...(sides.result !== undefined ? { result: sides.result } : {}) }, parsed)
  if (!call)
    return null
  const update = zcodeExtractTool(parsed.parentObject)
  const role: ToolRowRole = zcodeToolSpanRole(update?.kind ?? '', parsed) === 'result'
    ? 'result'
    : update?.kind === ZCODE_TOOL_KIND.Scheduled ? 'request' : 'update'
  return toolCallRow(call, role, { request: !!sides.request, result: !!sides.result })
}
