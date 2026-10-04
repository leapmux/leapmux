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
 * Read one tool row, with the final frame of its call when the span holds one.
 *
 * MiMo writes the whole call state on every frame, so the row reads the LATEST frame
 * the span holds: an opening row whose call already answered draws the answer's
 * status in its header.
 *
 * Only a frame of THIS call counts as its result. One turn can run several calls at
 * once, and a sibling's frame is no side of this one: its answer would otherwise draw
 * under this call's header.
 */
function mimoToolSpanRow(input: RowExtractionInput): ChatRow | null {
  const { resolved: parsed, span } = input
  const own = mimoToolPart(parsed.parentObject)
  if (!own)
    return null
  const completion = input.completion ?? parsed.completion
  const side = span.result ? mimoToolPart(span.result.parentObject) : null
  const result = side?.callId === own.callId ? side : null
  const role = mimoToolSpanRole(own, completion) === 'result' ? 'result' : 'request'
  const call = mimoToolCall({
    own,
    ...(result ? { result } : {}),
    ...(completion !== undefined ? { completion } : {}),
    rowFinal: role === 'result',
  })
  return toolCallRow(call, role, span.visibleRows)
}
