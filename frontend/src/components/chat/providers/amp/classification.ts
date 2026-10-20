import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { AMP_BLOCK_TYPE, AMP_LINE_TYPE } from '~/generated/contracts/amp-protocol'
import { pickString } from '~/lib/jsonPick'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { retainedRowIsFinal } from '../registry'
import { ampBlockText, ampToolResult, ampToolUse } from './extractors/toolCommon'

/**
 * Classify one assistant row. A tool call owns a span and takes priority.
 * A retained final call supplies its result. Text takes priority over thinking.
 * The current worker stores no empty or redacted assistant block.
 */
function classifyAssistantRow(input: ClassificationInput, parent: Record<string, unknown>): MessageCategory {
  if (ampToolUse(parent))
    return retainedRowIsFinal(input.completion) ? { kind: 'tool_result' } : { kind: 'tool_use' }
  if (ampBlockText(parent, AMP_BLOCK_TYPE.Text, 'text'))
    return { kind: 'assistant_text' }
  if (ampBlockText(parent, AMP_BLOCK_TYPE.Thinking, 'thinking'))
    return { kind: 'assistant_thinking' }
  return { kind: 'unknown' }
}

/** Amp message classification. */
export function classifyAmpMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider)

  // Test an empty wrapper before the notification thread. Amp threads hold only LeapMux notifications.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper))
    return notification(wrapper.messages, 'hidden')

  if (!parent)
    return { kind: 'unknown' }

  // The service stores user input as LeapMux's `{content}` envelope. The worker consumes Amp's native user echo.
  const type = pickString(parent, 'type')
  if (!type && typeof parent.content === 'string') {
    if (parent.planExecution === true)
      return { kind: 'plan_execution' }
    return { kind: 'user_content' }
  }

  switch (type) {
    case AMP_LINE_TYPE.Result:
      return { kind: 'result_divider' }
    case AMP_LINE_TYPE.Assistant:
      return classifyAssistantRow(input, parent)
    case AMP_LINE_TYPE.User:
      // A user row reaches the transcript only as a tool result.
      return ampToolResult(parent) ? { kind: 'tool_result' } : { kind: 'unknown' }
    default:
      break
  }

  // Amp's vocabulary differs from LeapMux's notification vocabulary.
  if (isPlainNotificationType(type))
    return notification([parent])

  // Keep an unknown native frame available for inspection.
  return { kind: 'unknown' }
}
