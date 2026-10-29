import type { CompactionDetails, CompactionOutcome, HookOutcome, NotificationEntry, RetryFacts } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { MUSE_COMPACTION_OUTCOME, MUSE_INPUT_OUTCOME, MUSE_ITEM_KIND, MUSE_ITEM_STATUS, MUSE_METHOD, MUSE_TODO_STATUS } from '~/generated/contracts/muse-protocol'
import { assignDefined, isObject, pickNumber, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'
import { museItem, museParams } from '../protocol'

const NATIVE_TODO_STATUSES: ReadonlySet<string> = new Set(Object.values(MUSE_TODO_STATUS))

const QUESTION_OUTCOME_TEXT: ReadonlyMap<string, string> = new Map([
  [MUSE_INPUT_OUTCOME.Answered, 'Muse question answered.'],
  [MUSE_INPUT_OUTCOME.Cancelled, 'Muse question cancelled.'],
  [MUSE_INPUT_OUTCOME.Interrupted, 'Muse question interrupted.'],
  [MUSE_INPUT_OUTCOME.Clarified, 'Muse question clarified.'],
  [MUSE_INPUT_OUTCOME.TimedOut, 'Muse question timed out.'],
  [MUSE_INPUT_OUTCOME.Aborted, 'Muse question aborted.'],
])

const COMPACTION_OUTCOMES: ReadonlyMap<string, CompactionOutcome> = new Map([
  [MUSE_COMPACTION_OUTCOME.Noop, 'skipped'],
  [MUSE_COMPACTION_OUTCOME.Failed, 'failed'],
  [MUSE_COMPACTION_OUTCOME.Cancelled, 'aborted'],
])

export function museCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const item = museItem(parsed.parentObject)
  if (item?.kind !== MUSE_ITEM_KIND.Compaction || item.outcome !== MUSE_COMPACTION_OUTCOME.Compacted)
    return null
  // A token count is a whole number of tokens; a fraction or a float beyond the
  // safe range is a corrupt native value, not a count to render.
  const count = (n: number | undefined) => n !== undefined && Number.isSafeInteger(n) && n >= 0
    ? n
    : undefined
  const before = count(pickNumber(item, 'tokensBefore', undefined))
  const after = count(pickNumber(item, 'tokensAfter', undefined))
  return {
    trigger: pickString(item, 'trigger'),
    ...(before !== undefined
      ? { pre: before }
      : {}),
    ...(after !== undefined
      ? { post: after }
      : {}),
  }
}
export function museNotificationEntry(payload: Record<string, unknown>): NotificationEntry[] {
  if (payload.method === MUSE_METHOD.SessionModelRouteUnserved) {
    const params = museParams(payload)
    const provider = pickString(params, 'installedProviderId').trim()
    const model = pickString(params, 'modelId').trim()
    if (!provider || !model)
      return []
    return [
      { kind: 'text', text: `The Muse login installed provider ${provider}, which cannot serve this session's model ${model}. Choose another model.` },
    ]
  }
  if (payload.method === MUSE_METHOD.UserInputSettled) {
    const params = museParams(payload)
    const outcome = pickString(params, 'outcome')
    if (!pickString(params, 'sessionId').trim() || !pickString(params, 'userInputId').trim() || !outcome.trim())
      return []
    return [
      { kind: 'text', text: QUESTION_OUTCOME_TEXT.get(outcome) ?? `Muse question settled: ${outcome}` },
    ]
  }
  if (payload.method === MUSE_METHOD.TodoListChanged) {
    const items = museParams(payload)?.items
    if (!Array.isArray(items))
      return []
    return items.flatMap((item): NotificationEntry[] => {
      if (!isObject(item) || typeof item.status !== 'string' || !item.status.trim() || NATIVE_TODO_STATUSES.has(item.status))
        return []
      return [
        { kind: 'text', text: `Unknown Muse to-do status: ${item.status}` },
      ]
    })
  }
  const item = museItem(payload)
  if (item?.kind === MUSE_ITEM_KIND.HookRun) {
    let outcome: HookOutcome
    switch (item.status) {
      case MUSE_ITEM_STATUS.Failed:
      case MUSE_ITEM_STATUS.Cancelled:
        outcome = 'failed'
        break
      case MUSE_ITEM_STATUS.TimedOut:
        outcome = 'timed-out'
        break
      case MUSE_ITEM_STATUS.Rejected:
        outcome = 'blocked'
        break
      default:
        return []
    }
    const entry: Extract<NotificationEntry, { kind: 'hook' }> = { kind: 'hook', outcome }
    assignDefined(entry, 'event', pickString(item, 'event').trim() || undefined)
    assignDefined(entry, 'name', pickString(item, 'label').trim() || undefined)
    assignDefined(entry, 'detail', pickString(item, 'reason').trim() || (item.status === MUSE_ITEM_STATUS.Cancelled
      ? 'cancelled'
      : undefined))
    return [entry]
  }
  if (item?.kind === MUSE_ITEM_KIND.Compaction) {
    if (item.outcome === MUSE_COMPACTION_OUTCOME.Compacted) {
      return [
        {
          kind: 'compaction',
          phase: 'end',
          detail: {
            trigger: pickString(item, 'trigger'),
          },
        },
      ]
    }
    const reason = pickString(item, 'reason')
    const outcome = pickString(item, 'outcome')
    const knownOutcome = COMPACTION_OUTCOMES.get(outcome)
    if (knownOutcome) {
      const entry: Extract<NotificationEntry, { kind: 'compaction' }> = { kind: 'compaction', phase: 'end', outcome: knownOutcome }
      assignDefined(entry, 'error', reason.trim()
        ? reason
        : undefined)
      return [entry]
    }
    // A future outcome keeps its reason or native word without an invented result.
    if (reason.trim()) {
      return [
        { kind: 'text', text: reason },
      ]
    }
    return outcome.trim()
      ? [
          { kind: 'text', text: `Muse compaction: ${outcome}` },
        ]
      : []
  }
  if (payload.method === MUSE_METHOD.TurnRetryScheduled) {
    const params = museParams(payload)
    const facts: RetryFacts = {
      error: pickString(params, 'reason'),
    }
    assignDefined(facts, 'attempt', pickNumber(params, 'nextAttempt', undefined))
    assignDefined(facts, 'maxAttempts', pickNumber(params, 'maxAttempts', undefined))
    assignDefined(facts, 'delayMs', pickNumber(params, 'retryDelayMs', undefined))
    return [
      retryEntry('api', facts),
    ]
  }
  return []
}
