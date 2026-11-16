import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { isObject } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { ohMyPiContentText } from '../messageContent'
import { ohMyPiToolCall, ohMyPiToolRow, ohMyPiToolSpanRowRole } from './toolCall'
import { ohMyPiExtractTool } from './toolCommon'

/**
 * Read one omp row into the shared row model.
 *
 * omp's stream is flat JSONL, so the row's own `type` and the classification already
 * agree on what the row is; the work here turns the frame into the neutral shape.
 *
 * No omp frame reads as thinking. The worker persists the thinking of each assistant
 * message as a reasoning row of its own, in LeapMux's assembled-message envelope,
 * which the shared transcript draws before any plugin reads a row.
 */
export function ohMyPiExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text': {
      const text = isObject(payload)
        ? ohMyPiContentText(payload)
        : ''
      // A message with no text block has nothing to show; it is not a row this
      // provider failed to read.
      return text
        ? { kind: 'assistant-text', text }
        : { kind: 'hidden' }
    }
    case 'tool_use':
    case 'tool_result':
      return ohMyPiToolSpanRow(payload, parsed, span)
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    default:
      return null
  }
}

/** The call id a span side carries, or null when the side states none. */
function sideCallId(side: ParsedMessageContent | undefined): string | null {
  return (side
    ? ohMyPiExtractTool(side.parentObject)?.toolCallId
    : '') || null
}

/**
 * The row an omp tool frame becomes, from its own frame and the span's request.
 *
 * A request row reads its own start frame alone. A result row also reads the start
 * frame, which states the arguments that an end frame omits. Only a request of THIS
 * call counts: one turn can run several calls at once, and a sibling's frame is no side
 * of this one.
 */
function ohMyPiToolSpanRow(payload: unknown, parsed: ParsedMessageContent, span: RowExtractionInput['span']): ChatRow | null {
  if (!isObject(payload))
    return null
  const own = ohMyPiExtractTool(payload)
  if (!own)
    return null
  const request = sideCallId(span.request) === own.toolCallId
    ? span.request
    : undefined
  const row = ohMyPiToolRow(payload, request, parsed.completion)
  if (!row)
    return null
  return toolCallRow(ohMyPiToolCall(row, parsed.completion), ohMyPiToolSpanRowRole(row), span.visibleRows)
}
