import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { AMP_BLOCK_TYPE } from '~/generated/contracts/amp-protocol'
import { isObject } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { ampToolCall, ampToolRow, ampToolSpanRowRole } from './toolCall'
import { ampBlockText, ampSideCallId } from './toolCommon'

/**
 * Read one Amp row into the shared row model.
 *
 * The classification already read the row's `type` and its block, so the work here
 * turns the row into the neutral shape.
 */
export function ampExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text': {
      const text = ampBlockText(payload, AMP_BLOCK_TYPE.Text, 'text')
      // A row with no text states nothing. It is not a row that this provider failed to read.
      return text ? { kind: 'assistant-text', text } : { kind: 'hidden' }
    }
    case 'assistant_thinking':
      return { kind: 'assistant-thinking', text: ampBlockText(payload, AMP_BLOCK_TYPE.Thinking, 'thinking') }
    case 'tool_use':
    case 'tool_result':
      return ampToolSpanRow(parsed, span)
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    default:
      return null
  }
}

/**
 * The row an Amp tool row becomes, with both span sides resolved.
 *
 * Only a side of THIS call counts: one message can run several calls, and a sibling's
 * row is no side of this one.
 */
function ampToolSpanRow(parsed: ParsedMessageContent, span: RowExtractionInput['span']): ChatRow | null {
  const payload = parsed.parentObject
  if (!isObject(payload))
    return null
  const ownId = ampSideCallId(parsed)
  const mine = (side: ParsedMessageContent | undefined) => side !== undefined && ampSideCallId(side) === ownId
  const row = ampToolRow(payload, mine(span.request) ? span.request : undefined, mine(span.result) ? span.result : undefined, parsed.completion)
  if (!row)
    return null
  return toolCallRow(ampToolCall(row, parsed.completion), ampToolSpanRowRole(row), span.visibleRows)
}
