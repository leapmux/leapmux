import type { MessageCategory } from '../../messageClassifier'
import type { ClassificationInput } from '../registry'
import { CODEBUDDY_FRAME_KIND, CODEBUDDY_SYSTEM_SUBTYPE } from '~/generated/contracts/codebuddy-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { isNotificationThreadWrapper } from '../../messageUtils'
import { notificationClassifierFor } from '../../notificationClassification'
import { codebuddyNotificationEntry } from './extractors/notification'
import { storedFunctionCallID, storedFunctionIsProgress } from './storedFunction'

/**
 * The `system` subtypes that hold nothing for the transcript. The worker stores every `system` line whole, so the
 * browser decides here:
 *
 * - `init` starts each turn with the session facts that the worker already read: the session ID, the model, the
 *   mode, and the slash commands.
 * - `keepalive` is a heartbeat while the model reasons, with no content.
 * - `status` holds `compacting` or null. The null status ends a progress state. CodeBuddy Code writes no compaction
 *   boundary, so a "compacting" start would draw a spinner that no row ends.
 * - `task_started` and `task_notification` reach the background-task registry, which the sidebar draws. The worker
 *   consumes them, so only a row that an earlier worker stored can reach the browser.
 */
const CODEBUDDY_HIDDEN_SYSTEM_SUBTYPES: ReadonlySet<string> = new Set([
  CODEBUDDY_SYSTEM_SUBTYPE.Init,
  CODEBUDDY_SYSTEM_SUBTYPE.Keepalive,
  CODEBUDDY_SYSTEM_SUBTYPE.Status,
  CODEBUDDY_SYSTEM_SUBTYPE.TaskStarted,
  CODEBUDDY_SYSTEM_SUBTYPE.TaskNotification,
])

/**
 * The `system` subtypes that `codebuddyNotificationEntry` reads. A line of one of them draws a notification when it
 * holds an entry, and nothing when it holds none.
 */
const CODEBUDDY_NOTIFICATION_SYSTEM_SUBTYPES: ReadonlySet<string> = new Set([
  CODEBUDDY_SYSTEM_SUBTYPE.McpStatus,
  CODEBUDDY_SYSTEM_SUBTYPE.Informational,
])

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
  const notification = notificationClassifierFor(input.agentProvider, codebuddyNotificationEntry)

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
    case CODEBUDDY_FRAME_KIND.System: {
      const subtype = pickString(parent, 'subtype')
      if (CODEBUDDY_HIDDEN_SYSTEM_SUBTYPES.has(subtype))
        return { kind: 'hidden' }
      // A subtype that this build does not know stays an unrecognized row, so the reader can still inspect it.
      return CODEBUDDY_NOTIFICATION_SYSTEM_SUBTYPES.has(subtype) ? notification([parent], 'hidden') : { kind: 'unknown' }
    }
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
    // A control notification states protocol state on one channel: a permission decision that the control banner
    // already showed, a completed elicitation that its own row states, or the slash-command list, which feeds no menu.
    case CODEBUDDY_FRAME_KIND.ControlNotification:
      return { kind: 'hidden' }
    case CODEBUDDY_FRAME_KIND.Error:
      return notification([parent])
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
