import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_ENTRY_TYPE, PI_EVENT, PI_EXTENSION_METHOD } from '~/generated/contracts/pi-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { retryEntry, tokenCount } from '../../../model/notification'
import { PI_MANUAL_COMPACTION_ERROR_PREFIX, PI_NOTIFY_TYPE, PI_SUMMARY_SOURCE_LABEL } from '../protocol'
import { piSessionEntry } from '../sessionEntry'

/**
 * A readable line for one Pi notification event that states plain TEXT.
 *
 * Compaction and the retries are not here: each of them is a structured entry, so
 * {@link piNotificationEntry} builds it and the shared formatter writes the sentence.
 * A second wording here would be a second source for the same row.
 *
 * Returns null for a shape Pi does not own.
 */
export function describePiNotification(parsed: unknown): string | null {
  if (!isObject(parsed))
    return null
  const type = pickString(parsed, 'type')

  if (type === PI_EVENT.ExtensionError) {
    const ext = pickString(parsed, 'extensionPath')
    const evt = pickString(parsed, 'event')
    const err = pickString(parsed, 'error')
    return `Extension error${ext ? ` in ${ext}` : ''}${evt ? ` (${evt})` : ''}${err ? `: ${err}` : ''}`
  }

  // Pi `extension_ui_request` with `method:"notify"` carries a user-visible message;
  // surface it directly. Another method gets a method-name label, so the reader still
  // sees that an extension fired something.
  if (type === PI_EVENT.ExtensionUIRequest) {
    const method = pickString(parsed, 'method')
    if (method === PI_EXTENSION_METHOD.Notify) {
      const text = pickString(parsed, 'message').trim()
      if (!text)
        return null
      const level = pickString(parsed, 'notifyType')
      const prefix = level === PI_NOTIFY_TYPE.Warning ? 'Warning: ' : level === PI_NOTIFY_TYPE.Error ? 'Error: ' : ''
      return `${prefix}${text}`
    }
    return method ? `Extension UI: ${method}` : 'Extension UI request'
  }

  return null
}

/** Read the optional sizes from a native compaction result or session entry. */
function piCompactionDetail(result: Record<string, unknown>, trigger?: string): CompactionDetails {
  const pre = tokenCount(result.tokensBefore)
  const post = tokenCount(result.estimatedTokensAfter)
  return {
    ...(trigger ? { trigger } : {}),
    ...(pre !== undefined ? { pre } : {}),
    ...(post !== undefined ? { post } : {}),
  }
}

/**
 * The compaction boundary a Pi message states, or null when it states none.
 *
 * An aborted or failed `compaction_end` produced no boundary. A successful
 * native frame carries `result`, even when it has no token count.
 */
export function piCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const inner = getInnerMessage(parsed)
  if (!isObject(inner))
    return null
  const entry = piSessionEntry(inner)
  if (entry?.type === PI_ENTRY_TYPE.Compaction)
    return piCompactionDetail(entry)
  const result = pickObject(inner, 'result')
  if (inner.type !== PI_EVENT.CompactionEnd || inner.aborted === true || !result)
    return null
  return piCompactionDetail(result, pickString(inner, 'reason'))
}

/**
 * Read one Pi notification frame into the shared notification model.
 *
 * Pi's compaction pair becomes a `compaction` entry, so it draws the same rule every
 * other provider's boundary draws. Its two retry families become `retry` entries, so
 * a reader who meets a stall on Pi and one on Claude reads the same sentence.
 */
export function piNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const type = pickString(msg, 'type')

  const entry = piSessionEntry(msg)
  if (entry?.type === PI_ENTRY_TYPE.Compaction)
    return [{ kind: 'compaction', phase: 'end', detail: piCompactionDetail(entry) }]
  if (type === PI_EVENT.CompactionEnd) {
    if (msg.aborted === true)
      return [{ kind: 'compaction', phase: 'end', outcome: 'aborted' }]
    const result = pickObject(msg, 'result')
    if (!result) {
      const rawError = pickString(msg, 'errorMessage')
      const error = (rawError.startsWith(PI_MANUAL_COMPACTION_ERROR_PREFIX) ? rawError.slice(PI_MANUAL_COMPACTION_ERROR_PREFIX.length) : rawError).trim()
      return [{ kind: 'compaction', phase: 'end', outcome: 'failed', ...(error ? { error } : {}) }]
    }
    return [{ kind: 'compaction', phase: 'end', detail: piCompactionDetail(result, pickString(msg, 'reason')) }]
  }

  if (type === PI_EVENT.AutoRetryStart) {
    const attempt = pickNumber(msg, 'attempt', undefined)
    const maxAttempts = pickNumber(msg, 'maxAttempts', undefined)
    const delayMs = pickNumber(msg, 'delayMs', undefined)
    const error = pickString(msg, 'errorMessage') || undefined
    return [retryEntry('api', {
      ...(attempt !== undefined ? { attempt } : {}),
      ...(maxAttempts !== undefined ? { maxAttempts } : {}),
      ...(delayMs !== undefined ? { delayMs } : {}),
      ...(error !== undefined ? { error } : {}),
    })]
  }
  if (type === PI_EVENT.AutoRetryEnd) {
    // A retry that SUCCEEDED ends the stall, and the row says so. One that failed
    // gave up, which is what `willRetry: false` states.
    const attempt = pickNumber(msg, 'attempt', undefined)
    const succeeded = msg.success === true
    const error = succeeded ? undefined : pickString(msg, 'finalError') || undefined
    return [retryEntry('api', {
      ...(attempt !== undefined ? { attempt } : {}),
      ...(succeeded ? {} : { willRetry: false }),
      ...(error !== undefined ? { error } : {}),
      ...(succeeded ? { succeeded: true } : {}),
    })]
  }

  // The three summarization-retry events state the same stall for the SUMMARY that
  // compaction writes, which is why they carry their own scope.
  if (type === PI_EVENT.SummarizationRetryScheduled) {
    const attempt = pickNumber(msg, 'attempt', undefined)
    const maxAttempts = pickNumber(msg, 'maxAttempts', undefined)
    const delayMs = pickNumber(msg, 'delayMs', undefined)
    const error = pickString(msg, 'errorMessage') || undefined
    return [retryEntry('summarization', {
      ...(attempt !== undefined ? { attempt } : {}),
      ...(maxAttempts !== undefined ? { maxAttempts } : {}),
      ...(delayMs !== undefined ? { delayMs } : {}),
      ...(error !== undefined ? { error } : {}),
    })]
  }
  if (type === PI_EVENT.SummarizationRetryAttemptStart) {
    const subject = PI_SUMMARY_SOURCE_LABEL.get(pickString(msg, 'source')) ?? 'summary'
    return [{ kind: 'status', text: `Retrying the ${subject}` }]
  }
  if (type === PI_EVENT.SummarizationRetryFinished)
    return [{ kind: 'status', text: 'Summary retry finished' }]

  const text = describePiNotification(msg)
  return text === null ? [] : [{ kind: 'text', text }]
}

/** Identify an old stored start without changing its ordinary category. */
export function piCompactionStart(message: Record<string, unknown>): boolean {
  return message.type === PI_EVENT.CompactionStart
}
