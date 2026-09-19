import type { MessageCategory } from '../../messageClassification'
import type { ClassificationInput } from '../registry'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { describeZCodeNotification } from './extractors/notification'
import { zcodePlanText } from './extractors/plan'
import { zcodeEnvelope, zcodeToolSpanRole } from './extractors/toolCommon'
import { zcodeAssistantText, zcodeIsBackgroundTask, zcodeIsModelResponse } from './messageContent'

/**
 * The event types that thread into chat as notifications.
 *
 * They are also the NON-PROGRESS set: each one is visible but says nothing about the
 * agent working, so the chat-level thinking heuristic must keep scanning past them.
 * Both uses read this one set, so they cannot drift.
 */
const ZCODE_NOTIFICATION_TYPES = new Set<string>([
  ZCODE_EVENT.PermissionResolved,
  ZCODE_EVENT.TurnSteerQueued,
  ZCODE_EVENT.TurnSteerDrained,
  ZCODE_EVENT.SessionClosed,
])

/**
 * Event types that carry no UI surface of their own.
 *
 * Each is hidden for a stated reason, and the reasons differ -- which is why this is
 * a list of deliberate decisions rather than a default branch:
 *
 *   - The session lifecycle pair repeats the state the create/resume RPC returned.
 *   - A model-written title would fight LeapMux's own naming.
 *   - The message/part projection is the desktop application's rendering model, and
 *     it repeats the text the model-response `session.updated` already carries.
 *   - The permission/userInput ANNOUNCEMENTS duplicate the interaction requests,
 *     which are the actionable copies and reach the control surface instead.
 *   - Checkpoint and rewind belong to an undo model LeapMux does not expose.
 *   - `turn.started` and `streamRecovery.updated` are worker-side bookkeeping.
 */
const ZCODE_HIDDEN_TYPES = new Set<string>([
  ZCODE_EVENT.SessionCreated,
  ZCODE_EVENT.SessionResumed,
  ZCODE_EVENT.SessionTitleUpdated,
  ZCODE_EVENT.TurnStarted,
  ZCODE_EVENT.MessageUpserted,
  ZCODE_EVENT.MessageRemoved,
  ZCODE_EVENT.PartStarted,
  ZCODE_EVENT.PartDelta,
  ZCODE_EVENT.PartUpserted,
  ZCODE_EVENT.PartRemoved,
  ZCODE_EVENT.ModelStreaming,
  ZCODE_EVENT.PermissionRequested,
  ZCODE_EVENT.UserInputRequested,
  ZCODE_EVENT.UserInputResolved,
  ZCODE_EVENT.CheckpointCreated,
  ZCODE_EVENT.RewindTriggered,
  ZCODE_EVENT.StreamRecoveryUpdated,
])

/**
 * A ZCode notification row with nothing to render.
 *
 * The only surface that can produce no line is a `permission.resolved` the describer
 * does not recognize. Applied by the standalone classifier AND the consolidated-thread
 * filter, so such a row is hidden either way instead of surfacing as raw JSON.
 */
function isHiddenZCodeNotification(msg: unknown): boolean {
  const envelope = zcodeEnvelope(msg)
  if (!envelope || !ZCODE_NOTIFICATION_TYPES.has(envelope.type))
    return false
  return describeZCodeNotification(msg) === null
}

/** ZCode message classification. */
export function classifyZCodeMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper

  // The empty-wrapper check runs FIRST so the type guard below stays the only
  // narrowing on `wrapper`: it narrows the false branch to null, which would make a
  // later `wrapper.messages` read need a cast to compile.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper, ZCODE_NOTIFICATION_TYPES)) {
    // A thread of only unrenderable notifications collapses to hidden rather than
    // falling through to a raw-JSON bubble.
    const messages = wrapper.messages.filter(m => !isHiddenZCodeNotification(m))
    if (messages.length === 0)
      return { kind: 'hidden' }
    return { kind: 'notification', messages }
  }

  if (!parent)
    return { kind: 'unknown' }
  // A proposed plan leaves the tool path, and BOTH layers read it through the same
  // function -- see `zcodePlanText`. It answers for the saved approval row AND for
  // an `ExitPlanMode` call that carries a plan in its arguments.
  if (zcodePlanText(parent, input.spanType, input.supplementalContent) !== null)
    return { kind: 'assistant_plan' }

  // A user row the service layer persisted is the LeapMux-neutral `{content}`
  // shape, with no ZCode `type`. It is matched BEFORE the event dispatch so a user
  // echo does not fall through to the unknown fallback and get JSON-stringified
  // into the bubble.
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  if (type === ZCODE_EVENT.TurnCompleted || type === ZCODE_EVENT.TurnFailed)
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
    return { kind: 'hidden' }
  }

  if (type === ZCODE_EVENT.SessionUpdated) {
    const payload = pickObject(parent, 'payload') ?? {}
    // The catch-all event. A background-task update belongs to the registry, and
    // the request-telemetry variants carry no conversation, so only a model
    // response with text becomes a bubble.
    if (zcodeIsBackgroundTask(payload) || !zcodeIsModelResponse(payload))
      return { kind: 'hidden' }
    return zcodeAssistantText(parent).trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
  }

  if (ZCODE_NOTIFICATION_TYPES.has(type)) {
    if (isHiddenZCodeNotification(parent))
      return { kind: 'hidden' }
    return { kind: 'notification', messages: [parent] }
  }

  // A row in LeapMux's own envelope carries no ZCode event, so the dispatch above
  // cannot match it. The stop row is the one this provider writes: `session/stop`
  // is answered with an empty object and sometimes with no turn frame at all, and
  // the worker states that stop itself. See persistZCodeStopRow.
  if (isPlainNotificationType(type))
    return { kind: 'notification', messages: [parent] }

  if (ZCODE_HIDDEN_TYPES.has(type))
    return { kind: 'hidden' }

  return { kind: 'unknown' }
}
