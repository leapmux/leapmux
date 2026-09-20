import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { pickNumber } from '~/lib/jsonPick'
import { copilotEvent } from './protocol'

/**
 * Copilot's context counter, for a row the worker's own normalization did not reach.
 *
 * The worker broadcasts the live counter from the same event, so this is the fallback
 * a replayed or unaugmented row takes.
 */
export function copilotContextUsage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const event = copilotEvent(parsed.parentObject)
  if (!event || event.type !== COPILOT_EVENT.SessionUsageInfo)
    return null
  const current = pickNumber(event.data, 'currentTokens')
  if (current == null || current <= 0)
    return null
  const info: ContextUsageInfo = { inputTokens: 0, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, outputTokens: 0, contextTokens: current }
  const limit = pickNumber(event.data, 'tokenLimit')
  if (limit != null && limit > 0)
    info.contextWindow = limit
  return info
}
