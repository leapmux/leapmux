import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { pickNumber } from '~/lib/jsonPick'
import { messageUsage } from '~/lib/messageParser'

/** Claude Code assistant `message.usage` shape: input_tokens + cache_creation/read_input_tokens. */
export function claudeContextUsageFromMessage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const usage = messageUsage(parsed)
  if (!usage || typeof usage.input_tokens !== 'number')
    return null
  return {
    inputTokens: usage.input_tokens,
    cacheCreationInputTokens: pickNumber(usage, 'cache_creation_input_tokens', 0),
    cacheReadInputTokens: pickNumber(usage, 'cache_read_input_tokens', 0),
  }
}
