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
  // Read every notification member. Unknown metadata must not conceal a later readable notice.
  if (wrapper)
    return notification(wrapper.messages)

  const parent = input.parentObject
  if (!parent)
    return { kind: 'unknown' }

  // A worker-assembled row carries the worker's own `type` field.
  if (pickString(parent, 'type') === ASSEMBLED_MESSAGE.Type) {
    const text = pickString(parent, 'text')
    return text
      ? { kind: 'assistant_text' }
      : { kind: 'hidden' }
  }

  // Native frames use type. A kind-only record is not a native turn end.
  const messageKind = pickString(parent, 'type')
  if (messageKind === LETTA_MESSAGE.TurnFinished)
    return { kind: 'result_divider' }

  // The worker stores each native delta at the top level.
  const source = lettaToolPayload(parent)
  if (!source)
    return { kind: 'unknown' }
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
      const first = Array.isArray(calls)
        ? calls.find(isObject)
        : null
      const call = singular ?? first
      return call && pickString(call, LETTA_DELTA_FIELD.ToolCallID) && pickString(call, LETTA_DELTA_FIELD.Name)
        ? { kind: 'tool_use' }
        : { kind: 'unknown' }
    }
    case LETTA_DELTA_KIND.ToolReturnMessage:
      return isLettaToolProgress(source)
        ? { kind: 'hidden' }
        : { kind: 'tool_result' }
    case LETTA_DELTA_KIND.ClientToolEnd:
      return lettaReturnedData(source).kind === 'present'
        ? { kind: 'tool_result' }
        : { kind: 'hidden' }
    case LETTA_DELTA_KIND.UserMessage:
      return { kind: 'user_content' }
    default:
      break
  }

  // Recognize the worker's user-content envelope before the native notice fallback.
  if (typeof parent.content === 'string' && !('type' in parent) && !('message_type' in parent))
    return { kind: 'user_content' }

  return notification([parent])
}
