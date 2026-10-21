import type { CompactionDetails, NotificationEntry, RetryFacts } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { DROID_NOTIFICATION, DROID_NOTIFICATION_FIELD } from '~/generated/contracts/droid-protocol'
import { assignDefined, isObject, pickBool, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'
import { DROID_LLM_RETRY_REASON_LABEL } from '../protocol'

/** Read one native notice into the shared notification model. */
export function droidNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  switch (pickString(message, DROID_NOTIFICATION_FIELD.Type)) {
    case DROID_NOTIFICATION.SessionCompacted:
      return [{ kind: 'compaction', phase: 'end', outcome: 'completed' }]
    case DROID_NOTIFICATION.Error: {
      const detail = pickString(message, DROID_NOTIFICATION_FIELD.Message)
        || pickString(pickObject(message, 'error'), 'message')
        || pickString(message, 'reason')
      return detail.trim() ? [{ kind: 'text', text: detail.trim() }] : []
    }
    case DROID_NOTIFICATION.LlmRetry: {
      const reason = pickString(message, DROID_NOTIFICATION_FIELD.Reason)
      const facts: RetryFacts = { error: DROID_LLM_RETRY_REASON_LABEL.get(reason) ?? reason }
      assignDefined(facts, 'attempt', pickNumber(message, 'attempt', undefined))
      return [retryEntry('api', facts)]
    }
    case DROID_NOTIFICATION.HookExecutionCompleted:
      return droidHookEntries(message)
    case DROID_NOTIFICATION.McpStatusChanged:
      return droidMcpEntries(message)
    case DROID_NOTIFICATION.QueuedMessagesDiscarded: {
      const text = pickString(message, 'text').trim()
      return [{ kind: 'text', text: text ? `Droid discarded a queued message: ${text}` : 'Droid discarded a queued message' }]
    }
    case DROID_NOTIFICATION.CreateMessage: {
      const native = pickObject(message, DROID_NOTIFICATION_FIELD.Message)
      if (pickString(native, DROID_NOTIFICATION_FIELD.Role) !== 'system' || pickString(native, 'visibility') !== 'user_only')
        return []
      const content = native?.[DROID_NOTIFICATION_FIELD.Content]
      const text = Array.isArray(content)
        ? content.filter(isObject).filter(block => pickString(block, 'type') === 'text').map(block => pickString(block, 'text')).join('\n').trim()
        : ''
      return text ? [{ kind: 'text', text }] : []
    }
    default:
      return []
  }
}

function droidHookEntries(message: Record<string, unknown>): NotificationEntry[] {
  const blocked = pickBool(message, 'hookPreventedAction')
  if (!blocked && pickString(message, 'hookStatus') !== 'error')
    return []
  const results = Array.isArray(message.hookResults) ? message.hookResults.filter(isObject) : []
  const failures = results.filter(result => blocked || pickNumber(result, 'exitCode', undefined) !== 0)
  return (failures.length > 0 ? failures : [{}]).map((result) => {
    const entry: Extract<NotificationEntry, { kind: 'hook' }> = { kind: 'hook', outcome: blocked ? 'blocked' : 'failed' }
    assignDefined(entry, 'event', pickString(message, 'hookEventName').trim() || undefined)
    assignDefined(entry, 'name', pickString(result, 'command').trim() || undefined)
    const exitCode = pickNumber(result, 'exitCode', undefined)
    if (exitCode !== undefined && Number.isSafeInteger(exitCode))
      entry.exitCode = exitCode
    const detail = pickString(result, 'stderr').trim() || pickString(result, 'stdout').trim() || pickString(message, 'error').trim()
    assignDefined(entry, 'detail', detail || undefined)
    return entry
  })
}

function droidMcpEntries(message: Record<string, unknown>): NotificationEntry[] {
  if (!Array.isArray(message.servers))
    return []
  return message.servers.filter(isObject).flatMap((server): NotificationEntry[] => {
    const requiresAuth = pickBool(server, 'requiresAuth')
    if (!requiresAuth && pickString(server, 'status') !== 'failed')
      return []
    const name = pickString(server, 'name').trim() || 'unknown'
    if (requiresAuth)
      return [{ kind: 'text', text: `MCP server ${name} needs authentication` }]
    const error = pickString(server, 'error').trim()
    return [{ kind: 'text', text: error ? `MCP server ${name} failed: ${error}` : `MCP server ${name} failed` }]
  })
}

/** A native compaction boundary may share a stored row with other notices. */
export function droidCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const messages: unknown[] = parsed.wrapper?.messages ?? (parsed.parentObject ? [parsed.parentObject] : [])
  return messages.some(message => isObject(message) && pickString(message, DROID_NOTIFICATION_FIELD.Type) === DROID_NOTIFICATION.SessionCompacted)
    ? {}
    : null
}
