import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { getInnerMessageType, parseMessageContent } from '~/lib/messageParser'

/** RESULT-role messages with these inner types are mid-turn notifications, not turn ends. */
const NOTIFICATION_TYPES = new Set(['settings_changed', 'context_cleared', 'plan_execution', 'agent_error'])

/**
 * Whether the agent is still working — the last meaningful (non-notification)
 * message is not a turn-end RESULT.
 */
export function isAgentWorking(msgs: AgentChatMessage[]): boolean {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const msg = msgs[i]
    // Messages with delivery errors were never sent to the agent — skip them.
    if (msg.deliveryError)
      continue
    // LEAPMUX messages are platform notifications (settings_changed,
    // context_cleared, etc.) — they never indicate the agent is working.
    // context_cleared is a turn boundary: the agent restarted with a fresh
    // context and is now idle, so stop scanning into the old history.
    if (msg.role === MessageRole.LEAPMUX) {
      const innerType = getInnerMessageType(parseMessageContent(msg))
      if (innerType === 'context_cleared')
        return false
      continue
    }
    if (msg.role !== MessageRole.RESULT)
      return true
    // RESULT message — check if it's just a mid-turn notification to skip
    const innerType = getInnerMessageType(parseMessageContent(msg))
    if (innerType && NOTIFICATION_TYPES.has(innerType))
      continue
    return false // real turn-end RESULT
  }
  return false // no messages or all notifications
}
