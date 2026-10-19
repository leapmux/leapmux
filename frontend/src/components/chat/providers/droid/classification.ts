import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { DROID_NOTIFICATION, DROID_NOTIFICATION_FIELD, DROID_TOOL_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { notificationClassifierFor } from '../../notificationClassification'
import { droidNotificationEntry } from './extractors/notification'

/**
 * Factory Droid message classification.
 *
 * The worker stores native payloads for visible notices. Unknown frames stay available through the no-display card.
 */
export function classifyDroidMessage(input: ClassificationInput): MessageCategory {
  const notification = notificationClassifierFor(input.agentProvider, droidNotificationEntry)
  const wrapper = input.wrapper
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (wrapper)
    return notification(wrapper.messages)

  const parent = input.parentObject
  if (!parent)
    return { kind: 'unknown' }

  const type = pickString(parent, DROID_NOTIFICATION_FIELD.Type)
  switch (type) {
    case DROID_NOTIFICATION.CreateMessage: {
      const message = pickObject(parent, DROID_NOTIFICATION_FIELD.Message)
      const role = pickString(message, DROID_NOTIFICATION_FIELD.Role)
      // The worker stores assistant metadata beside its assembled content rows.
      if (role === 'assistant')
        return { kind: 'hidden' }
      break
    }
    case DROID_TOOL_NOTIFICATION.ToolCall:
      return { kind: 'tool_use' }
    case DROID_TOOL_NOTIFICATION.ToolResult:
      return { kind: 'tool_result' }
    case DROID_NOTIFICATION.AgentTurnCompleted:
      return { kind: 'result_divider' }
    default:
      break
  }
  const category = notification([parent])
  return category.kind === 'notification' && category.entries.length > 0 ? category : { kind: 'unknown' }
}
