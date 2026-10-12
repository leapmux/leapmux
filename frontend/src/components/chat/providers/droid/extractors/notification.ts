import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { DROID_NOTIFICATION, DROID_NOTIFICATION_FIELD } from '~/generated/contracts/droid-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/** Read one native notice into the shared notification model. */
export function droidNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  switch (pickString(message, DROID_NOTIFICATION_FIELD.Type)) {
    case DROID_NOTIFICATION.SessionCompacted:
      return [{ kind: 'compaction', phase: 'end' }]
    case DROID_NOTIFICATION.Error: {
      const detail = pickString(message, DROID_NOTIFICATION_FIELD.Message)
        || pickString(pickObject(message, 'error'), 'message')
        || pickString(message, 'reason')
      return [{ kind: 'text', text: detail || JSON.stringify(message) }]
    }
    case DROID_NOTIFICATION.WorkingStateChanged:
    case DROID_NOTIFICATION.SettingsUpdated:
    case DROID_NOTIFICATION.SessionTitleUpdated:
    case DROID_NOTIFICATION.SessionTokenUsageChanged:
    case DROID_NOTIFICATION.ToolExecutionPhaseChanged:
    case DROID_NOTIFICATION.AssistantTextDelta:
    case DROID_NOTIFICATION.AssistantTextComplete:
      return []
    default:
      return [{ kind: 'text', text: JSON.stringify(message) }]
  }
}

/** A native compaction boundary may share a stored row with other notices. */
export function droidCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const messages: unknown[] = parsed.wrapper?.messages ?? (parsed.parentObject ? [parsed.parentObject] : [])
  return messages.some(message => isObject(message) && pickString(message, DROID_NOTIFICATION_FIELD.Type) === DROID_NOTIFICATION.SessionCompacted)
    ? {}
    : null
}
