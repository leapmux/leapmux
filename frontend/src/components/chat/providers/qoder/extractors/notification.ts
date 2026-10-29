import type { CompactionDetails, NotificationEntry, RetryFacts } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { QODER_FRAME_KIND, QODER_STATUS, QODER_SYSTEM_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { assignDefined, isObject, pickCounter, pickNumber, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { compactionMetaFromBoundary, retryEntry } from '../../../model/notification'
import { formatDuration } from '../../../rendererUtils'

/** Read Qoder's completed context boundary for the transcript and context grid. */
export function qoderCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const message = getInnerMessage(parsed)
  if (!isObject(message) || message.type !== QODER_FRAME_KIND.System || message.subtype !== QODER_SYSTEM_SUBTYPE.CompactBoundary)
    return null
  return compactionMetaFromBoundary(message)
}

/** Read native notices into the neutral notification model. */
export function qoderNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  if (message.type !== QODER_FRAME_KIND.System)
    return []
  if (message.subtype === QODER_SYSTEM_SUBTYPE.CompactBoundary) {
    return [
      {
        kind: 'compaction',
        phase: 'end',
        detail: compactionMetaFromBoundary(message),
      },
    ]
  }
  if (message.subtype === QODER_SYSTEM_SUBTYPE.ApiRetry) {
    const status = integerCount(message, 'error_status')
    const facts: RetryFacts = {}
    assignDefined(facts, 'attempt', pickNumber(message, 'attempt', undefined))
    assignDefined(facts, 'maxAttempts', pickNumber(message, 'max_retries', undefined))
    assignDefined(facts, 'delayMs', pickNumber(message, 'retry_delay_ms', undefined))
    assignDefined(facts, 'error', pickString(message, 'error').trim() || (status === undefined
      ? undefined
      : String(status)))
    return [
      retryEntry('api', facts),
    ]
  }
  if (message.subtype === QODER_SYSTEM_SUBTYPE.ModelQueueStatus) {
    const count = integerCount(message, 'queue_count')
    const wait = pickCounter(message, 'wait_time_ms')
    return [
      { kind: 'status', text: `Model request queued${count
        ? ` (${count} ahead)`
        : ''}${wait
        ? `, waited ${formatDuration(wait)}`
        : ''}` },
    ]
  }
  if (message.subtype === QODER_SYSTEM_SUBTYPE.HookResponse) {
    if (message.outcome === 'success')
      return []
    const entry: Extract<NotificationEntry, { kind: 'hook' }> = { kind: 'hook', outcome: 'failed' }
    assignDefined(entry, 'event', pickString(message, 'hook_event').trim() || undefined)
    assignDefined(entry, 'name', pickString(message, 'hook_name').trim() || undefined)
    const exitCode = pickNumber(message, 'exit_code', undefined)
    assignDefined(entry, 'exitCode', Number.isSafeInteger(exitCode)
      ? exitCode
      : undefined)
    assignDefined(entry, 'detail', pickString(message, 'stderr').trim() || pickString(message, 'output').trim() || undefined)
    return [entry]
  }
  if (message.subtype === QODER_SYSTEM_SUBTYPE.AgentsMdSizeWarning) {
    const total = integerCount(message, 'total_bytes')
    const threshold = integerCount(message, 'threshold_bytes')
    return [
      {
        kind: 'text',
        text: total !== undefined && threshold !== undefined
          ? `Warning: AGENTS.md files total ${total} bytes, above the ${threshold}-byte threshold`
          : 'Warning: AGENTS.md files exceed the configured size threshold',
      },
    ]
  }
  return []
}

function integerCount(message: Record<string, unknown>, key: string): number | undefined {
  const value = pickCounter(message, key)
  return Number.isSafeInteger(value)
    ? value
    : undefined
}

/** Identify a stored start for the shared compaction safety net. */
export function qoderCompactionStart(message: Record<string, unknown>): boolean {
  return message.type === QODER_FRAME_KIND.System && message.subtype === QODER_SYSTEM_SUBTYPE.Status
    && message.status === QODER_STATUS.Compacting
}
