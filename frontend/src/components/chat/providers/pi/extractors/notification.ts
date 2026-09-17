import type { CompactionBoundaryMeta, NotificationEntryIR } from '../../../ir/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { PI_EVENT, PI_EXTENSION_METHOD } from '~/generated/contracts/pi-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { toTokenCount } from '../../../ir/notification'

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
function piCompactionDetail(m: Record<string, unknown>): CompactionBoundaryMeta {
  return {
    trigger: pickString(m, 'reason') || undefined,
    pre: toTokenCount(pickNumber(pickObject(m, 'result'), 'tokensBefore') ?? undefined),
  }
}

/**
 * The compaction boundary a Pi message states, or null when it states none.
 *
 * An ABORTED `compaction_end` produced no boundary at all, so the context size did
 * not move and the grid must not refresh from it.
 */
export function piCompactionBoundary(parsed: ParsedMessageContent): CompactionBoundaryMeta | null {
  const inner = getInnerMessage(parsed)
  if (!isObject(inner) || pickString(inner, 'type') !== PI_EVENT.CompactionEnd || inner.aborted === true)
    return null
  return piCompactionDetail(inner)
}

/**
 * Read one Pi notification frame into the shared notification IR.
 *
 * Pi's compaction pair becomes a `compaction` entry, so it draws the same rule every
 * other provider's boundary draws. Its two retry families become `retry` entries, so
 * a reader who meets a stall on Pi and one on Claude reads the same sentence.
 */
export function piNotificationEntry(msg: Record<string, unknown>): NotificationEntryIR[] {
  const type = pickString(msg, 'type')

  if (type === PI_EVENT.CompactionStart)
    return [{ kind: 'compaction', phase: 'start' }]
  if (type === PI_EVENT.CompactionEnd) {
    return msg.aborted === true
      ? [{ kind: 'compaction', phase: 'end', error: 'aborted' }]
      : [{ kind: 'compaction', phase: 'end', detail: piCompactionDetail(msg) }]
  }

  if (type === PI_EVENT.AutoRetryStart) {
    return [{
      kind: 'retry',
      scope: 'api',
      attempt: pickNumber(msg, 'attempt') ?? undefined,
      maxAttempts: pickNumber(msg, 'maxAttempts') ?? undefined,
      delayMs: pickNumber(msg, 'delayMs') ?? undefined,
      error: pickString(msg, 'errorMessage') || undefined,
    }]
  }
  if (type === PI_EVENT.AutoRetryEnd) {
    // A retry that SUCCEEDED ends the stall, and the row says so. One that failed
    // gave up, which is what `willRetry: false` states.
    return [{
      kind: 'retry',
      scope: 'api',
      attempt: pickNumber(msg, 'attempt') ?? undefined,
      willRetry: msg.success === true ? undefined : false,
      error: msg.success === true ? undefined : pickString(msg, 'finalError') || undefined,
      succeeded: msg.success === true || undefined,
    }]
  }

  // The three summarization-retry events state the same stall for the SUMMARY that
  // compaction writes, which is why they carry their own scope.
  if (type === PI_EVENT.SummarizationRetryScheduled) {
    return [{
      kind: 'retry',
      scope: 'summarization',
      attempt: pickNumber(msg, 'attempt') ?? undefined,
      maxAttempts: pickNumber(msg, 'maxAttempts') ?? undefined,
      delayMs: pickNumber(msg, 'delayMs') ?? undefined,
      error: pickString(msg, 'errorMessage') || undefined,
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
