import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CLINE_EVENT } from '~/generated/contracts/cline-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { CLINE_FIELD, clinePayload } from '../protocol'
import { clineToolCall, clineToolRow, clineToolSpanRowRole } from './toolCall'
import { clineSideCallId } from './toolCommon'

/**
 * Read one Cline row into the shared row model.
 *
 * The classification already read the row's event, so the work here turns the row
 * into the neutral shape.
 */
export function clineExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed, span } = input
  const payload = parsed.parentObject
  switch (category.kind) {
    case 'assistant_text':
      return clineTextRow(payload)
    case 'assistant_thinking': {
      const text = pickString(clinePayload(payload, CLINE_EVENT.ReasoningFinished), CLINE_FIELD.Reasoning)
      return text.trim() ? { kind: 'assistant-thinking', text } : { kind: 'hidden' }
    }
    case 'tool_use':
    case 'tool_result':
      return clineToolSpanRow(parsed, span)
    case 'user_content':
      return leapmuxUserRow(payload)
    case 'plan_execution':
      return leapmuxPlanExecutionRow(payload)
    default:
      return null
  }
}

/**
 * The text row of a message: its text, or the statement of the media it returned.
 * A row with no text states nothing. It is not a row that this provider failed to read.
 */
function clineTextRow(payload: Record<string, unknown> | undefined): ChatRow {
  const text = pickString(clinePayload(payload, CLINE_EVENT.AssistantFinished), CLINE_FIELD.Text)
  if (text.trim())
    return { kind: 'assistant-text', text }
  const media = pickObject(clinePayload(payload, CLINE_EVENT.AssistantMedia), CLINE_FIELD.Media)
  if (media) {
    const type = pickString(media, 'mediaType') || pickString(media, 'mimeType') || pickString(media, 'type')
    return { kind: 'assistant-text', text: type ? `The model returned media (${type}).` : 'The model returned media.' }
  }
  return { kind: 'hidden' }
}

/**
 * The row a Cline tool row becomes, with both span sides resolved.
 *
 * Only a side of THIS call counts: one message can run several calls, and a sibling's
 * row is no side of this one.
 */
function clineToolSpanRow(parsed: ParsedMessageContent, span: RowExtractionInput['span']): ChatRow | null {
  const payload = parsed.parentObject
  if (!isObject(payload))
    return null
  const ownId = clineSideCallId(parsed)
  const mine = (side: ParsedMessageContent | undefined) => side !== undefined && clineSideCallId(side) === ownId
  const row = clineToolRow(payload, mine(span.request) ? span.request : undefined, mine(span.result) ? span.result : undefined, parsed.completion)
  if (!row)
    return null
  return toolCallRow(clineToolCall(row, parsed.completion), clineToolSpanRowRole(row), span.visibleRows)
}
