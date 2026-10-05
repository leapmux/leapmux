import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { DEEPSEEK_HARNESS_EVENT, DEEPSEEK_HARNESS_SUPPLEMENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject, pickObject } from '~/lib/jsonPick'
import { deepseekHarnessEventData } from './protocol'

function tokenCount(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined
}

/**
 * The native context window that the Worker stated for this message.
 * The native usage carries no window. The Worker takes it from the `request/context` event
 * and adds it to the supplement of each assistant block.
 */
function statedContextWindow(parsed: ParsedMessageContent): number | undefined {
  const supplement = isObject(parsed.supplementalContent) ? parsed.supplementalContent : undefined
  const tokens = tokenCount(supplement?.[DEEPSEEK_HARNESS_SUPPLEMENT.ContextWindow])
  return tokens !== undefined && tokens > 0 ? tokens : undefined
}

export function deepseekHarnessContextUsage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const data = deepseekHarnessEventData(parsed.parentObject, DEEPSEEK_HARNESS_EVENT.AssistantMessage)
  const usage = pickObject(data, 'usage')
  if (!usage)
    return null
  const inputTokens = tokenCount(usage.inputTokens)
  if (inputTokens === undefined)
    return null
  const cacheCreationInputTokens = tokenCount(usage.cacheWriteTokens) ?? 0
  const cacheReadInputTokens = tokenCount(usage.cacheReadTokens) ?? 0
  const outputTokens = tokenCount(usage.outputTokens)
  const total = tokenCount(usage.totalTokens)
  const contextWindow = statedContextWindow(parsed)
  return {
    inputTokens,
    cacheCreationInputTokens,
    cacheReadInputTokens,
    ...(outputTokens !== undefined ? { outputTokens } : {}),
    ...(total !== undefined ? { contextTokens: total } : {}),
    ...(contextWindow !== undefined ? { contextWindow } : {}),
  }
}
