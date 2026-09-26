import type { MessageCategory } from '../../messageClassifier'
import type { NotificationEntry } from '../../model/notification'
import type { ClassificationInput } from '../registry'
import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND, LETTA_MESSAGE } from '~/generated/contracts/letta-protocol'
import { ASSEMBLED_MESSAGE } from '~/generated/contracts/worker-vocab'
import { pickObject, pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'

/**
 * Letta Code message classification.
 *
 * The worker persists each stream_delta payload verbatim, so the dispatcher
 * reads the same `message_type` the server wrote.
 */
export function classifyLettaMessage(input: ClassificationInput): MessageCategory {
  const notification = notificationClassifierFor(input.agentProvider, lettaNotificationEntry)
  const wrapper = input.wrapper
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper))
    return notification(wrapper.messages, 'hidden')

  const parent = input.parentObject
  if (!parent)
    return { kind: 'hidden' }

  // A worker-assembled row carries the worker's own `type` field.
  if (pickString(parent, 'type') === ASSEMBLED_MESSAGE.Type) {
    const text = pickString(parent, 'text') ?? ''
    return text ? { kind: 'assistant_text' } : { kind: 'hidden' }
  }

  // The wire discriminator is `type`, not `kind`, on every protocol_v2
  // message. A `turn_finished` read from `kind` is not recognized and becomes a
  // JSON notification instead of the result divider the reader expects.
  const messageKind = pickString(parent, 'type') || pickString(parent, LETTA_DELTA_FIELD.Kind)
  if (messageKind === LETTA_MESSAGE.TurnFinished)
    return { kind: 'result_divider' }

  // The delta payload may sit at the top level (the persisted shape) or under
  // `payload` (the raw frame).
  const nested = pickObject(parent, 'payload')
  const source = nested && Object.keys(nested).length > 0 ? nested : parent
  const messageType = pickString(source, LETTA_DELTA_FIELD.MessageType) || pickString(source, 'message_type')
  switch (messageType) {
    case LETTA_DELTA_KIND.AssistantMessage:
      return { kind: 'assistant_text' }
    case LETTA_DELTA_KIND.ReasoningMessage:
      return { kind: 'assistant_thinking' }
    case LETTA_DELTA_KIND.ClientToolStart:
      return { kind: 'tool_use' }
    case LETTA_DELTA_KIND.ToolReturnMessage:
    case LETTA_DELTA_KIND.ClientToolEnd:
      return { kind: 'tool_result' }
    case LETTA_DELTA_KIND.UserMessage:
      return { kind: 'user_content' }
    default:
      break
  }

  // The user row LeapMux writes itself is `{content}`, not a provider frame.
  // Without this it fell through to the notification reader and drew as a JSON
  // blob instead of the message the reader typed.
  if (typeof parent.content === 'string' && !('type' in parent) && !('message_type' in parent))
    return { kind: 'user_content' }

  return notification([parent], 'hidden')
}

/**
 * The notification entry of one provider notice. The raw payload travels whole,
 * so the row draws the provider's own words.
 */
function lettaNotificationEntry(message: Record<string, unknown>): NotificationEntry[] {
  return [{ kind: 'text', text: JSON.stringify(message) }]
}
