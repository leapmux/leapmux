import type { CompactionDetails, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import {
  ZCODE_COMPACTION_STATUS,
  ZCODE_DECISION,
  ZCODE_EVENT,
  ZCODE_GOAL_VERIFICATION_STATUS,
  ZCODE_HOOK_RUN_OUTCOME,
  ZCODE_METHOD,
  ZCODE_MODEL_SELECTION_ORIGIN,
  ZCODE_NETWORK_STATUS,
  ZCODE_STATE_REASON,
} from '~/generated/contracts/zcode-protocol'
import { pickCounter, pickObject, pickString } from '~/lib/jsonPick'
import { zcodeIsMainQuery } from '../messageContent'
import { zcodeSessionUpdateKind } from '../sessionUpdateKind'
import { zcodeEnvelope } from './toolCommon'

/**
 * The sentence for one resolved permission, per decision.
 *
 * Only a decision the app-server made BY ITSELF reaches a row -- one the user
 * answered is already recorded as its own answer row.
 *
 * This table lists every decision in the app-server's enumeration, and a decision that is
 * absent or outside it yields null: reporting an unread decision as a denial would
 * state the opposite of an `allow` and would turn an escalation into a refusal that
 * never happened.
 *
 * A Map rather than a plain object, for the reason `zcode/toolKinds.ts` gives: the
 * key is a WIRE word, and a plain object answers `toString` or `constructor` from
 * `Object.prototype` instead of reporting that it holds no entry. This table holds
 * FUNCTIONS, so that answer was then CALLED -- `toString` drew `[object Undefined]`
 * as the whole sentence, and `__proto__` threw and took the message into the
 * ErrorBoundary.
 */
const ZCODE_DECISION_PHRASE: ReadonlyMap<string, (tool: string) => string> = new Map([
  [ZCODE_DECISION.Allow, (tool: string) => `Allowed ${tool} automatically`],
  [ZCODE_DECISION.Deny, (tool: string) => `Denied ${tool}`],
  [ZCODE_DECISION.Escalate, (tool: string) => `Escalated ${tool} for approval`],
  [ZCODE_DECISION.Modify, (tool: string) => `Ran ${tool} with modified input`],
])

/**
 * A human-readable line for one ZCode notification row.
 *
 * Returns null for a shape ZCode does not own, so the shared provider-neutral
 * notification switch (settings_changed, interrupted, ...) can try it instead -- and
 * for a row of ZCode's own whose decision it cannot read, which the plugin then
 * hides rather than surfacing as an empty notification.
 */
export function describeZCodeNotification(parsed: unknown): string | null {
  const envelope = zcodeEnvelope(parsed)
  if (!envelope)
    return null
  const payload = envelope.payload

  switch (envelope.type) {
    case ZCODE_EVENT.PermissionResolved: {
      const phrase = ZCODE_DECISION_PHRASE.get(pickString(payload, 'decision'))
      if (!phrase)
        return null
      const head = phrase(pickString(payload, 'toolName') || 'a tool')
      const reason = pickString(payload, 'reason')
      return reason ? `${head} — ${reason}` : head
    }

    case ZCODE_EVENT.TurnSteerQueued: {
      const preview = pickString(payload, 'inputPreview')
      return preview ? `Queued for the running turn: ${preview}` : 'Queued for the running turn'
    }

    case ZCODE_EVENT.TurnSteerDrained:
      return 'Queued input delivered to the agent'

    case ZCODE_EVENT.SessionClosed: {
      const reason = pickString(payload, 'reason')
      return reason ? `Session closed — ${reason}` : 'Session closed'
    }

    default:
      return null
  }
}

/**
 * The phrase for each reason that ZCode gives for a discarded queued input.
 *
 * The reason is a wire word, so an unknown word stays off the row rather than
 * showing a raw token. A Map rather than a plain object, for the reason that
 * {@link ZCODE_DECISION_PHRASE} gives. The worker persists no `promoted` discard:
 * that input runs as a turn of its own.
 */
const ZCODE_STEER_DISCARD_REASON_PHRASE: ReadonlyMap<string, string> = new Map([
  ['user_removed', 'you removed it'],
  ['session_resumed', 'the session resumed'],
  ['turn_failed', 'the turn failed'],
])

/**
 * The entries for one `session.updated` row, dispatched on the payload's kind.
 *
 * These are the notification rows that the worker persists:
 *
 *   - The final state of a goal check.
 *   - The compaction lifecycle.
 *   - The microcompact boundary.
 *   - The scheduled retry of the main turn.
 *   - The registry fallback.
 *   - A client-visible hook failure.
 *   - The workspace hook admission.
 *   - A discarded queued input.
 *
 * A shape that the reader has no text for answers with no entries, and the
 * classifier hides the row.
 */
function zcodeSessionUpdateEntries(payload: Record<string, unknown>): NotificationEntry[] {
  switch (zcodeSessionUpdateKind(payload)) {
    case 'goalVerification':
      return zcodeGoalVerificationEntries(payload)
    case 'compaction':
      return zcodeCompactionLifecycleEntries(payload)
    case 'microcompact':
      return [{ kind: 'compaction', phase: 'end', micro: true }]
    case 'networkStatus':
      return zcodeNetworkRetryEntries(payload)
    case 'modelSelected': {
      // The registry fallback is the one switch that states its source and its
      // target. The worker persists no other model selection.
      if (pickString(payload, 'origin') !== ZCODE_MODEL_SELECTION_ORIGIN.RegistryFallback)
        return []
      const from = pickString(pickObject(payload, 'previousModelSelection'), 'modelId')
      const to = pickString(pickObject(payload, 'modelSelection'), 'modelId')
      return from && to ? [{ kind: 'text', text: `Switched from ${from} to ${to}` }] : []
    }
    case 'hookRun':
      return zcodeHookRunEntries(payload)
    case 'hookAdmission': {
      const pending = pickCounter(payload, 'pendingCount')
      if (!pending)
        return []
      const hooks = pending === 1 ? 'workspace hook' : 'workspace hooks'
      return [{ kind: 'text', text: `${pending} ${hooks} pending review; disabled for this session` }]
    }
    case 'steerDiscarded': {
      const reason = ZCODE_STEER_DISCARD_REASON_PHRASE.get(pickString(payload, 'reason'))
      return [{ kind: 'text', text: reason ? `Queued input discarded (${reason})` : 'Queued input discarded' }]
    }
    default:
      return []
  }
}

/**
 * The verb for each hook outcome that a user must see. A cancelled hook stopped
 * with its turn, which is not a failure, so it has no verb.
 */
const ZCODE_HOOK_OUTCOME_VERB: ReadonlyMap<string, { verb: string, messageKey: string | null }> = new Map([
  [ZCODE_HOOK_RUN_OUTCOME.Failed, { verb: 'failed', messageKey: 'errorMessage' }],
  [ZCODE_HOOK_RUN_OUTCOME.Blocked, { verb: 'blocked', messageKey: 'blockReason' }],
  // ZCode's message for a timeout is "Hook timed out after {n}ms", which repeats
  // the verb. The row omits it.
  [ZCODE_HOOK_RUN_OUTCOME.TimedOut, { verb: 'timed out', messageKey: null }],
])

/** A client-visible hook run that did not do its work. */
function zcodeHookRunEntries(payload: Record<string, unknown>): NotificationEntry[] {
  if (pickObject(payload, 'descriptor')?.clientVisible !== true)
    return []
  const outcome = ZCODE_HOOK_OUTCOME_VERB.get(pickString(payload, 'outcome'))
  if (!outcome)
    return []
  const event = pickString(payload, 'hookEventName').trim()
  const head = `${event ? `Hook ${event}` : 'A hook'} ${outcome.verb}`
  const message = outcome.messageKey ? pickString(payload, outcome.messageKey).trim() : ''
  return [{ kind: 'text', text: message ? `${head}: ${message}` : head }]
}

/** `head — detail`, or `head` alone when the detail states nothing. */
function withDetail(head: string, detail: string): string {
  return detail ? `${head} — ${detail}` : head
}

/** The text with a full stop at its end, unless it already ends a sentence. */
function asSentence(text: string): string {
  return /[.!?]$/.test(text) ? text : `${text}.`
}

/** The goal check's own row, from its lifecycle state and its verdict. */
function zcodeGoalVerificationEntries(payload: Record<string, unknown>): NotificationEntry[] {
  const status = pickString(payload, 'status')
  if (status !== ZCODE_GOAL_VERIFICATION_STATUS.Completed
    && status !== ZCODE_GOAL_VERIFICATION_STATUS.FailedClosed
    && status !== ZCODE_GOAL_VERIFICATION_STATUS.Cancelled) {
    return []
  }
  // ZCode's own client reads an absent iteration as the first one.
  const iteration = pickCounter(payload, 'goalIteration') ?? 1
  const prefix = `Iteration ${iteration} · `
  const verification = pickObject(payload, 'verification') ?? {}
  const reason = pickString(verification, 'reason').trim()
  switch (status) {
    case ZCODE_GOAL_VERIFICATION_STATUS.Completed: {
      if (verification.passed === true)
        return [{ kind: 'text', text: withDetail(`${prefix}Goal complete, ending task`, reason) }]
      // ZCode continues the goal loop only with a next action. With none, it
      // skips the continuation and the goal stays active with no turn running.
      const nextAction = pickString(verification, 'nextAction').trim()
      if (!nextAction)
        return [{ kind: 'text', text: withDetail(`${prefix}Goal incomplete; the goal stays active`, reason) }]
      const head = withDetail(`${prefix}Goal incomplete, continuing`, reason)
      return [{ kind: 'text', text: `${asSentence(head)} Next: ${nextAction}` }]
    }
    case ZCODE_GOAL_VERIFICATION_STATUS.FailedClosed:
      return [{ kind: 'text', text: withDetail(`${prefix}The goal check could not run, so the goal ends`, reason) }]
    default:
      return [{ kind: 'text', text: `${prefix}Goal check cancelled; the goal is paused` }]
  }
}

/**
 * The size facts of a completed compaction.
 *
 * `truePostCompactTokenCount` is the count that ZCode measured after the rewrite.
 * ZCode's own client prefers it to `postCompactTokenCount`, its estimate.
 */
function zcodeCompactionDetails(payload: Record<string, unknown>): CompactionDetails {
  const detail: CompactionDetails = {}
  const trigger = pickString(payload, 'trigger')
  const pre = pickCounter(payload, 'preCompactTokenCount')
  const post = pickCounter(payload, 'truePostCompactTokenCount') ?? pickCounter(payload, 'postCompactTokenCount')
  if (trigger)
    detail.trigger = trigger
  if (pre !== undefined)
    detail.pre = pre
  if (post !== undefined)
    detail.post = post
  return detail
}

/** The compaction lifecycle: one row for each state of the one rewrite. */
function zcodeCompactionLifecycleEntries(payload: Record<string, unknown>): NotificationEntry[] {
  const status = pickString(payload, 'status')
  switch (status) {
    case ZCODE_COMPACTION_STATUS.Started:
      return [{ kind: 'compaction', phase: 'start' }]
    case ZCODE_COMPACTION_STATUS.Retrying: {
      const attempt = pickCounter(payload, 'attempt')
      const maxAttempts = pickCounter(payload, 'maxAttempts')
      return [{
        kind: 'retry',
        scope: 'summarization',
        ...(attempt !== undefined ? { attempt } : {}),
        ...(maxAttempts !== undefined ? { maxAttempts } : {}),
      }]
    }
    case ZCODE_COMPACTION_STATUS.Skipped:
      return [{ kind: 'text', text: 'Context is up to date; no compression needed' }]
    case ZCODE_COMPACTION_STATUS.Completed:
      return [{ kind: 'compaction', phase: 'end', detail: zcodeCompactionDetails(payload) }]
    case ZCODE_COMPACTION_STATUS.Failed:
      // A failed compaction states why in its `reason`.
      return [{ kind: 'compaction', phase: 'end', error: pickString(payload, 'reason').trim() || 'the provider reported a failure' }]
    case ZCODE_COMPACTION_STATUS.Interrupted:
      return [{ kind: 'compaction', phase: 'end', error: 'aborted' }]
    default:
      return []
  }
}

/**
 * The size facts of a completed compaction row, or null for any other row.
 *
 * The plugin's `compactionBoundaryFromMessage` hook. The context meter reads the
 * size after the rewrite from it, so an automatic compaction, which ends with no
 * reply of its own, moves the meter at once. Every other compaction state, a
 * microcompact and a stored `state.updated` row state no size after the rewrite.
 */
export function zcodeCompactionBoundary(parsed: ParsedMessageContent): CompactionDetails | null {
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.SessionUpdated)
    return null
  const payload = envelope.payload
  if (zcodeSessionUpdateKind(payload) !== 'compaction' || pickString(payload, 'status') !== ZCODE_COMPACTION_STATUS.Completed)
    return null
  return zcodeCompactionDetails(payload)
}

