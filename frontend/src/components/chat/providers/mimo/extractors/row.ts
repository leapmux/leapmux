import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { mimoToolCall } from './toolCall'
import { mimoToolPart, mimoToolSpanRole } from './toolCommon'

/** Read one MiMo row into the shared row model. */
export function mimoExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed } = input
  switch (category.kind) {
    case 'tool_use':
    case 'tool_result':
      return mimoToolSpanRow(input)
    case 'user_content':
      return leapmuxUserRow(parsed.parentObject)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(parsed.parentObject)
    default:
      return null
  }
}

/**
 * Read one tool row from its own frame.
 *
 * MiMo writes the whole call state on every frame, so each row draws the state its own
 * frame states: an opening row draws the running call, and the final row draws the
 * answer. No row reads another frame of its span.
 */
function mimoToolSpanRow(input: RowExtractionInput): ChatRow | null {
  const { resolved: parsed, span } = input
  const own = mimoToolPart(parsed.parentObject)
  if (!own)
    return null
  const completion = input.completion ?? parsed.completion
  const role = mimoToolSpanRole(own, completion) === 'result'
    ? 'result'
    : 'request'
  const call = mimoToolCall({
    own,
    ...(input.spanId
      ? { registryKey: input.spanId }
      : {}),
    ...(completion !== undefined
      ? { completion }
      : {}),
    rowFinal: role === 'result',
  })
  return toolCallRow(call, role, span.visibleRows)
}
