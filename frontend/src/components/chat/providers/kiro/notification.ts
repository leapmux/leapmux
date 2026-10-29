import type { CompactionDetails, NotificationEntry } from '../../model/notification'
import type { ACPNativeNotifications } from '../acp/notification'
import { ACP_UPDATE } from '~/generated/contracts/acp-protocol'
import { KIRO_KIND, KIRO_META } from '~/generated/contracts/kiro-protocol'
import { isObject, pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { kiroMeta } from './protocol'

/**
 * Read one native Kiro row that the worker stores whole: a content-filter
 * refusal chunk, a v1 hook failure or cancellation, and the two summarization
 * ends. Each arrives as the update object of a session/update, with the
 * `_meta.kiro` block the worker read.
 */
export function kiroNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const kiro = kiroMeta(message)
  if (!kiro)
    return []
  const kind = pickString(kiro, KIRO_META.Kind)
  if (message.sessionUpdate === ACP_UPDATE.AgentMessageChunk && isObject(kiro[KIRO_META.Refusal])) {
    return [
      kiroRefusalEntry(kiro[KIRO_META.Refusal]),
    ]
  }
  if (kind === KIRO_KIND.SummarizationCompleted || kind === KIRO_KIND.SummarizationFailed) {
    return [
      kiroCompactionEntry(kind),
    ]
  }
  if (kind === KIRO_KIND.HookUpdate)
    return kiroHookEntries(kiro)
  return []
}

/** One refusal states its category, its explanation, and the model it suggests. */
function kiroRefusalEntry(refusal: unknown): NotificationEntry {
  const record = isObject(refusal)
    ? refusal
    : {}
  const parts: string[] = []
  const category = pickString(record, 'category')
  const explanation = pickString(record, 'explanation').trim()
  if (category)
    parts.push(`(${category})`)
  if (explanation)
    parts.push(explanation)
  const recommended = pickString(record, 'recommendedModel').trim()
  if (recommended)
    parts.push(`Recommended model: ${recommended}`)
  return {
    kind: 'text',
    text: `The model refused this request ${parts.join(' ')}`.trim(),
  }
}

/** Each summarization end is one compaction boundary; a failure keeps its status. */
function kiroCompactionEntry(kind: string): NotificationEntry {
  if (kind === KIRO_KIND.SummarizationFailed)
    return { kind: 'compaction', phase: 'end', outcome: 'failed' }
  return { kind: 'compaction', phase: 'end', outcome: 'completed' }
}

/** A v1 hook failure or cancellation carries its command result. */
function kiroHookEntries(kiro: Record<string, unknown>): NotificationEntry[] {
  const hook = pickObject(kiro, 'hook')
  const status = pickString(hook, 'status')
  if (status !== 'failed' && status !== 'canceled')
    return []
  const result = pickObject(hook, 'result')
  const entry: Extract<NotificationEntry, { kind: 'hook' }> = {
    kind: 'hook',
    outcome: status === 'canceled'
      ? 'blocked'
      : 'failed',
  }
  const name = pickString(hook, 'name').trim()
  if (name)
    entry.name = name
  const output = pickString(result, 'output').trim()
  if (output)
    entry.detail = output
  const exitCode = pickNumber(result, 'exitCode')
  if (exitCode !== null && Number.isSafeInteger(exitCode))
    entry.exitCode = exitCode
  return [entry]
}

/** Every summarization end is a boundary for the context meter. */
export function kiroCompactionBoundary(message: Record<string, unknown>): CompactionDetails | null {
  const kiro = kiroMeta(message)
  const kind = pickString(kiro, KIRO_META.Kind)
  if (kind !== KIRO_KIND.SummarizationCompleted && kind !== KIRO_KIND.SummarizationFailed)
    return null
  return {}
}

export const kiroNativeNotifications: ACPNativeNotifications = {
  isMember: message => kiroNotificationEntry(message).length > 0,
  entries: kiroNotificationEntry,
  compactionBoundary: kiroCompactionBoundary,
}
