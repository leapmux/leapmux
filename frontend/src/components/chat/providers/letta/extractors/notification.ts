import type { CompactionDetails, NotificationEntry, RetryFacts } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND, LETTA_EVENT_TYPE } from '~/generated/contracts/letta-protocol'
import { assignDefined, isObject, pickCounter, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'
import { retryEntry } from '../../../model/notification'

// Only the browser reads the structured API error fallback.
const LOOP_ERROR_API_ERROR_FIELD = 'api_error'

/**
 * The words that Letta Code states for a loop error.
 *
 * `message` is the notice that Letta Code writes in its own transcript. It is
 * friendly text for a known failure (a lost connection, a service outage) and the
 * indented JSON of the error for a local backend error. `api_error` repeats the
 * service error in a structured form, so it answers when the notice has no words.
 * Both are empty when the delta states neither.
 */
function lettaLoopErrorWords(delta: Record<string, unknown>): string {
  return lettaErrorWords(pickString(delta, LETTA_DELTA_FIELD.Message).trim()
    || pickString(pickObject(delta, LOOP_ERROR_API_ERROR_FIELD), LETTA_DELTA_FIELD.Message))
}

/** Read nested native error messages while preserving an unrecognized error body. */
export function lettaErrorWords(text: string): string {
  const original = text.trim()
  let value: unknown
  try {
    value = JSON.parse(original)
  }
  catch {
    return original
  }
  let words = ''
  while (isObject(value)) {
    words = pickString(value, LETTA_DELTA_FIELD.Message).trim() || words
    value = value[LETTA_DELTA_FIELD.Error]
  }
  return words || original
}

/** Read stored native notices. An unknown notice returns no entry and retains the classifier's diagnostic policy. */
export function lettaNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const kind = pickString(message, LETTA_DELTA_FIELD.MessageType)
  if (kind === LETTA_DELTA_KIND.SummaryMessage)
    return [{ kind: 'compaction', phase: 'end', detail: lettaCompactionDetails(message) }]
  if (kind === LETTA_DELTA_KIND.Status) {
    const text = pickString(message, LETTA_DELTA_FIELD.Message).trim()
    return text ? [{ kind: 'status', text }] : []
  }
  if (kind === LETTA_DELTA_KIND.Retry || (kind === LETTA_DELTA_KIND.EventMessage && message[LETTA_DELTA_FIELD.EventType] === LETTA_EVENT_TYPE.Retry)) {
    const data = kind === LETTA_DELTA_KIND.Retry ? message : pickObject(message, LETTA_DELTA_FIELD.EventData)
    const facts: RetryFacts = {}
    assignDefined(facts, 'attempt', pickNumber(data, 'attempt', undefined))
    assignDefined(facts, 'maxAttempts', pickNumber(data, 'max_attempts', undefined))
    assignDefined(facts, 'delayMs', pickNumber(data, 'delay_ms', undefined))
    assignDefined(facts, 'error', pickString(data, LETTA_DELTA_FIELD.Message))
    return [retryEntry('api', facts)]
  }
  if (pickString(message, LETTA_DELTA_FIELD.MessageType) === LETTA_DELTA_KIND.LoopError) {
    const words = lettaLoopErrorWords(message)
    if (words)
      return [{ kind: 'text', text: words }]
  }
  return []
}

function lettaCompactionDetails(message: Record<string, unknown>): CompactionDetails {
  const stats = pickObject(message, 'compaction_stats')
  const detail: CompactionDetails = {}
  assignDefined(detail, 'trigger', pickString(stats, LETTA_DELTA_FIELD.Trigger).trim() || undefined)
  assignDefined(detail, 'pre', pickCounter(stats, 'context_tokens_before'))
  assignDefined(detail, 'post', pickCounter(stats, 'context_tokens_after'))
  return detail
}

/** Read the native summary boundary for the context meter. */
export function lettaCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const message = getInnerMessage(parsed)
  return isObject(message) && message[LETTA_DELTA_FIELD.MessageType] === LETTA_DELTA_KIND.SummaryMessage
    ? lettaCompactionDetails(message)
    : null
}

/** Identify a stored native start for the shared compaction safety net. */
export function lettaCompactionStart(message: Record<string, unknown>): boolean {
  return message[LETTA_DELTA_FIELD.MessageType] === LETTA_DELTA_KIND.EventMessage && message[LETTA_DELTA_FIELD.EventType] === LETTA_EVENT_TYPE.Compaction
}
