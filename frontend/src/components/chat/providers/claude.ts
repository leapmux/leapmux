import type { MessageCategory } from '../messageClassification'
import type { ProviderPlugin } from './registry'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { buildInterruptRequest } from '~/utils/controlResponse'
import { isObject } from '../messageUtils'
import { registerProvider } from './registry'

/** Check whether the wrapper envelope represents a Claude Code notification thread. */
function isNotificationThreadWrapper(wrapper: { messages: unknown[] } | null): wrapper is { messages: unknown[] } {
  if (!wrapper || wrapper.messages.length < 1)
    return false
  const first = wrapper.messages[0] as Record<string, unknown>
  const t = first.type as string | undefined
  const st = first.subtype as string | undefined
  return t === 'settings_changed' || t === 'context_cleared' || t === 'interrupted' || t === 'rate_limit' || t === 'plan_execution' || t === 'agent_renamed'
    || (t === 'system' && st !== 'init' && st !== 'task_notification')
}

/** Claude Code message classification. */
function classifyClaudeCodeMessage(
  parentObject: Record<string, unknown> | undefined,
  wrapper: { old_seqs: number[], messages: unknown[] } | null,
): MessageCategory {
  // 0. Empty wrapper (all notifications consolidated to no-ops) — hide.
  if (wrapper && wrapper.messages.length === 0)
    return { kind: 'hidden' }

  // 1. Notification thread (wrapper with notification-type first message)
  if (isNotificationThreadWrapper(wrapper)) {
    const msgs = wrapper.messages.filter((m) => {
      if (!isObject(m))
        return true
      return !((m as Record<string, unknown>).type === 'rate_limit' && isObject((m as Record<string, unknown>).rate_limit_info)
        && ((m as Record<string, unknown>).rate_limit_info as Record<string, unknown>).status === 'allowed')
    })
    if (msgs.length === 0)
      return { kind: 'hidden' }
    return { kind: 'notification_thread', messages: msgs }
  }

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
    if (subtype === 'task_notification')
      return { kind: 'task_notification' }
    return { kind: 'notification' }
  }

  // Non-system notification types
  if (type === 'rate_limit') {
    if (isObject(parentObject.rate_limit_info) && (parentObject.rate_limit_info as Record<string, unknown>).status === 'allowed')
      return { kind: 'hidden' }
    return { kind: 'notification' }
  }
  if (type === 'interrupted' || type === 'context_cleared' || type === 'settings_changed' || type === 'agent_renamed')
    return { kind: 'notification' }

  // Result divider
  if (type === 'result')
    return { kind: 'result_divider' }

  // Compact summary
  if (parentObject.isCompactSummary === true)
    return { kind: 'compact_summary' }

  // Control response (synthetic message with controlResponse)
  if (parentObject.isSynthetic === true && isObject(parentObject.controlResponse))
    return { kind: 'control_response' }

  // Assistant messages
  if (type === 'assistant') {
    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObject(message)) {
      const content = (message as Record<string, unknown>).content
      if (Array.isArray(content)) {
        const contentArr = content as Array<Record<string, unknown>>
        const toolUse = contentArr.find(c => isObject(c) && c.type === 'tool_use') as Record<string, unknown> | undefined
        if (toolUse) {
          return {
            kind: 'tool_use',
            toolName: String(toolUse.name || ''),
            toolUse,
            content: contentArr,
          }
        }
        if (contentArr.some(c => isObject(c) && c.type === 'text'))
          return { kind: 'assistant_text' }
        if (contentArr.some(c => isObject(c) && c.type === 'thinking'))
          return { kind: 'assistant_thinking' }
      }
    }
    return { kind: 'unknown' }
  }

  // User messages
  if (type === 'user') {
    const message = parentObject.message as Record<string, unknown> | undefined
    if (isObject(message)) {
      const content = (message as Record<string, unknown>).content
      if (typeof content === 'string')
        return { kind: 'user_text' }
      if (Array.isArray(content)) {
        if ((content as Array<Record<string, unknown>>).some(c => isObject(c) && c.type === 'tool_result'))
          return { kind: 'tool_result' }
      }
    }
    return { kind: 'unknown' }
  }

  // Plain object with string .content and no .type → user_content
  if (!type && typeof parentObject.content === 'string') {
    if (parentObject.hidden === true)
      return { kind: 'hidden' }
    return { kind: 'user_content' }
  }

  return { kind: 'unknown' }
}

const claudeCodePlugin: ProviderPlugin = {
  classify: classifyClaudeCodeMessage,

  buildInterruptContent(): string | null {
    return buildInterruptRequest()
  },

  // Claude Code control_response format is the native wire format —
  // return null to signal "send as-is".
  buildControlResponse(): Uint8Array | null {
    return null
  },
}

registerProvider(AgentProvider.CLAUDE_CODE, claudeCodePlugin)
