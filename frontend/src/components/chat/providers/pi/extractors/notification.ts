import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT, PI_EXTENSION_METHOD } from '~/generated/contracts/pi-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { toTokenCount } from '../../../model/notification'

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
    if (method === PI_EXTENSION_METHOD.Notify)
      return pickString(parsed, 'message') || null
    return method ? `Extension UI: ${method}` : 'Extension UI request'
  }

  return null
}

/**
 * What one Pi `compaction_end` frame states about the context it rewrote.
 *
 * Pi carries only a pre-compaction size (`result.tokensBefore`) and no post count, so
 * the transition degrades to pre-only -- exactly as the shared formatter renders an
 * unknown post. The `reason` (manual, threshold, overflow) is the trigger.
 */
function piCompactionDetail(m: Record<string, unknown>): CompactionDetails {
  // Each fact rides only when the frame stated it, never as an explicitly undefined key.
  const trigger = pickString(m, 'reason') || undefined
  const pre = toTokenCount(pickNumber(pickObject(m, 'result'), 'tokensBefore') ?? undefined)
  return {
    ...(trigger !== undefined ? { trigger } : {}),
    ...(pre !== undefined ? { pre } : {}),
  }
}

/**
 * The compaction boundary a Pi message states, or null when it states none.
 *
 * An ABORTED `compaction_end` produced no boundary at all, so the context size did
 * not move and the grid must not refresh from it.
 */
export function piCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const inner = getInnerMessage(parsed)
  if (!isObject(inner) || pickString(inner, 'type') !== PI_EVENT.CompactionEnd || inner.aborted === true)
    return null
  return piCompactionDetail(inner)
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

  if (type === PI_EVENT.CompactionStart)
    return [{ kind: 'compaction', phase: 'start' }]
  if (type === PI_EVENT.CompactionEnd) {
    return msg.aborted === true
      ? [{ kind: 'compaction', phase: 'end', error: 'aborted' }]
      : [{ kind: 'compaction', phase: 'end', detail: piCompactionDetail(msg) }]
  }

  if (type === PI_EVENT.AutoRetryStart) {
    const attempt = pickNumber(msg, 'attempt', undefined)
    const maxAttempts = pickNumber(msg, 'maxAttempts', undefined)
    const delayMs = pickNumber(msg, 'delayMs', undefined)
    const error = pickString(msg, 'errorMessage') || undefined
    return [{
      kind: 'retry',
      scope: 'api',
      ...(attempt !== undefined ? { attempt } : {}),
      ...(maxAttempts !== undefined ? { maxAttempts } : {}),
      ...(delayMs !== undefined ? { delayMs } : {}),
      ...(error !== undefined ? { error } : {}),
    }]
  }
  if (type === PI_EVENT.AutoRetryEnd) {
    // A retry that SUCCEEDED ends the stall, and the row says so. One that failed
    // gave up, which is what `willRetry: false` states.
    const attempt = pickNumber(msg, 'attempt', undefined)
    const succeeded = msg.success === true
    const error = succeeded ? undefined : pickString(msg, 'finalError') || undefined
    return [{
      kind: 'retry',
      scope: 'api',
      ...(attempt !== undefined ? { attempt } : {}),
      ...(succeeded ? {} : { willRetry: false }),
      ...(error !== undefined ? { error } : {}),
      ...(succeeded ? { succeeded: true } : {}),
    }]
  }

  // The three summarization-retry events state the same stall for the SUMMARY that
  // compaction writes, which is why they carry their own scope.
  if (type === PI_EVENT.SummarizationRetryScheduled) {
    const attempt = pickNumber(msg, 'attempt', undefined)
    const maxAttempts = pickNumber(msg, 'maxAttempts', undefined)
    const delayMs = pickNumber(msg, 'delayMs', undefined)
    const error = pickString(msg, 'errorMessage') || undefined
    return [{
      kind: 'retry',
      scope: 'summarization',
      ...(attempt !== undefined ? { attempt } : {}),
      ...(maxAttempts !== undefined ? { maxAttempts } : {}),
      ...(delayMs !== undefined ? { delayMs } : {}),
      ...(error !== undefined ? { error } : {}),
    }]
  }
  if (type === PI_EVENT.SummarizationRetryAttemptStart) {
    const source = pickString(msg, 'source')
    return [{ kind: 'status', text: source ? `Retrying the ${source} summary` : 'Retrying the summary' }]
  }
  if (type === PI_EVENT.SummarizationRetryFinished)
    return [{ kind: 'status', text: 'Summary retry finished' }]

  const text = describePiNotification(msg)
  return text === null ? [] : [{ kind: 'text', text }]
}
