import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { KIMI_CONVERSATION_FIELD, KIMI_CONVERSATION_ITEM_KIND, KIMI_CONVERSATION_RECORD_ROLE } from '~/generated/contracts/kimi-protocol'
import { kimiConversationRow } from './conversation'

/** Read span ownership from the retained native request or result role. */
export function kimiSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const conversation = kimiConversationRow(parsed.parentObject)
  if (conversation?.kind === 'item' && conversation.item[KIMI_CONVERSATION_FIELD.Type] === KIMI_CONVERSATION_ITEM_KIND.ToolCall) {
    return conversation.role === KIMI_CONVERSATION_RECORD_ROLE.Request
      ? 'request'
      : 'result'
  }
  return 'other'
}

/** Results validate their earlier request's native identity for span layout. */
export function kimiNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return kimiSpanRole(parsed) === 'result'
}
