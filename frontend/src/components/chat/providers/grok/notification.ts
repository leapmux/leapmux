import type { CompactionDetails, NotificationEntry } from '../../model/notification'
import type { ACPNativeNotifications } from '../acp/notification'
import { GROK_METHOD, GROK_NOTIFICATION } from '~/generated/contracts/grok-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry, tokenCount } from '../../model/notification'

// The Worker stores Grok's retry and compaction frames as whole session
// notification lines, so every reader opens the same envelope first.
function grokUpdate(message: Record<string, unknown>): Record<string, unknown> | null {
  if (pickString(message, 'method') !== GROK_METHOD.SessionNotification)
    return null
  return pickObject(pickObject(message, 'params'), 'update')
}

export function grokNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const update = grokUpdate(message)
  if (!update)
    return []
  if (pickString(update, 'sessionUpdate') === GROK_NOTIFICATION.RetryState && pickString(update, 'type') === 'retrying') {
    const attempt = tokenCount(update.attempt)
    const maxAttempts = tokenCount(update.max_retries)
    return [retryEntry('api', {
      ...(attempt !== undefined ? { attempt } : {}),
      ...(maxAttempts !== undefined ? { maxAttempts } : {}),
      ...(pickString(update, 'reason').trim() ? { error: pickString(update, 'reason') } : {}),
    })]
  }
  return grokCompactionEntries(update)
}

function grokCompactionEntries(update: Record<string, unknown>): NotificationEntry[] {
  switch (pickString(update, 'sessionUpdate')) {
    case GROK_NOTIFICATION.AutoCompactCompleted: {
      const detail = grokCompactionDetails(update)
      return [{
        kind: 'compaction',
        phase: 'end',
        outcome: 'completed',
        ...(detail !== null ? { detail } : {}),
      }]
    }
    case GROK_NOTIFICATION.AutoCompactFailed:
      return [{
        kind: 'compaction',
        phase: 'end',
        outcome: 'failed',
        ...(pickString(update, 'error').trim() ? { error: pickString(update, 'error').trim() } : {}),
      }]
    case GROK_NOTIFICATION.AutoCompactCancelled:
      return [{ kind: 'compaction', phase: 'end', outcome: 'aborted' }]
    default:
      return []
  }
}

function grokCompactionDetails(update: Record<string, unknown>): CompactionDetails | null {
  const pre = tokenCount(update.tokens_before)
  const post = tokenCount(update.tokens_after)
  if (pre === undefined && post === undefined)
    return null
  return {
    ...(pre !== undefined ? { pre } : {}),
    ...(post !== undefined ? { post } : {}),
  }
}

/** Every native compaction end is a boundary; a completed one states its counts. */
export function grokCompactionBoundary(message: Record<string, unknown>): CompactionDetails | null {
  const update = grokUpdate(message)
  if (!update)
    return null
  switch (pickString(update, 'sessionUpdate')) {
    case GROK_NOTIFICATION.AutoCompactCompleted:
    case GROK_NOTIFICATION.AutoCompactFailed:
    case GROK_NOTIFICATION.AutoCompactCancelled:
      return grokCompactionDetails(update) ?? {}
    default:
      return null
  }
}

export const grokNativeNotifications: ACPNativeNotifications = {
  isMember: message => grokNotificationEntry(message).length > 0,
  entries: grokNotificationEntry,
  compactionBoundary: grokCompactionBoundary,
}
