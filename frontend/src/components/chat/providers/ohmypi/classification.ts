import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { OH_MY_PI_EVENT, OH_MY_PI_NOTIFICATION_EVENT, OH_MY_PI_ROLE } from '~/generated/contracts/ohmypi-protocol'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { ohMyPiNotificationEntry } from './extractors/notification'
import { ohMyPiContentText } from './messageContent'

/** Native notifications and the command replies that can join their thread. */
const OH_MY_PI_NOTIFICATION_TYPES = new Set<string>([
  ...OH_MY_PI_NOTIFICATION_EVENT,
  OH_MY_PI_EVENT.ExtensionUIRequest,
  OH_MY_PI_EVENT.Response,
])

/** omp message classification. */
export function classifyOhMyPiMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, ohMyPiNotificationEntry)

  // The empty-wrapper test runs FIRST, so the thread test below stays the one
  // narrowing on `wrapper`.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper, OH_MY_PI_NOTIFICATION_TYPES))
    return notification(wrapper.messages, 'hidden')

  if (!parent)
    return { kind: 'unknown' }

  // A user row the service layer persisted is LeapMux's `{content}` shape, with no omp
  // `type`. It is matched BEFORE the event dispatch, so it does not fall through to the
  // unknown card.
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  if (type === OH_MY_PI_EVENT.AgentEnd)
    return { kind: 'result_divider' }

  if (type === OH_MY_PI_EVENT.ToolExecutionStart) {
    // A turn that ended while the call ran stores this frame AGAIN as the closing row,
    // with the partial result omp reported beside it. That copy is the call's result.
    return retainedRowIsFinal(input.completion) ? { kind: 'tool_result' } : { kind: 'tool_use' }
  }
  if (type === OH_MY_PI_EVENT.ToolExecutionEnd)
    return { kind: 'tool_result' }

  if (type === OH_MY_PI_EVENT.MessageEnd) {
    const message = pickObject(parent, 'message')
    const role = pickString(message, 'role')
    // The reply's TEXT alone. The worker persists its thinking as a reasoning row of
    // its own, before this one, so a reply with no text draws nothing here.
    if (role === OH_MY_PI_ROLE.Assistant)
      return ohMyPiContentText(parent).trim() ? { kind: 'assistant_text' } : { kind: 'hidden' }
    if (role === OH_MY_PI_ROLE.Custom) {
      // omp writes a custom message for the model: a job's result, a late diagnostic,
      // the file of a skill the reader called. None of them is the assistant's reply,
      // whatever its attribution, so each draws as a notice, and a copy that another
      // row already draws reads as no entry and hides.
      return notification([parent], 'hidden')
    }
    return { kind: 'unknown' }
  }

  if (OH_MY_PI_NOTIFICATION_TYPES.has(type))
    return notification([parent], 'hidden')

  // LeapMux's own envelope, which every provider answers the same way. No branch
  // above claims one of these rows, because omp's vocabulary spells none of their
  // tokens.
  if (isPlainNotificationType(type))
    return notification([parent])

  return { kind: 'unknown' }
}
