import type { AgentChatMessage, AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { AgentSessionInfo } from '~/stores/agentSession.store'
import { AgentProvider, AgentStatus, MessageRole } from '~/generated/leapmux/v1/agent_pb'
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
      const parsed = parseMessageContent(msg)
      const innerType = getInnerMessageType(parsed)
      if (innerType === 'context_cleared')
        return false
      // Check if context_cleared is in a notification thread wrapper.
      if (parsed.wrapper) {
        for (const m of parsed.wrapper.messages) {
          if (typeof m === 'object' && m !== null && (m as Record<string, unknown>).type === 'context_cleared')
            return false
        }
      }
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

/**
 * Whether the chat-level thinking indicator should be shown for an agent.
 * Codex exposes an explicit turn ID for active turns; prefer that over the
 * generic message-history heuristic so idle-but-running Codex tabs don't show
 * as thinking on creation.
 */
export function shouldShowThinkingIndicator(
  agent: AgentInfo | undefined,
  sessionInfo: AgentSessionInfo | undefined,
  msgs: AgentChatMessage[],
  streamingText: string | undefined,
  pendingControlRequests = 0,
): boolean {
  if (!agent || agent.status !== AgentStatus.ACTIVE)
    return false
  if (pendingControlRequests > 0)
    return false
  if (streamingText)
    return true
  if (agent.agentProvider === AgentProvider.CODEX)
    return Boolean(sessionInfo?.codexTurnId)
  return isAgentWorking(msgs)
}
