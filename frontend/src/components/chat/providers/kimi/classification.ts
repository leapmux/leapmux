import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { KIMI_CONVERSATION_FIELD, KIMI_CONVERSATION_ITEM_KIND, KIMI_CONVERSATION_RECORD_ROLE, KIMI_CONVERSATION_STATUS, KIMI_CONVERSATION_TURN_KIND, KIMI_NOTIFICATION_EVENT } from '~/generated/contracts/kimi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { kimiConversationRow, kimiTurnIsBlocked } from './conversation'
import { kimiNotificationEntry } from './extractors/notification'
import { kimiPlanText } from './extractors/plan'
import { kimiEvent } from './protocol'

/** Whether one thread entry is a Kimi Code notice. */
function kimiNotifies(entry: unknown): boolean {
  const conversation = kimiConversationRow(entry)
  if (conversation?.kind === 'notice' || conversation?.kind === 'failure' || conversation?.kind === 'retry')
    return true
  if (conversation?.kind === 'item') {
    const type = conversation.item[KIMI_CONVERSATION_FIELD.Type]
    return conversation.role === KIMI_CONVERSATION_RECORD_ROLE.Origin
      || type === KIMI_CONVERSATION_ITEM_KIND.Notification
      || type === KIMI_CONVERSATION_ITEM_KIND.System
      || (type === KIMI_CONVERSATION_ITEM_KIND.AssistantMessage && kimiTurnIsBlocked(conversation.turnContext))
  }
  const event = kimiEvent(entry)
  return !!event && KIMI_NOTIFICATION_EVENT.has(event.type)
}

/** Kimi Code message classification. */
export function classifyKimiMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, kimiNotificationEntry)

  if (wrapper) {
    if (wrapper.messages.length === 0)
      return { kind: 'hidden' }
    // A thread of Kimi notices collapses to hidden when none of them words anything.
    if (wrapper.messages.some(kimiNotifies))
      return notification(wrapper.messages, 'hidden')
    if (isNotificationThreadWrapper(wrapper))
      return notification(wrapper.messages)
  }

  if (!parent)
    return { kind: 'unknown' }

  const conversation = kimiConversationRow(parent)
  if (conversation?.kind === 'retry')
    return notification([parent], 'hidden')
  if (conversation?.kind === 'item') {
    switch (conversation.item[KIMI_CONVERSATION_FIELD.Type]) {
      case KIMI_CONVERSATION_ITEM_KIND.AssistantMessage:
        if (kimiTurnIsBlocked(conversation.turnContext)) {
          const notice = notification([parent], 'hidden')
          if (notice.kind === 'notification')
            return notice
        }
        return { kind: 'assistant_text' }
      case KIMI_CONVERSATION_ITEM_KIND.Reasoning:
        return { kind: 'assistant_thinking' }
      case KIMI_CONVERSATION_ITEM_KIND.UserMessage:
        return conversation.role === KIMI_CONVERSATION_RECORD_ROLE.Origin
          ? notification([parent], 'hidden')
          : { kind: 'user_text' }
      case KIMI_CONVERSATION_ITEM_KIND.ToolCall:
        if (kimiPlanText(parent) !== null) {
          return conversation.role === KIMI_CONVERSATION_RECORD_ROLE.Request
            ? { kind: 'assistant_plan' }
            : { kind: 'hidden' }
        }
        return {
          kind: conversation.role === KIMI_CONVERSATION_RECORD_ROLE.Request
            ? 'tool_use'
            : 'tool_result',
        }
      default:
        return notification([parent], 'hidden')
    }
  }
  if (conversation?.kind === 'turn') {
    if (conversation.turn[KIMI_CONVERSATION_FIELD.Kind] !== KIMI_CONVERSATION_TURN_KIND.Agent || kimiTurnIsBlocked(conversation.turn))
      return { kind: 'hidden' }
    return {
      kind: conversation.turn[KIMI_CONVERSATION_FIELD.Status] === KIMI_CONVERSATION_STATUS.Ended
        ? 'result_divider'
        : 'hidden',
    }
  }
  if (conversation?.kind === 'notice' || conversation?.kind === 'failure')
    return notification([parent], 'hidden')

  const event = kimiEvent(parent)
  if (!event) {
    // A row the service layer wrote: the LeapMux-neutral `{content}` user row, or one
    // of LeapMux's own notices.
    if (typeof parent.content === 'string') {
      if (parent.planExecution === true)
        return { kind: 'plan_execution' }
      return { kind: 'user_content' }
    }
    if (isObject(parent) && isPlainNotificationType(pickString(parent, 'type')))
      return notification([parent])
    return { kind: 'unknown' }
  }

  if (KIMI_NOTIFICATION_EVENT.has(event.type))
    return notification([parent], 'hidden')
  // The Worker preserves unknown native events as separate message rows.
  return { kind: 'unknown' }
}
