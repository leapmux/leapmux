import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { ZCODE_EVENT, ZCODE_RECORD } from '~/generated/contracts/zcode-protocol'
import { assertNever } from '~/lib/assertNever'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { zcodeNotificationEntry } from './extractors/notification'
import { zcodePlanText } from './extractors/plan'
import { zcodeRecoveryEvidence } from './extractors/recoveryEvidence'
import { zcodeEnvelope, zcodeToolSpanRole } from './extractors/toolCommon'
import { zcodeAssistantText } from './messageContent'
import { ZCODE_SESSION_UPDATE_ROWS, zcodeSessionUpdateKind } from './sessionUpdateKind'

/**
 * The event types that thread into chat as notifications.
 *
 * They are also the NON-PROGRESS set: each one is visible but says nothing about the
 * agent working, so the chat-level thinking heuristic must keep scanning past them.
 * Both uses read this one set, so they cannot drift.
 */
const ZCODE_NOTIFICATION_TYPES = new Set<string>([
  ZCODE_RECORD.BackgroundTurnEnd,
  ZCODE_EVENT.PermissionResolved,
  ZCODE_EVENT.TurnSteerQueued,
  ZCODE_EVENT.TurnSteerDrained,
  ZCODE_EVENT.SessionClosed,
])

/** ZCode message classification. */
export function classifyZCodeMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, zcodeNotificationEntry)

  // The empty-wrapper check runs FIRST so the type guard below stays the only
  // narrowing on `wrapper`: it narrows the false branch to null, which would make a
  // later `wrapper.messages` read need a cast to compile.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  // Each stored notification supplies its own entries within a thread.
  // Model responses use ordinary message rows and do not join these threads.
  if (wrapper && wrapper.messages.some((message) => {
    if (!isObject(message))
      return false
    const envelope = zcodeEnvelope(message)
    return envelope?.type === ZCODE_EVENT.SessionUpdated
      && ZCODE_SESSION_UPDATE_ROWS[zcodeSessionUpdateKind(envelope.payload)] === 'notification'
      && (zcodeSessionUpdateKind(envelope.payload) !== 'compaction' || zcodeNotificationEntry(message).length > 0)
  })) {
    return notification(wrapper.messages, 'hidden')
  }
  if (isNotificationThreadWrapper(wrapper, ZCODE_NOTIFICATION_TYPES)) {
    // A thread of only unrenderable notifications collapses to hidden rather than
    // falling through to a raw-JSON bubble.
    return notification(wrapper.messages, 'hidden')
  }

  if (!parent)
    return { kind: 'unknown' }
  // The classifier and extractor both read plans through zcodePlanText.
  // It reads a stored approval row or an ExitPlanMode call that carries a plan.
  if (zcodePlanText(parent, input.spanType, input.supplementalContent) !== null)
    return { kind: 'assistant_plan' }

  // A user row the service layer persisted is the LeapMux-neutral `{content}`
  // shape, with no ZCode `type`. It is matched BEFORE the event dispatch so a user
  // echo does not fall through to the unknown fallback and get JSON-stringified
  // into the bubble.
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  if (type === ZCODE_EVENT.TurnCompleted || type === ZCODE_EVENT.TurnFailed || zcodeRecoveryEvidence(parent))
    return { kind: 'result_divider' }

  if (type === ZCODE_EVENT.ToolUpdated) {
    const payload = pickObject(parent, 'payload') ?? {}
    const role = zcodeToolSpanRole(pickString(payload, 'kind'), input)
    if (role === 'result') {
      return { kind: 'tool_result' }
    }
    if (role === 'request') {
      return { kind: 'tool_use' }
    }
    // The Worker consumes `started` and `progress` for live counters. One
    // reaching a transcript means a provider changed its protocol.
    return { kind: 'unknown' }
  }

  if (type === ZCODE_EVENT.SessionUpdated) {
    const payload = pickObject(parent, 'payload') ?? {}
    const kind = zcodeSessionUpdateKind(payload)
    if (kind === 'compaction' && zcodeNotificationEntry(parent).length === 0)
      return { kind: 'unknown' }
    const row = ZCODE_SESSION_UPDATE_ROWS[kind]
    switch (row) {
      // A notification row goes through the notification reader, which words
      // each shape: the goal check verdict, the compaction lifecycle, the
      // microcompact boundary, the retry, the registry fallback, the hook run,
      // the workspace hook admission and the discarded queued input. An entry
      // that the reader cannot word stays hidden rather than surfacing as raw
      // JSON.
      case 'notification':
        return notification([parent], 'hidden')
      case 'reply':
        return zcodeAssistantText(parent).trim()
          ? { kind: 'assistant_text' }
          : { kind: 'hidden' }
      case 'unknown':
        return { kind: 'unknown' }
      default:
        return assertNever(row)
    }
  }

  if (ZCODE_NOTIFICATION_TYPES.has(type)) {
    return notification([parent], 'hidden')
  }

  // A row in LeapMux's own envelope carries no ZCode event, so the dispatch above
  // cannot match it. The stop row is the one this provider writes: `session/stop`
  // is answered with an empty object and sometimes with no turn frame at all, and
  // the worker states that stop itself. See persistZCodeStopRow.
  if (isPlainNotificationType(type))
    return notification([parent])

  return { kind: 'unknown' }
}
