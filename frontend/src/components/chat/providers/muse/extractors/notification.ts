import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { MUSE_COMPACTION_OUTCOME, MUSE_INPUT_OUTCOME, MUSE_ITEM_KIND, MUSE_METHOD, MUSE_TODO_STATUS } from '~/generated/contracts/muse-protocol'
import { isObject, pickNumber, pickString } from '~/lib/jsonPick'
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

const COMPACTION_OUTCOME_TEXT: ReadonlyMap<string, string> = new Map([
  [MUSE_COMPACTION_OUTCOME.Noop, 'Muse compaction did not change the context.'],
  [MUSE_COMPACTION_OUTCOME.Failed, 'Muse compaction failed.'],
  [MUSE_COMPACTION_OUTCOME.Cancelled, 'Muse compaction cancelled.'],
])

export function museCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const item = museItem(parsed.parentObject)
  if (item?.kind !== MUSE_ITEM_KIND.Compaction || item.outcome !== MUSE_COMPACTION_OUTCOME.Compacted)
    return null
  const pre = pickNumber(item, 'tokensBefore', undefined)
  const post = pickNumber(item, 'tokensAfter', undefined)
  return { trigger: pickString(item, 'trigger'), ...(pre !== undefined && pre >= 0 ? { pre } : {}), ...(post !== undefined && post >= 0 ? { post } : {}) }
}
export function museNotificationEntry(payload: Record<string, unknown>): NotificationEntry[] {
  if (payload.method === MUSE_METHOD.UserInputSettled) {
    const params = museParams(payload)
    const outcome = pickString(params, 'outcome')
    if (!pickString(params, 'sessionId').trim() || !pickString(params, 'userInputId').trim() || !outcome.trim())
      return []
    return [{ kind: 'text', text: QUESTION_OUTCOME_TEXT.get(outcome) ?? `Muse question settled: ${outcome}` }]
  }
  if (payload.method === MUSE_METHOD.TodoListChanged) {
    const items = museParams(payload)?.items
    if (!Array.isArray(items))
      return []
    return items.flatMap((item): NotificationEntry[] => {
      if (!isObject(item) || typeof item.status !== 'string' || item.status === '' || NATIVE_TODO_STATUSES.has(item.status))
        return []
      return [{ kind: 'text', text: `Unknown Muse to-do status: ${item.status}` }]
    })
  }
  const item = museItem(payload)
  if (item?.kind === MUSE_ITEM_KIND.Compaction) {
    if (item.outcome === MUSE_COMPACTION_OUTCOME.Compacted)
      return [{ kind: 'compaction', phase: 'end', detail: { trigger: pickString(item, 'trigger') } }]
    return [{ kind: 'text', text: pickString(item, 'reason') || COMPACTION_OUTCOME_TEXT.get(pickString(item, 'outcome')) || `Muse compaction: ${pickString(item, 'outcome')}` }]
  }
  if (payload.method === MUSE_METHOD.TurnRetryScheduled) {
    const params = museParams(payload)
    const attempt = pickNumber(params, 'attempt', undefined)
    const delayMs = pickNumber(params, 'delayMs', undefined)
    return [{ kind: 'retry', scope: 'api', error: pickString(params, 'message'), ...(attempt !== undefined ? { attempt } : {}), ...(delayMs !== undefined ? { delayMs } : {}) }]
  }
  return []
}
