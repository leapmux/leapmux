import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { CODEBUDDY_FRAME_KIND } from '~/generated/contracts/codebuddy-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { storedFunctionCallID, storedFunctionIsProgress } from './storedFunction'

/**
 * CodeBuddy message classification.
 *
 * Live frames carry Anthropic content blocks inside `message`. A completed
 * Workflow child stores its native content blocks on the record itself. This
 * classifier reads both shapes and leaves the row content to extraction.
 */
export function classifyCodebuddyMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider)

  // The empty-wrapper test runs FIRST, so the thread test below stays the one
  // narrowing on `wrapper`. These providers write no notification of their own,
  // so a thread holds LeapMux's own envelopes alone.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper))
    return notification(wrapper.messages, 'hidden')

  if (!parent || !isObject(parent))
    return { kind: 'unknown' }

  const type = pickString(parent, 'type')
  switch (type) {
    case CODEBUDDY_FRAME_KIND.Assistant:
      return classifyAssistant(parent)
    case 'message':
      return classifyStoredMessage(parent)
    case 'function_call':
      return storedFunctionCallID(parent) && pickString(parent, 'name')
        ? { kind: 'tool_use' }
        : { kind: 'unknown' }
    case 'function_call_output':
    case 'function_call_result':
      if (!storedFunctionCallID(parent))
        return { kind: 'unknown' }
      return storedFunctionIsProgress(parent) ? { kind: 'hidden' } : { kind: 'tool_result' }
    case CODEBUDDY_FRAME_KIND.User:
      return { kind: 'tool_result' }
    case CODEBUDDY_FRAME_KIND.Result:
      return { kind: 'result_divider' }
    case CODEBUDDY_FRAME_KIND.ConversationReset:
      return { kind: 'notification', entries: [] }
    default:
      return { kind: 'unknown' }
  }
}

function classifyStoredMessage(parent: Record<string, unknown>): MessageCategory {
  if (pickString(parent, 'role') !== 'assistant')
    return { kind: 'unknown' }
  const content = Array.isArray(parent.content) ? parent.content : []
  const hasText = content.some(
    block => isObject(block) && pickString(block, 'type') === 'output_text' && typeof block.text === 'string' && block.text.length > 0,
  )
  return { kind: hasText ? 'assistant_text' : 'hidden' }
}

function classifyAssistant(parent: Record<string, unknown>): MessageCategory {
  const message = isObject(parent.message) ? parent.message : undefined
  const content = message && Array.isArray(message.content) ? message.content : []
  const hasToolUse = content.some(
    block => isObject(block) && pickString(block, 'type') === 'tool_use',
  )
  if (hasToolUse)
    return { kind: 'tool_use' }
  const hasText = content.some(
    block => isObject(block) && pickString(block, 'type') === 'text',
  )
  if (hasText)
    return { kind: 'assistant_text' }
  const hasThinking = content.some(
    block => isObject(block) && pickString(block, 'type') === 'thinking',
  )
  if (hasThinking)
    return { kind: 'assistant_thinking' }
  return { kind: 'hidden' }
}
