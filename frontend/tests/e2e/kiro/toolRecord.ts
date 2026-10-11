import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { acpToolSupplement } from '../../../src/components/chat/providers/acp/toolSupplement'
import { parseMessageContent } from '../../../src/lib/messageParser'

/** Read the Kiro provider section only when its original Worker frame identity matches. */
export function readKiroToolSupplement(message: AgentChatMessage): Record<string, unknown> | null {
  const parsed = parseMessageContent(message)
  const original = parsed.parentObject
  return original ? acpToolSupplement(original, parsed.supplementalContent) ?? null : null
}
