import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { pickNumber } from '~/lib/jsonPick'
import { messageUsage } from '~/lib/messageParser'

/**
 * The context usage one omp assistant message states in `message.usage`:
 * `{input, output, cacheRead, cacheWrite, totalTokens, cost}`.
 *
 * The worker also writes the normalized `context_usage` beside each row, which the
 * shared reader prefers; this reads a row that carries only omp's own counts. Returns
 * null for a message with no counts, such as a request that failed before the model
 * answered.
 */
export function ohMyPiContextUsageFromMessage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const usage = messageUsage(parsed)
  if (!usage || typeof usage.input !== 'number')
    return null
  const inputTokens = usage.input
  const cacheCreationInputTokens = pickNumber(usage, 'cacheWrite', 0)
  const cacheReadInputTokens = pickNumber(usage, 'cacheRead', 0)
  const outputTokens = pickNumber(usage, 'output', undefined)
  const totalTokens = pickNumber(usage, 'totalTokens', undefined)
  if (inputTokens <= 0 && cacheCreationInputTokens <= 0 && cacheReadInputTokens <= 0 && (outputTokens ?? 0) <= 0 && (totalTokens ?? 0) <= 0)
    return null
  return {
    inputTokens,
    cacheCreationInputTokens,
    cacheReadInputTokens,
    ...(outputTokens !== undefined ? { outputTokens } : {}),
    ...(totalTokens !== undefined && totalTokens > 0 ? { contextTokens: totalTokens } : {}),
  }
}
