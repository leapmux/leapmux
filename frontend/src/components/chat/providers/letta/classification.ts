import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND, LETTA_MESSAGE } from '~/generated/contracts/letta-protocol'
import { ASSEMBLED_MESSAGE } from '~/generated/contracts/worker-vocab'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { notificationClassifierFor } from '../../notificationClassification'
import { lettaNotificationEntry } from './extractors/notification'
import { isLettaToolProgress, lettaReturnedData, lettaToolPayload } from './toolOutput'

/**
 * Letta Code message classification.
 *
 * The worker persists native stream_delta payloads. It stores one payload per
 * call when a native message carries several tool calls.
 */
export function classifyLettaMessage(input: ClassificationInput): MessageCategory {
  const notification = notificationClassifierFor(input.agentProvider, lettaNotificationEntry)
  const wrapper = input.wrapper
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  // The Worker wraps a notice and nothing else, so every wrapper is a notification
  // thread. The shared test `isNotificationThreadWrapper` accepts a thread only when
  // a member has a base notification `type`, and no Letta Code notice has one (a
  // subagent snapshot has its own `type`, a loop error has none). A thread that fails
  // that test is read by its first member alone. A failed turn stores the snapshot
  // first, so the error behind it would never reach the transcript.
  if (wrapper)
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
  const source = lettaToolPayload(parent)
  if (!source)
    return { kind: 'hidden' }
  const messageType = pickString(source, LETTA_DELTA_FIELD.MessageType)
  switch (messageType) {
    case LETTA_DELTA_KIND.AssistantMessage:
      return { kind: 'assistant_text' }
    case LETTA_DELTA_KIND.ReasoningMessage:
      return { kind: 'assistant_thinking' }
    case LETTA_DELTA_KIND.ClientToolStart:
      return { kind: 'tool_use' }
    case LETTA_DELTA_KIND.ToolCallMessage: {
      const singular = pickObject(source, LETTA_DELTA_FIELD.ToolCall)
      const calls = source[LETTA_DELTA_FIELD.ToolCalls]
      const first = Array.isArray(calls) ? calls.find(isObject) : null
      const call = singular ?? first
      return call && pickString(call, LETTA_DELTA_FIELD.ToolCallID) && pickString(call, LETTA_DELTA_FIELD.Name)
        ? { kind: 'tool_use' }
        : { kind: 'hidden' }
    }
    case LETTA_DELTA_KIND.ToolReturnMessage:
      return isLettaToolProgress(source) ? { kind: 'hidden' } : { kind: 'tool_result' }
    case LETTA_DELTA_KIND.ClientToolEnd:
      return lettaReturnedData(source).kind === 'present' ? { kind: 'tool_result' } : { kind: 'hidden' }
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
