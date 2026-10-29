import type { CompactionDetails, NotificationEntry, RetryFacts } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { DEEPSEEK_HARNESS_EVENT, DEEPSEEK_HARNESS_HOOK_DECISION } from '~/generated/contracts/deepseek-harness-protocol'
import { assignDefined, isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'
import { deepseekHarnessEventData } from '../protocol'

export function deepseekHarnessNotificationEntry(payload: Record<string, unknown>): NotificationEntry[] {
  const data = deepseekHarnessEventData(payload)
  if (!data)
    return []
  switch (payload.type) {
    case DEEPSEEK_HARNESS_EVENT.CompactionEnd: {
      const error = pickString(data, 'error')
      return error
        ? [
            {
              kind: 'compaction',
              phase: 'end',
              outcome: 'failed',
              error,
            },
          ]
        : [
            {
              kind: 'compaction',
              phase: 'end',
              outcome: 'completed',
              detail: {},
            },
          ]
    }
    case DEEPSEEK_HARNESS_EVENT.LlmRetry: {
      const facts: RetryFacts = {
        error: pickString(pickObject(data, 'failure'), 'message'),
      }
      assignDefined(facts, 'attempt', pickNumber(data, 'retry', undefined))
      assignDefined(facts, 'maxAttempts', pickNumber(data, 'maxRetries', undefined))
      assignDefined(facts, 'delayMs', pickNumber(data, 'delayMs', undefined))
      return [
        retryEntry('api', facts),
      ]
    }
    case DEEPSEEK_HARNESS_EVENT.HookResult:
      return hookEntries(data)
    case DEEPSEEK_HARNESS_EVENT.DeliverablesPresented: {
      const files = Array.isArray(data.files)
        ? data.files.filter(isObject).flatMap((file) => {
            const path = pickString(file, 'path')
            const description = pickString(file, 'description')
            return path
              ? [
                  description
                    ? `${path} (${description})`
                    : path,
                ]
              : []
          })
        : []
      return [
        {
          kind: 'text',
          text: files.length
            ? `Presented files:\n${files.join('\n')}`
            : 'Deliverables presented',
        },
      ]
    }
    default:
      return []
  }
}

function hookEntries(data: Record<string, unknown>): NotificationEntry[] {
  const blocked = data.decision === DEEPSEEK_HARNESS_HOOK_DECISION.Block
    || data.decision === DEEPSEEK_HARNESS_HOOK_DECISION.Deny
    || data.decision === DEEPSEEK_HARNESS_HOOK_DECISION.Stop
  const code = pickNumber(data, 'exitCode', undefined)
  const exitCode = code !== undefined && Number.isSafeInteger(code)
    ? code
    : undefined
  const detail = pickString(data, 'stderrSummary').trim()
  if (!blocked && (exitCode === 0 || (exitCode === undefined && !detail)))
    return []
  const entry: Extract<NotificationEntry, { kind: 'hook' }> = {
    kind: 'hook',
    outcome: blocked ? 'blocked' : 'failed',
  }
  assignDefined(entry, 'event', pickString(data, 'point') || undefined)
  assignDefined(entry, 'name', pickString(data, 'handlerId') || undefined)
  assignDefined(entry, 'exitCode', exitCode)
  assignDefined(entry, 'detail', detail || undefined)
  return [entry]
}

export function deepseekHarnessCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const data = deepseekHarnessEventData(parsed.parentObject, DEEPSEEK_HARNESS_EVENT.CompactionEnd)
  return data && data.error === undefined
    ? {}
    : null
}

/** Identify a stored start for the history filter without changing its category. */
export function deepseekHarnessCompactionStart(payload: Record<string, unknown>): boolean {
  return deepseekHarnessEventData(payload, DEEPSEEK_HARNESS_EVENT.CompactionStart) !== undefined
}
