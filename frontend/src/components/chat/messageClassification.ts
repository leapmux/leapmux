import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import * as chatStyles from './messageStyles.css'

// ---------------------------------------------------------------------------
// MessageCategory — discriminated union for single-pass message classification
// ---------------------------------------------------------------------------

export type MessageCategory
  = | { kind: 'hidden' }
    | { kind: 'notification_thread', messages: unknown[] }
    | { kind: 'notification' }
    | { kind: 'task_notification' }
    | { kind: 'tool_use', toolName: string, toolUse: Record<string, unknown>, content: Array<Record<string, unknown>> }
    | { kind: 'tool_result' }
    | { kind: 'assistant_text' }
    | { kind: 'assistant_thinking' }
    | { kind: 'user_text' }
    | { kind: 'user_content' }
    | { kind: 'result_divider' }
    | { kind: 'control_response' }
    | { kind: 'compact_summary' }
    | { kind: 'unknown' }

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/** Check whether the wrapper envelope represents a notification thread. */
function isNotificationThreadWrapper(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  if (!wrapper || wrapper.messages.length < 1)
    return false
  const first = wrapper.messages[0] as Record<string, unknown>
  const t = first.type as string | undefined
  const st = first.subtype as string | undefined
  return t === 'settings_changed' || t === 'context_cleared' || t === 'interrupted'
    || (t === 'system' && st !== 'init' && st !== 'task_notification')
}

/**
 * Classify a parsed message into exactly one category.
 *
 * This replaces the ~15 boolean flags previously computed in MessageBubble
 * and the repeated type-checking done by each renderer in the chain.
 */
export function classifyMessage(
  parentObject: Record<string, unknown> | undefined,
  wrapper: { old_seqs: number[], messages: unknown[] } | null,
): MessageCategory {
  // 1. Notification thread (wrapper with notification-type first message)
  if (isNotificationThreadWrapper(wrapper))
    return { kind: 'notification_thread', messages: wrapper.messages }

  if (!parentObject)
    return { kind: 'unknown' }

  const type = parentObject.type as string | undefined
  const subtype = parentObject.subtype as string | undefined

  // 2. Hidden: system init, or system status (non-compacting)
  if (type === 'system') {
    if (subtype === 'init')
      return { kind: 'hidden' }
    if (subtype === 'status' && parentObject.status !== 'compacting')
      return { kind: 'hidden' }
    // 3. Task notification
    if (subtype === 'task_notification')
      return { kind: 'task_notification' }
    // 4. Other system subtypes are notifications (compact_boundary, microcompact_boundary, etc.)
    return { kind: 'notification' }
  }

  // 4b. Non-system notification types
  if (type === 'interrupted' || type === 'context_cleared' || type === 'settings_changed')
    return { kind: 'notification' }

  // 5. Result divider
  if (type === 'result')
    return { kind: 'result_divider' }

  // 6. Compact summary
  if (parentObject.isCompactSummary === true)
    return { kind: 'compact_summary' }

  // 7. Control response (synthetic message with controlResponse)
  if (parentObject.isSynthetic === true && isObj(parentObject.controlResponse))
    return { kind: 'control_response' }

  // 8. Assistant messages
  if (type === 'assistant') {
    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObj(message)) {
      const content = (message as Record<string, unknown>).content
      if (Array.isArray(content)) {
        const contentArr = content as Array<Record<string, unknown>>
        // Check for tool_use first (higher priority than text)
        const toolUse = contentArr.find(c => isObj(c) && c.type === 'tool_use') as Record<string, unknown> | undefined
        if (toolUse) {
          return {
            kind: 'tool_use',
            toolName: String(toolUse.name || ''),
            toolUse,
            content: contentArr,
          }
        }
        // Check for text content
        const hasText = contentArr.some(c => isObj(c) && c.type === 'text')
        if (hasText)
          return { kind: 'assistant_text' }
        // Check for thinking content
        const hasThinking = contentArr.some(c => isObj(c) && c.type === 'thinking')
        if (hasThinking)
          return { kind: 'assistant_thinking' }
      }
    }
    return { kind: 'unknown' }
  }

  // 9–10. User messages
  if (type === 'user') {
    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObj(message)) {
      const content = (message as Record<string, unknown>).content
      // 9. String content → user_text
      if (typeof content === 'string')
        return { kind: 'user_text' }
      // 10. Array content with tool_result → tool_result
      if (Array.isArray(content)) {
        const hasToolResult = (content as Array<Record<string, unknown>>).some(
          c => isObj(c) && c.type === 'tool_result',
        )
        if (hasToolResult)
          return { kind: 'tool_result' }
      }
    }
    return { kind: 'unknown' }
  }

  // 11. Plain object with string .content and no .type → user_content
  if (!type && typeof parentObject.content === 'string')
    return { kind: 'user_content' }

  // 12. Fallback
  return { kind: 'unknown' }
}

// ---------------------------------------------------------------------------
// CSS helpers — derive layout classes from category
// ---------------------------------------------------------------------------

function roleStyle(role: MessageRole): string {
  switch (role) {
    case MessageRole.USER: return chatStyles.userMessage
    case MessageRole.ASSISTANT: return chatStyles.assistantMessage
    default: return chatStyles.systemMessage
  }
}

const META_KINDS = new Set<MessageCategory['kind']>([
  'hidden',
  'result_divider',
  'tool_use',
  'tool_result',
  'control_response',
  'compact_summary',
  'notification',
  'task_notification',
])

/** Row class: determines horizontal alignment. */
export function messageRowClass(kind: MessageCategory['kind'], role: MessageRole): string {
  if (kind === 'notification' || kind === 'notification_thread')
    return chatStyles.messageRowCenter
  if (!META_KINDS.has(kind) && role === MessageRole.USER)
    return chatStyles.messageRowEnd
  return chatStyles.messageRow
}

/** Bubble class: determines visual style of the message container. */
export function messageBubbleClass(kind: MessageCategory['kind'], role: MessageRole): string {
  if (kind === 'notification' || kind === 'notification_thread')
    return chatStyles.systemMessage
  if (kind === 'assistant_thinking')
    return chatStyles.thinkingMessage
  if (META_KINDS.has(kind))
    return chatStyles.metaMessage
  return roleStyle(role)
}
