import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { QODER_FRAME_KIND, QODER_SYSTEM_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { qoderNotificationEntry } from './extractors/notification'

/** Classify Qoder's native transcript rows before extraction. */
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
    && (subtype === QODER_SYSTEM_SUBTYPE.CompactBoundary
      || subtype === QODER_SYSTEM_SUBTYPE.ApiRetry
      || subtype === QODER_SYSTEM_SUBTYPE.ModelQueueStatus
      || subtype === QODER_SYSTEM_SUBTYPE.HookResponse
      || subtype === QODER_SYSTEM_SUBTYPE.HookDiagnostics
      || subtype === QODER_SYSTEM_SUBTYPE.AgentsMdSizeWarning))) {
    return notification(wrapper.messages, wrapper.messages.every(isEmptyHookDiagnostics)
      ? 'hidden'
      : 'notification')
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
      if (isEmptyHookDiagnostics(parent))
        return { kind: 'hidden' }
      return notification([parent])
    default:
      return { kind: 'unknown' }
  }
}

function isEmptyHookDiagnostics(message: unknown): boolean {
  return isObject(message) && message.type === QODER_FRAME_KIND.System
    && message.subtype === QODER_SYSTEM_SUBTYPE.HookDiagnostics
    && message.read_status === 'complete'
    && Array.isArray(message.diagnostics) && message.diagnostics.length === 0
}

/**
 * Read tool results.
 *
 * Qoder sends an empty `content: []` as a provisional result before the result that
 * holds the image bytes, so that frame stays hidden. A body of an empty string or of
 * blank text answers a call that printed nothing, such as `mkdir`. The request row
 * never reads its result, so this row is the only one that states the finished status
 * and the exit code, and it stays visible.
 */
function classifyUser(parent: Record<string, unknown>): MessageCategory {
  const message = isObject(parent.message)
    ? parent.message
    : undefined
  const content = message && Array.isArray(message.content)
    ? message.content
    : []
  const result = content.find(block => isObject(block) && pickString(block, 'type') === 'tool_result')
  if (!isObject(result))
    return { kind: 'unknown' }
  const body = result.content
  if (typeof body === 'string')
    return { kind: 'tool_result' }
  if (!Array.isArray(body))
    return { kind: 'unknown' }
  if (body.length === 0)
    return { kind: 'hidden' }
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
  return {
    kind: opaque ? 'unknown' : 'tool_result',
  }
}

function classifyAssistant(parent: Record<string, unknown>): MessageCategory {
  const message = isObject(parent.message)
    ? parent.message
    : undefined
  const content = message && Array.isArray(message.content)
    ? message.content
    : []
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
