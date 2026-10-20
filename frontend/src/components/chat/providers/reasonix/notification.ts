import type { CompactionDetails, NotificationEntry } from '../../model/notification'
import type { ACPNativeNotifications } from '../acp/notification'
import { ACP_UPDATE } from '~/generated/contracts/acp-protocol'
import { REASONIX_NOTICE_PREFIX } from '~/generated/contracts/reasonix-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'

/**
 * Read one stored Reasonix notice chunk. The worker stores a warning or a
 * compaction note whole when its text starts with one of Reasonix's two
 * prefixes, so the reader splits the same way.
 */
export function reasonixNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const text = pickString(pickObject(message, 'content'), 'text')
  if (message.sessionUpdate !== ACP_UPDATE.AgentMessageChunk || text === '')
    return []
  if (text.startsWith(REASONIX_NOTICE_PREFIX.Compacted))
    return [{ kind: 'compaction', phase: 'end', outcome: 'completed' }]
  if (text.startsWith(REASONIX_NOTICE_PREFIX.Warning))
    return [{ kind: 'text', text: text.slice(REASONIX_NOTICE_PREFIX.Warning.length).trim() }]
  return []
}

/** A compaction notice is a boundary for the context meter. */
export function reasonixCompactionBoundary(message: Record<string, unknown>): CompactionDetails | null {
  const text = pickString(pickObject(message, 'content'), 'text')
  return message.sessionUpdate === ACP_UPDATE.AgentMessageChunk && text.startsWith(REASONIX_NOTICE_PREFIX.Compacted)
    ? {}
    : null
}

export const reasonixNativeNotifications: ACPNativeNotifications = {
  isMember: message => reasonixNotificationEntry(message).length > 0,
  entries: reasonixNotificationEntry,
  compactionBoundary: reasonixCompactionBoundary,
}
