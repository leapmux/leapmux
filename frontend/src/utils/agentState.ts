import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { decompressContentToString } from '~/lib/decompress'

/** RESULT-role messages with these inner types are mid-turn notifications, not turn ends. */
const NOTIFICATION_TYPES = new Set(['settings_changed', 'context_cleared'])

/** Parse the inner message type from a chat message's compressed content. */
function getInnerMessageType(msg: AgentChatMessage): string | undefined {
  try {
    const text = decompressContentToString(msg.content, msg.contentCompression)
    if (!text) return undefined
    const parsed = JSON.parse(text)
    const inner = parsed?.messages?.[0] ?? parsed
    return inner?.type as string | undefined
  }
  catch { return undefined }
}

/**
 * Whether the agent is still working — the last meaningful (non-notification)
 * message is not a turn-end RESULT.
 */
export function isAgentWorking(msgs: AgentChatMessage[]): boolean {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const msg = msgs[i]
    if (msg.role !== MessageRole.RESULT) return true
    // RESULT message — check if it's just a mid-turn notification to skip
    const innerType = getInnerMessageType(msg)
    if (innerType && NOTIFICATION_TYPES.has(innerType)) continue
    return false // real turn-end RESULT
  }
  return false // no messages or all notifications
}
