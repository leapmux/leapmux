import type { NotificationEntry } from '../model/notification'
import type { ACPNativeNotifications } from './acp/notification'
import { OPENCODE_EVENT, OPENCODE_STATUS_TYPE } from '~/generated/contracts/opencode-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../model/notification'

/** Read native daemon notices shared by OpenCode and Kilo. */
export function openCodeNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  if (message.type === OPENCODE_EVENT.SessionCompacted) {
    return [
      { kind: 'compaction', phase: 'end' },
    ]
  }
  if (message.type !== OPENCODE_EVENT.SessionStatus)
    return []
  const status = pickObject(pickObject(message, 'properties'), 'status')
  if (pickString(status, 'type') !== OPENCODE_STATUS_TYPE.Retry)
    return []
  const attempt = pickNumber(status, 'attempt', undefined)
  return [
    retryEntry('api', {
      ...(attempt !== undefined
        ? { attempt }
        : {}),
      error: pickString(status, 'message'),
    }),
  ]
}

export const openCodeNativeNotifications: ACPNativeNotifications = {
  isMember: message => openCodeNotificationEntry(message).length > 0,
  entries: openCodeNotificationEntry,
  compactionBoundary: message => message.type === OPENCODE_EVENT.SessionCompacted
    ? {}
    : null,
}
