import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { CODEBUDDY_FRAME_KIND } from '~/generated/contracts/codebuddy-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'

/**
 * CodeBuddy message classification.
 *
 * The stream is Claude Code-shaped, so an assistant frame carries a `message`
 * with Anthropic content blocks. This plugin reads only what it needs to pick a
 * category; the extraction turns it into the neutral row.
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
