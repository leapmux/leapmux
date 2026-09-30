import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { QODER_FRAME_KIND, QODER_SYSTEM_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { isObject } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { compactionMetaFromBoundary } from '../../../model/notification'

/** Read Qoder's completed context boundary for the transcript and context grid. */
export function qoderCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const message = getInnerMessage(parsed)
  if (!isObject(message) || message.type !== QODER_FRAME_KIND.System || message.subtype !== QODER_SYSTEM_SUBTYPE.CompactBoundary)
    return null
  return compactionMetaFromBoundary(message)
}

/** Read the native compaction status and boundary into notification entries. */
export function qoderNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  if (message.type !== QODER_FRAME_KIND.System)
    return []
  if (message.subtype === QODER_SYSTEM_SUBTYPE.CompactBoundary)
    return [{ kind: 'compaction', phase: 'end', detail: compactionMetaFromBoundary(message) }]
  if (message.subtype === QODER_SYSTEM_SUBTYPE.Status && message.status === 'compacting')
    return [{ kind: 'compaction', phase: 'start' }]
  return []
}
