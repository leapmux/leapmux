import type { MessageCategory } from '../../messageClassifier'
import type { NotificationEntry } from '../../model/notification'
import type { ClassificationInput } from '../registry'
import { DROID_NOTIFICATION, DROID_NOTIFICATION_FIELD, DROID_TOOL_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'

/**
 * Factory Droid message classification.
 *
 * The worker persists each notification payload verbatim, so the dispatcher
 * reads the same `type` the CLI wrote. A frame this build does not know reaches
 * the reader as the raw frame rather than disappearing.
 */
export function classifyDroidMessage(input: ClassificationInput): MessageCategory {
  const notification = notificationClassifierFor(input.agentProvider, droidNotificationEntry)
  const wrapper = input.wrapper
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper))
    return notification(wrapper.messages, 'hidden')

  const parent = input.parentObject
  if (!parent)
    return { kind: 'hidden' }

  // A worker-assembled row carries the worker's own `type` field.
  const assembled = pickString(parent, 'type')
  if (assembled === 'assembled') {
    const text = pickString(parent, 'text') ?? ''
    return text ? { kind: 'assistant_text' } : { kind: 'hidden' }
  }

  const type = pickString(parent, DROID_NOTIFICATION_FIELD.Type)
  switch (type) {
    case DROID_NOTIFICATION.CreateMessage: {
      const message = pickObject(parent, DROID_NOTIFICATION_FIELD.Message)
      const role = pickString(message, DROID_NOTIFICATION_FIELD.Role)
      if (role === 'user')
        return { kind: 'user_content' }
      return { kind: 'hidden' }
    }
    case DROID_TOOL_NOTIFICATION.ToolCall:
      return { kind: 'tool_use' }
    case DROID_TOOL_NOTIFICATION.ToolResult:
      return { kind: 'tool_result' }
    case DROID_NOTIFICATION.AgentTurnCompleted:
      return { kind: 'result_divider' }
    case DROID_NOTIFICATION.WorkingStateChanged:
    case DROID_NOTIFICATION.SettingsUpdated:
    case DROID_NOTIFICATION.SessionTitleUpdated:
    case DROID_NOTIFICATION.SessionTokenUsageChanged:
    case DROID_NOTIFICATION.Error:
      return notification([parent], 'notification')
    default:
      return notification([parent], 'hidden')
  }
}

/**
 * The notification entry of one provider notice. The raw payload travels whole,
 * so the row draws the provider's own words.
 */
function droidNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  return [{ kind: 'text', text: JSON.stringify(message) }]
}
