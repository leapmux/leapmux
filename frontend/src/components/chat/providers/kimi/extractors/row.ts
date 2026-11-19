import type { ChatRow } from '../../../model/row'
import type { RowExtractionInput } from '~/components/chat/rowExtractionTypes'
import { KIMI_CONVERSATION_FIELD, KIMI_CONVERSATION_ITEM_KIND, KIMI_CONVERSATION_RECORD_ROLE } from '~/generated/contracts/kimi-protocol'
import { pickString } from '~/lib/jsonPick'
import { leapmuxPlanExecutionRow, leapmuxUserRow } from '../../../leapmuxRows'
import { toolCallRow } from '../../../model/row'
import { kimiConversationRow, kimiConversationToolsMatch } from '../conversation'
import { kimiPlanText } from './plan'
import { kimiToolCall, kimiToolRow } from './toolCall'

/** Read a public Kimi conversation record or a row written by LeapMux. */
export function kimiExtractRow(input: RowExtractionInput): ChatRow | null {
  const { category, resolved: parsed } = input
  const conversation = kimiConversationRow(parsed.parentObject)
  switch (category.kind) {
    case 'user_text':
      return conversation?.kind === 'item' && conversation.role === KIMI_CONVERSATION_RECORD_ROLE.Item
        && conversation.item[KIMI_CONVERSATION_FIELD.Type] === KIMI_CONVERSATION_ITEM_KIND.UserMessage
        ? {
            kind: 'user',
            text: kimiChildPromptText(pickString(conversation.item, KIMI_CONVERSATION_FIELD.Text)),
            attachments: [],
          }
        : null
    case 'assistant_text':
      return conversation?.kind === 'item' && conversation.item[KIMI_CONVERSATION_FIELD.Type] === KIMI_CONVERSATION_ITEM_KIND.AssistantMessage
        ? {
            kind: 'assistant-text',
            text: pickString(conversation.item, KIMI_CONVERSATION_FIELD.Text),
          }
        : null
    case 'assistant_thinking':
      return conversation?.kind === 'item' && conversation.item[KIMI_CONVERSATION_FIELD.Type] === KIMI_CONVERSATION_ITEM_KIND.Reasoning
        ? {
            kind: 'assistant-thinking',
            text: pickString(conversation.item, KIMI_CONVERSATION_FIELD.Text),
          }
        : null
    case 'assistant_plan': {
      if (conversation?.kind !== 'item' || conversation.role !== KIMI_CONVERSATION_RECORD_ROLE.Request)
        return null
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

/** Kimi injects this leading block into coordinator prompts for children. */
function kimiChildPromptText(value: string): string {
  const text = value.trim()
  const opening = '<git-context>'
  const closing = '</git-context>'
  if (!text.startsWith(opening))
    return text
  const end = text.indexOf(closing)
  return end < 0
    ? text
    : text.slice(end + closing.length).trim()
}

/**
 * Resolve one native tool row without reading a later result.
 * An earlier request supplies only the native identity check for span layout.
 */
function kimiToolSpanRow(input: RowExtractionInput): ChatRow | null {
  const { resolved: parsed, span, completion } = input
  const conversation = kimiConversationRow(parsed.parentObject)
  if (conversation?.kind !== 'item')
    return null
  const row = kimiToolRow(parsed.parentObject, completion ?? parsed.completion, parsed.topLevel)
  if (!row)
    return null
  const request = kimiConversationRow(span.request?.parentObject)
  const paired = kimiConversationToolsMatch(conversation, request)
  return toolCallRow(kimiToolCall(row), row.finished ? 'result' : 'request', { request: span.visibleRows.request && paired })
}
