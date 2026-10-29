import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickNumber, pickObject } from '~/lib/jsonPick'
import { zcodeEnvelope } from './extractors/toolCommon'
import { ZCODE_SESSION_UPDATE_ROWS, zcodeSessionUpdateKind } from './sessionUpdateKind'

/** Read native usage from a stored model response when the worker supplies no usage supplement. */
export function zcodeContextUsageFromMessage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.SessionUpdated)
    return null
  // Only a stored model response carries the reply's usage.
  if (ZCODE_SESSION_UPDATE_ROWS[zcodeSessionUpdateKind(envelope.payload)] !== 'reply')
    return null
  const usage = pickObject(envelope.payload, 'usage')
  if (!usage)
    return null
  const inputTokens = pickNumber(usage, 'inputTokens', 0)
  const outputTokens = pickNumber(usage, 'outputTokens', 0)
  const cacheReadInputTokens = pickNumber(usage, 'cacheReadTokens', 0)
  const cacheCreationInputTokens = pickNumber(usage, 'cacheWriteTokens', 0)
  if (inputTokens === 0 && outputTokens === 0 && cacheReadInputTokens === 0 && cacheCreationInputTokens === 0)
    return null
  const info: ContextUsageInfo = {
    inputTokens,
    cacheCreationInputTokens,
    cacheReadInputTokens,
    outputTokens,
  }
  const total = pickNumber(usage, 'totalTokens')
  if (total != null && total > 0)
    info.contextTokens = total
  return info
}
