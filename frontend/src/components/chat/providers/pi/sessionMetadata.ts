import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { pickNumber } from '~/lib/jsonPick'
import { messageUsage } from '~/lib/messageParser'

/**
 * Pi assistant `message.usage` shape: input / cacheWrite / cacheRead / output / totalTokens.
 * Retained as a fallback for raw/unaugmented messages; newer backend messages carry a normalized
 * top-level context_usage the shared reader handles. Returns null when the message carries no Pi
 * `message.usage`, or it has no token data.
 */
export function piContextUsageFromMessage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const usage = messageUsage(parsed)
  if (!usage || typeof usage.input !== 'number')
    return null
  const inputTokens = usage.input
  const cacheCreationInputTokens = pickNumber(usage, 'cacheWrite', 0)
  const cacheReadInputTokens = pickNumber(usage, 'cacheRead', 0)
  const outputTokens = pickNumber(usage, 'output', undefined)
  const totalTokens = pickNumber(usage, 'totalTokens', undefined)
  const hasPiTokenData = inputTokens > 0
    || cacheCreationInputTokens > 0
    || cacheReadInputTokens > 0
    || (outputTokens ?? 0) > 0
    || (totalTokens ?? 0) > 0
  if (!hasPiTokenData)
    return null
  const piUsage: ContextUsageInfo = { inputTokens, cacheCreationInputTokens, cacheReadInputTokens }
  if (outputTokens !== undefined)
    piUsage.outputTokens = outputTokens
  if (totalTokens !== undefined && totalTokens > 0)
    piUsage.contextTokens = totalTokens
  return piUsage
}
