import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { QODER_FRAME_KIND, QODER_SYSTEM_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { qoderNotificationEntry } from './extractors/notification'

/**
 * Qoder message classification.
 *
 * The stream carries Anthropic-shaped assistant frames with extra fields, plus
 * a first-class control channel. This plugin reads only what it needs to pick a
 * category; the extraction turns it into the neutral row.
 */
export function classifyQoderMessage(input: ClassificationInput): MessageCategory {
  const parent = input.parentObject
  const wrapper = input.wrapper
  const notification = notificationClassifierFor(input.agentProvider, qoderNotificationEntry)

  // Hide an empty wrapper before the thread check narrows `wrapper`.
  // Qoder's compaction frames can share a thread with LeapMux notifications.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }
  if (isNotificationThreadWrapper(wrapper, undefined, (type, subtype) =>
    type === QODER_FRAME_KIND.System
    && (subtype === QODER_SYSTEM_SUBTYPE.CompactBoundary || subtype === QODER_SYSTEM_SUBTYPE.Status))) {
    return notification(wrapper.messages, 'hidden')
  }

  if (!parent || !isObject(parent))
    return { kind: 'unknown' }

  const type = pickString(parent, 'type')
  switch (type) {
    case QODER_FRAME_KIND.Assistant:
      return classifyAssistant(parent)
    case QODER_FRAME_KIND.User:
      return classifyUser(parent)
    case QODER_FRAME_KIND.Result:
      return { kind: 'result_divider' }
    case QODER_FRAME_KIND.System:
      return notification([parent], 'hidden')
    case QODER_FRAME_KIND.StreamEvent:
    case QODER_FRAME_KIND.CommandLifecycle:
    case QODER_FRAME_KIND.ToolProgress:
    case QODER_FRAME_KIND.Progress:
    case QODER_FRAME_KIND.Attachment:
      return { kind: 'hidden' }
    default:
      return { kind: 'unknown' }
  }
}

/** Qoder echoes user input and sends an empty result before image bytes. */
function classifyUser(parent: Record<string, unknown>): MessageCategory {
  const message = isObject(parent.message) ? parent.message : undefined
  const content = message && Array.isArray(message.content) ? message.content : []
  const result = content.find(block => isObject(block) && pickString(block, 'type') === 'tool_result')
  if (!isObject(result))
    return { kind: 'hidden' }
  const body = result.content
  if (typeof body === 'string')
    return { kind: body.trim() ? 'tool_result' : 'hidden' }
  if (!Array.isArray(body))
    return { kind: 'unknown' }
  let opaque = false
  for (const block of body) {
    if (!isObject(block)) {
      opaque = true
      continue
    }
    const type = pickString(block, 'type')
    if (type === 'image' || type === 'resource')
      return { kind: 'tool_result' }
    if (type === 'text') {
      if (pickString(block, 'text').trim())
        return { kind: 'tool_result' }
      continue
    }
    opaque = true
  }
  return { kind: opaque ? 'unknown' : 'hidden' }
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
