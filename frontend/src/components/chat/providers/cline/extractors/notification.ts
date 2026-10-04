import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { CLINE_EVENT, CLINE_NOTICE_KIND, CLINE_NOTICE_PHASE, CLINE_TEAM_RUN_EVENT } from '~/generated/contracts/cline-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { CLINE_FIELD, clineEnvelope, clinePayload } from '../protocol'

/**
 * Cline's two kinds of notice, as the worker persists them.
 *
 *   session.notice  {message, noticeType, reason?, metadata: {kind, phase, ...}, agent}
 *   team.progress   {summary: {teamName, ...}, lastEvent: {eventType, runId, agentId, taskId, message?}}
 *
 * A `session.notice` states a status of the lead's run: a compaction of its context,
 * a retry of a model call that failed, a recovery. Its `metadata.kind` and `phase`
 * say which, and the worker groups the notices of one compaction and of one retry by
 * the same two words. A `team.progress` row states one event of a teammate run.
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
  const pre = pickNumber(metadata, 'tokensBefore')
  const post = pickNumber(metadata, 'tokensAfter')
  return { ...(trigger ? { trigger } : {}), ...(pre !== null ? { pre } : {}), ...(post !== null ? { post } : {}) }
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
      case CLINE_NOTICE_PHASE.Started:
        return [{ kind: 'compaction', phase: 'start', detail: { trigger } }]
      case CLINE_NOTICE_PHASE.Completed:
        return [{ kind: 'compaction', phase: 'end', detail: compactionDetails(metadata, trigger) }]
      case CLINE_NOTICE_PHASE.Skipped:
        return [{ kind: 'status', text: 'Compaction skipped' }]
      case CLINE_NOTICE_PHASE.Failed:
        return [{ kind: 'status', text: 'Compaction failed' }]
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
      return [{
        kind: 'retry',
        scope: 'api',
        ...(attempt !== null ? { attempt } : {}),
        ...(maxAttempts !== null ? { maxAttempts } : {}),
        ...(delayMs !== null ? { delayMs } : {}),
        ...(error ? { error } : {}),
      }]
    }
    case CLINE_NOTICE_KIND.CompactionBudgetEmergency:
      return [{ kind: 'status', text: 'Compaction trimmed the context further to fit the model' }]
    case CLINE_NOTICE_KIND.ContextOverflowRecovery:
      return [{ kind: 'status', text: 'The context exceeded the model\'s window; compacting and retrying' }]
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

/**
 * Read one Cline notification row into the shared notification model.
 *
 * The SOLE notification seam, for a standalone row and for one entry of a consolidated
 * thread alike. Returns an empty list for a row this provider does not own, and for a
 * row of its own that states nothing it can word.
 */
export function clineNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  const envelope = clineEnvelope(message)
  if (!envelope)
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