/**
 * The scheduled retry of the main turn, from the api-retry record that the
 * worker normalized. A side query's retry follows the rule of its reply: the
 * model call is the app-server's own, so it is not the user's.
 */
function zcodeNetworkRetryEntries(payload: Record<string, unknown>): NotificationEntry[] {
  if (pickString(payload, 'type') !== ZCODE_NETWORK_STATUS.RetryScheduled || !zcodeIsMainQuery(payload))
    return []
  const apiRetry = pickObject(pickObject(pickObject(payload, '_meta'), 'zcode'), 'apiRetry')
  if (!apiRetry)
    return []
  const attempt = pickCounter(apiRetry, 'attempt')
  const maxAttempts = pickCounter(apiRetry, 'maxRetries')
  const delayMs = pickCounter(apiRetry, 'retryDelayMs')
  return [{
    kind: 'retry',
    scope: 'api',
    ...(attempt !== undefined ? { attempt } : {}),
    ...(maxAttempts !== undefined ? { maxAttempts } : {}),
    ...(delayMs !== undefined ? { delayMs } : {}),
    error: pickString(apiRetry, 'error'),
  }]
}

/**
 * Read one ZCode notification row into the shared notification model.
 *
 * ZCode's SOLE notification seam, for a standalone row and for one entry of a
 * consolidated wrapper alike -- without it a multi-event thread would render only its
 * first message.
 */
export function zcodeNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  if (pickString(msg, 'method') === ZCODE_METHOD.StateUpdated) {
    const reason = pickString(pickObject(msg, 'params'), 'reason')
    switch (reason) {
      case ZCODE_STATE_REASON.CompactStarted:
        return [{ kind: 'compaction', phase: 'start' }]
      case ZCODE_STATE_REASON.SessionCompacted:
        return [{ kind: 'compaction', phase: 'end', detail: {} }]
      case ZCODE_STATE_REASON.SessionCompactFailed:
        return [{ kind: 'compaction', phase: 'end', error: 'the provider reported a failure' }]
      case ZCODE_STATE_REASON.SessionCompactCancelled:
        return [{ kind: 'compaction', phase: 'end', error: 'cancelled' }]
      default:
        return []
    }
  }
  const envelope = zcodeEnvelope(msg)
  if (envelope && envelope.type === ZCODE_EVENT.SessionUpdated)
    return zcodeSessionUpdateEntries(envelope.payload)
  const text = describeZCodeNotification(msg)
  return text === null ? [] : [{ kind: 'text', text }]
}
