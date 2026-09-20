import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { CODEX_METHOD } from '~/generated/contracts/codex-protocol'
import { pickNumber, pickObject } from '~/lib/jsonPick'
import { getInnerMessage } from '~/lib/messageParser'

/** Codex context usage from a `thread/tokenUsage/updated` notification (`params.tokenUsage.last`). */
export function codexContextUsageFromNotification(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const inner = getInnerMessage(parsed)
  if (!inner || inner.method !== CODEX_METHOD.ThreadTokenUsageUpdated)
    return null
  const tokenUsage = pickObject(pickObject(inner, 'params'), 'tokenUsage')
  const last = pickObject(tokenUsage, 'last')
  const inputTokens = pickNumber(last, 'inputTokens')
  if (inputTokens === null)
    return null
  const cached = pickNumber(last, 'cachedInputTokens', 0)
  const contextUsage: ContextUsageInfo = {
    inputTokens: Math.max(inputTokens - cached, 0),
    cacheCreationInputTokens: 0,
    cacheReadInputTokens: cached,
  }
  const contextWindow = pickNumber(tokenUsage, 'modelContextWindow')
  if (contextWindow !== null)
    contextUsage.contextWindow = contextWindow
  return contextUsage
}
