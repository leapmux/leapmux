import type { ChatRow, ToolSpanRowRole } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { kimiTranscriptTool, kimiTranscriptToolsMatch } from '../transcript'
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
 * Resolve one native tool row without reading a later result.
 * An earlier request supplies only the native identity check for span layout.
 */
function kimiToolSpanRow(input: RowExtractionInput): ChatRow | null {
  const { resolved: parsed, span, completion } = input
  const native = kimiTranscriptTool(parsed.parentObject)
  if (!native)
    return null
  const row = kimiToolRow(parsed.parentObject, completion ?? parsed.completion, parsed.topLevel)
  if (!row)
    return null
  const call = kimiToolCall(row)
  const role: ToolSpanRowRole = row.finished ? 'result' : 'request'
  return toolCallRow(call, role, {
    request: span.visibleRows.request && kimiTranscriptToolsMatch(native, kimiTranscriptTool(span.request?.parentObject)),
  })
}
