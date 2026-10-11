import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { MIMO_EVENT, MIMO_PART_TYPE, MIMO_STATUS_TYPE } from '~/generated/contracts/mimo-protocol'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { mimoNotificationEntry } from './extractors/notification'
import { mimoEvent, mimoToolPart, mimoToolSpanRole } from './extractors/toolCommon'

/**
 * The event types that thread into chat as notifications: a retry, a compaction, and
 * an error outside a turn. A consolidated thread holds only these, so a thread of any
 * of them is recognized as MiMo's.
 */
const MIMO_NOTIFICATION_TYPES = new Set<string>([
  MIMO_EVENT.SessionStatus,
  MIMO_EVENT.MessagePartUpdated,
  MIMO_EVENT.SessionError,
])

/**
 * True for a row the worker persisted as a TURN END.
 *
 * The worker states the turn's tool count beside every turn end, and nowhere else. A
 * `session.error` reaches a transcript both ways -- as the divider of a failed turn and
 * as a notification outside one -- and the count is what tells the two apart.
 */
function persistedAsTurnEnd(parent: Record<string, unknown>): boolean {
  return typeof parent[MESSAGE_METADATA_FIELD.ToolUses] === 'number'
}

/** MiMo message classification. */
export function classifyMiMoMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, mimoNotificationEntry)

  // The empty-wrapper check runs FIRST so the type guard below stays the only
  // narrowing on `wrapper`.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper, MIMO_NOTIFICATION_TYPES))
    return notification(wrapper.messages, 'hidden')

  if (!parent)
    return { kind: 'unknown' }

  // A user row the service layer persisted is the LeapMux-neutral `{content}` shape,
  // with no MiMo `type`.
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    if (parent.hidden === true)
      return { kind: 'hidden' }
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  const event = mimoEvent(parent)
  if (event) {
    switch (event.type) {
      case MIMO_EVENT.SessionStatus: {
        const status = pickString(pickObject(event.properties, 'status'), 'type')
        if (status === MIMO_STATUS_TYPE.Idle)
          return { kind: 'result_divider' }
        if (status === MIMO_STATUS_TYPE.Retry)
          return notification([parent], 'hidden')
        // `busy` never reaches a transcript: the worker reads it for the turn flag.
        return { kind: 'hidden' }
      }
      case MIMO_EVENT.SessionError:
        return persistedAsTurnEnd(parent) ? { kind: 'result_divider' } : notification([parent], 'hidden')
      case MIMO_EVENT.MessagePartUpdated: {
        const part = pickObject(event.properties, 'part')
        if (pickString(part, 'type') === MIMO_PART_TYPE.Compaction)
          return notification([parent], 'hidden')
        const tool = mimoToolPart(parent)
        if (!tool)
          return { kind: 'hidden' }
        const role = mimoToolSpanRole(tool, input.completion)
        if (role === 'result')
          return { kind: 'tool_result' }
        if (role === 'request')
          return { kind: 'tool_use' }
        // A pending frame states no input, and the worker never persists one.
        return { kind: 'hidden' }
      }
      default:
        break
    }
  }

  // A row in LeapMux's own envelope carries no MiMo event.
  if (isPlainNotificationType(type))
    return notification([parent])

  return { kind: 'unknown' }
}
