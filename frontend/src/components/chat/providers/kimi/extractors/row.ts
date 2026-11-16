import type { ChatRow, ToolSpanRowRole } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { KIMI_EVENT } from '~/generated/contracts/kimi-protocol'
import { pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { kimiEventData } from '../protocol'
import { kimiPlanText } from './plan'
import { kimiToolCall, kimiToolRow } from './toolCall'

/**
 * Read one Kimi Code row into the shared row model.
 *
 * The worker assembles the streamed text and thinking into rows of LeapMux's own, which
 * the shared classifier answers before this plugin runs. What reaches here is a tool
 * call, the plan an `ExitPlanMode` call proposes, and a row the service layer wrote.
 */
export function kimiExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed } = input
  switch (category.kind) {
    case 'assistant_plan': {
      const plan = kimiPlanText(parsed.parentObject)
      return plan === null
        ? null
        : { kind: 'assistant-plan', text: plan }
    }
    case 'tool_use':
    case 'tool_result':
      return kimiToolSpanRow(input)
    case 'user_content':
      return leapmuxUserRow(parsed.parentObject)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(parsed.parentObject)
    default:
      return null
  }
}

/**
 * Resolve the current row and the span's request into one Kimi Code tool call.
 *
 * A result row reads the name and the arguments off its paired start. A request row
 * reads its own start alone, so it states no result.
 */
function kimiToolSpanRow(input: RowExtractionInput): ChatRow | null {
  const { resolved: parsed, span, spanType, completion } = input
  const row = kimiToolRow(parsed.parentObject, spanType, span.request?.parentObject, completion ?? parsed.completion, parsed.topLevel)
  if (!row)
    return null
  const call = kimiToolCall(row)
  const role: ToolSpanRowRole = row.finished ? 'result' : 'request'
  // The span's request is a row of any kind, so it must state THIS call before it counts
  // as the row beside this one.
  return toolCallRow(call, role, {
    request: span.visibleRows.request && kimiSideStates(span.request, row.toolCallId, KIMI_EVENT.ToolCallStarted),
  })
}

/** Whether one side of the span is the given event of the named call. */
function kimiSideStates(side: ParsedMessageContent | undefined, toolCallId: string, type: string): boolean {
  const data = kimiEventData(side?.parentObject, type)
  return !!data && pickString(data, 'toolCallId') === toolCallId
}
