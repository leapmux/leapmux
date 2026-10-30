import type { NotificationEntry } from '../../../model/notification'
import type { ACPNativeNotifications } from '../../acp/notification'
import { KILO_EVENT, KILO_STATUS_TYPE } from '~/generated/contracts/kilo-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'

/** Read Kilo native retry and compaction notices. */
export function kiloNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  if (message.type === KILO_EVENT.SessionCompacted) {
    return [
      { kind: 'compaction', phase: 'end' },
    ]
  }
  if (message.type !== KILO_EVENT.SessionStatus)
    return []
  const status = pickObject(pickObject(message, 'properties'), 'status')
  if (pickString(status, 'type') !== KILO_STATUS_TYPE.Retry)
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

export const kiloNativeNotifications: ACPNativeNotifications = {
  isMember: message => kiloNotificationEntry(message).length > 0,
  entries: kiloNotificationEntry,
  compactionBoundary: message => message.type === KILO_EVENT.SessionCompacted
    ? {}
    : null,
}
