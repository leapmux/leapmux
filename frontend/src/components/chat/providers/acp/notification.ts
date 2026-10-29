import type { CompactionDetails, NotificationEntry } from '../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { getInnerMessage } from '~/lib/messageParser'

/** Native frames that a provider stores as notifications. */
export interface ACPNativeNotifications {
  isMember: (message: Record<string, unknown>) => boolean
  entries: (message: Record<string, unknown>) => NotificationEntry[]
  compactionBoundary?: (message: Record<string, unknown>) => CompactionDetails | null
  compactionStart?: (message: Record<string, unknown>) => boolean
}

/** Read one native compaction frame from the parsed transcript envelope. */
export function createACPCompactionBoundaryReader(reader: NonNullable<ACPNativeNotifications['compactionBoundary']>) {
  return (parsed: ParsedMessageContent): CompactionDetails | null => {
    const message = getInnerMessage(parsed)
    return message
      ? reader(message)
      : null
  }
}
