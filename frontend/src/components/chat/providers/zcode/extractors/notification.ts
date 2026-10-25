import type { CompactionDetails, HookOutcome, NotificationEntry } from '../../../model/notification'
import type { ParsedMessageContent } from '~/lib/messageParser'
import {
  ZCODE_COMPACTION_STATUS,
  ZCODE_DECISION,
  ZCODE_EVENT,
  ZCODE_GOAL_VERIFICATION_STATUS,
  ZCODE_HOOK_RUN_OUTCOME,
  ZCODE_NETWORK_STATUS,
} from '~/generated/contracts/zcode-protocol'
import { pickCounter, pickObject, pickString } from '~/lib/jsonPick'
import { retryEntry } from '../../../model/notification'
import { zcodeSessionUpdateKind } from '../sessionUpdateKind'
import { zcodeEnvelope } from './toolCommon'

/**
 * Map the automatic decisions that the worker stores to their text.
 * A user answer already has its own response row. The worker drops automatic allows.
 * An unknown decision has no entry. It must not become a denial by default.
 * The map does not inherit object properties, so a wire value cannot select Object.prototype methods.
 */
const ZCODE_DECISION_PHRASE: ReadonlyMap<string, (tool: string) => string> = new Map([
  [ZCODE_DECISION.Deny, (tool: string) => `Denied ${tool}`],
  [ZCODE_DECISION.Escalate, (tool: string) => `Escalated ${tool} for approval`],
  [ZCODE_DECISION.Modify, (tool: string) => `Ran ${tool} with modified input`],
])

/**
 * Describe one native notification.
 * Return null when this reader has no description. The shared reader can then check worker notifications.
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
 * Read the native notification shapes that the worker stores:
 * - Goal check results.
 * - Compaction ends and microcompact boundaries.
 * - API retries.
 * - Registry model fallbacks.
 * - Hook failures.
 * - Discarded queued input.
 * A shape with no readable facts returns no entries.
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
      const from = pickString(pickObject(payload, 'previousModelSelection'), 'modelId')
      const to = pickString(pickObject(payload, 'modelSelection'), 'modelId')
      return from && to ? [{ kind: 'text', text: `Switched from ${from} to ${to}` }] : []
    }
    case 'hookRun':
      return zcodeHookRunEntries(payload)
    case 'steerDiscarded': {
      const reason = ZCODE_STEER_DISCARD_REASON_PHRASE.get(pickString(payload, 'reason'))
      return [{ kind: 'text', text: reason ? `Queued input discarded (${reason})` : 'Queued input discarded' }]
    }
    default:
      return []
  }
}

/**
 * Map native hook outcomes to neutral outcomes and their detail fields.
 * A cancelled hook stops with its turn and supplies no failure notice.
 */
const ZCODE_HOOK_OUTCOMES: ReadonlyMap<string, { outcome: HookOutcome, messageKey: string | null }> = new Map([
  [ZCODE_HOOK_RUN_OUTCOME.Failed, { outcome: 'failed', messageKey: 'errorMessage' }],
  [ZCODE_HOOK_RUN_OUTCOME.Blocked, { outcome: 'blocked', messageKey: 'blockReason' }],
  // ZCode's message for a timeout is "Hook timed out after {n}ms", which repeats
  // the verb. The row omits it.
  [ZCODE_HOOK_RUN_OUTCOME.TimedOut, { outcome: 'timed-out', messageKey: null }],
])

/**
 * Read a stored hook failure. The worker already checked its visibility.
 */
function zcodeHookRunEntries(payload: Record<string, unknown>): NotificationEntry[] {
  const outcome = ZCODE_HOOK_OUTCOMES.get(pickString(payload, 'outcome'))
  if (!outcome)
    return []
  const event = pickString(payload, 'hookEventName').trim()
  const message = outcome.messageKey ? pickString(payload, outcome.messageKey).trim() : ''
  return [{ kind: 'hook', outcome: outcome.outcome, ...(event ? { event } : {}), ...(message ? { detail: message } : {}) }]
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

/**
 * Read a stored compaction end and its outcome.
 */
function zcodeCompactionLifecycleEntries(payload: Record<string, unknown>): NotificationEntry[] {
  const status = pickString(payload, 'status')
  switch (status) {
    case ZCODE_COMPACTION_STATUS.Skipped:
      return [{ kind: 'compaction', phase: 'end', outcome: 'skipped' }]
    case ZCODE_COMPACTION_STATUS.Completed:
      return [{ kind: 'compaction', phase: 'end', detail: zcodeCompactionDetails(payload) }]
    case ZCODE_COMPACTION_STATUS.Failed:
      // A failed compaction states why in its `reason`.
      return [{ kind: 'compaction', phase: 'end', outcome: 'failed', error: pickString(payload, 'reason').trim() || 'the provider reported a failure' }]
    case ZCODE_COMPACTION_STATUS.Interrupted:
      return [{ kind: 'compaction', phase: 'end', outcome: 'aborted' }]
    default:
      return []
  }
}

/**
 * Read the sizes from a successful compaction end for the context meter.
 * The measured post-compaction count takes precedence over the estimate.
 * Other outcomes and microcompactions supply no size after the compaction.
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
 * Read the native apiRetry record. The worker stores only main-request retries.
 */
function zcodeNetworkRetryEntries(payload: Record<string, unknown>): NotificationEntry[] {
  if (pickString(payload, 'type') !== ZCODE_NETWORK_STATUS.RetryScheduled)
    return []
  const apiRetry = pickObject(pickObject(pickObject(payload, '_meta'), 'zcode'), 'apiRetry')
  if (!apiRetry)
    return []
  const attempt = pickCounter(apiRetry, 'attempt')
  const maxAttempts = pickCounter(apiRetry, 'maxRetries')
  const delayMs = pickCounter(apiRetry, 'retryDelayMs')
  return [retryEntry('api', {
    ...(attempt !== undefined ? { attempt } : {}),
    ...(maxAttempts !== undefined ? { maxAttempts } : {}),
    ...(delayMs !== undefined ? { delayMs } : {}),
    error: pickString(apiRetry, 'error'),
  })]
}

/**
 * Read one standalone notification or one member of a notification thread.
 */
export function zcodeNotificationEntry(msg: Record<string, unknown>): NotificationEntry[] {
  const envelope = zcodeEnvelope(msg)
  if (envelope && envelope.type === ZCODE_EVENT.SessionUpdated)
    return zcodeSessionUpdateEntries(envelope.payload)
  const text = describeZCodeNotification(msg)
  return text === null ? [] : [{ kind: 'text', text }]
}

/** Identify an old stored start without restoring its notification reader. */
export function zcodeCompactionStart(message: Record<string, unknown>): boolean {
  const envelope = zcodeEnvelope(message)
  if (envelope?.type !== ZCODE_EVENT.SessionUpdated || zcodeSessionUpdateKind(envelope.payload) !== 'compaction')
    return false
  return envelope.payload.status === ZCODE_COMPACTION_STATUS.Started || envelope.payload.status === ZCODE_COMPACTION_STATUS.Retrying
}
