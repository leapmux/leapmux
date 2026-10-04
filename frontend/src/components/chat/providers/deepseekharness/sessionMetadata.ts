import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { DEEPSEEK_HARNESS_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { pickObject } from '~/lib/jsonPick'
import { deepseekHarnessEventData } from './protocol'

function tokenCount(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : undefined
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
  return {
    inputTokens,
    cacheCreationInputTokens,
    cacheReadInputTokens,
    ...(outputTokens !== undefined ? { outputTokens } : {}),
    ...(total !== undefined ? { contextTokens: total } : {}),
  }
}
