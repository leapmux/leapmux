import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CLINE_EVENT, CLINE_NOTICE_KIND, CLINE_NOTICE_PHASE, CLINE_TEAM_RUN_EVENT } from '~/generated/contracts/cline-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry, tokenCount } from '../../../model/notification'
import { CLINE_FIELD, clineEnvelope, clinePayload } from '../protocol'

/**
 * Read the two native notice envelopes:
 * - session.notice: {message, metadata: {kind, phase}, agent}.
 * - team.progress: {lastEvent: {eventType, runId, agentId, taskId, message?}}.
 *
 * The worker routes each notice to its owner:
 * - The lead.
 * - An active spawned child.
 * - An identified teammate run.
 * Cline leaves spawned-child notices untagged. The active spawn chain determines their owner.
 * Compaction starts use live state. Stored compaction notices describe only an end.
 */

/** The compaction kinds, and the trigger word each one states. */
const COMPACTION_TRIGGERS: ReadonlyMap<string, string> = new Map([
  [CLINE_NOTICE_KIND.AutoCompaction, 'auto'],
  [CLINE_NOTICE_KIND.ManualCompaction, 'manual'],
  [CLINE_NOTICE_KIND.OverflowRecoveryCompaction, 'overflow'],
])

/** The words of each teammate run event, after the teammate's name. */
const TEAM_RUN_WORDS: ReadonlyMap<string, string> = new Map([
  [CLINE_TEAM_RUN_EVENT.RunQueued, 'queued a run'],
  [CLINE_TEAM_RUN_EVENT.RunStarted, 'started a run'],
  [CLINE_TEAM_RUN_EVENT.RunCompleted, 'completed a run'],
  [CLINE_TEAM_RUN_EVENT.RunFailed, 'failed a run'],
  [CLINE_TEAM_RUN_EVENT.RunCancelled, 'cancelled a run'],
  [CLINE_TEAM_RUN_EVENT.RunInterrupted, 'had a run interrupted'],
])

/** What a finished compaction states about the context it rewrote. */
function compactionDetails(metadata: Record<string, unknown> | null, trigger: string): CompactionDetails {
  const pre = tokenCount(pickNumber(metadata, 'tokensBefore'))
  const post = tokenCount(pickNumber(metadata, 'tokensAfter'))
  return { ...(trigger ? { trigger } : {}), ...(pre !== undefined ? { pre } : {}), ...(post !== undefined ? { post } : {}) }
}

/** The entries of one `session.notice`. */
function noticeEntries(payload: Record<string, unknown>): NotificationEntry[] {
  const metadata = pickObject(payload, CLINE_FIELD.Metadata)
  const kind = pickString(metadata, 'kind')
  const phase = pickString(metadata, 'phase')
  const message = pickString(payload, CLINE_FIELD.Message)
  const trigger = COMPACTION_TRIGGERS.get(kind)
  if (trigger !== undefined) {
    switch (phase) {
      case CLINE_NOTICE_PHASE.Completed:
        return [{ kind: 'compaction', phase: 'end', detail: compactionDetails(metadata, trigger) }]
      case CLINE_NOTICE_PHASE.Skipped:
        return [{ kind: 'compaction', phase: 'end', outcome: 'skipped', detail: { trigger } }]
      case CLINE_NOTICE_PHASE.Failed:
        return [{ kind: 'compaction', phase: 'end', outcome: 'failed', detail: { trigger } }]
      default:
        break
    }
  }
  switch (kind) {
    case CLINE_NOTICE_KIND.ProviderErrorRetry: {
      const attempt = pickNumber(metadata, 'attempt')
      const maxAttempts = pickNumber(metadata, 'maxRetries')
      const delayMs = pickNumber(metadata, 'delayMs')
      const error = pickString(metadata, 'providerError')
      return [retryEntry('api', {
        ...(attempt !== null ? { attempt } : {}),
        ...(maxAttempts !== null ? { maxAttempts } : {}),
        ...(delayMs !== null ? { delayMs } : {}),
        ...(error ? { error } : {}),
      })]
    }
    case CLINE_NOTICE_KIND.CompactionBudgetEmergency:
      return [{ kind: 'status', text: 'Compaction trimmed the context further to fit the model' }]
    case CLINE_NOTICE_KIND.ContextOverflowRecovery:
      return [{ kind: 'status', text: 'The context exceeded the model\'s window; compacting and retrying' }]
    case CLINE_NOTICE_KIND.MaxTokensCompaction:
      return [{ kind: 'status', text: phase === CLINE_NOTICE_PHASE.Started ? 'The answer hit the output limit; compacting and retrying' : message }]
    case CLINE_NOTICE_KIND.MaxTokensRecovery:
      return [{ kind: 'status', text: phase === CLINE_NOTICE_PHASE.Failed ? 'Recovery from the output limit failed' : 'The answer hit the output limit; retrying' }]
    default:
      return message ? [{ kind: 'status', text: message }] : []
  }
}

/** The entries of one `team.progress` row: the teammate and what its run did. */
function teamEntries(payload: Record<string, unknown>): NotificationEntry[] {
  const last = pickObject(payload, 'lastEvent')
  const words = TEAM_RUN_WORDS.get(pickString(last, 'eventType'))
  if (!words)
    return []
  const teammate = pickString(last, 'agentId') || 'A teammate'
  const task = pickString(last, 'taskId')
  const error = pickString(last, CLINE_FIELD.Message)
  const text = `${teammate} ${words}${task ? ` (${task})` : ''}${error ? `: ${error}` : ''}`
  return [{ kind: 'text', text }]
}

/** Read a standalone native notice or one member of a notification thread. */
export function clineNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const envelope = clineEnvelope(message)
  if (!envelope || clineCompactionStart(message))
    return []
  switch (envelope.event) {
    case CLINE_EVENT.SessionNotice:
      return noticeEntries(envelope.payload)
    case CLINE_EVENT.TeamProgress:
      return teamEntries(envelope.payload)
    default:
      return []
  }
}

/** Whether one row is a Cline notice. */
export function clineIsNotice(message: unknown): boolean {
  const envelope = clineEnvelope(message)
  return envelope !== null && (envelope.event === CLINE_EVENT.SessionNotice || envelope.event === CLINE_EVENT.TeamProgress)
}

/**
 * The compaction boundary one row states, or null for any other row.
 *
 * The context-usage grid reads it the moment the boundary lands, so the gauge drops to
 * the compacted size before the next usage report arrives.
 */
export function clineCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const payload = clinePayload(parsed.parentObject, CLINE_EVENT.SessionNotice)
  const metadata = pickObject(payload, CLINE_FIELD.Metadata)
  const trigger = COMPACTION_TRIGGERS.get(pickString(metadata, 'kind'))
  if (trigger === undefined || pickString(metadata, 'phase') !== CLINE_NOTICE_PHASE.Completed)
    return null
  return compactionDetails(metadata, trigger)
}

/** Identify an older stored start for the shared compaction safety net. */
export function clineCompactionStart(message: unknown): boolean {
  const payload = clinePayload(message, CLINE_EVENT.SessionNotice)
  const metadata = pickObject(payload, CLINE_FIELD.Metadata)
  return COMPACTION_TRIGGERS.has(pickString(metadata, 'kind'))
    && pickString(metadata, 'phase') === CLINE_NOTICE_PHASE.Started
}
