import type { ParsedMessageContent } from '~/lib/messageParser'
import type { ContextUsageInfo } from '~/models/agentSession'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickNumber, pickObject } from '~/lib/jsonPick'
import { zcodeEnvelope } from './extractors/toolCommon'
import { zcodeIsMainReply } from './messageContent'

/**
 * ZCode context usage from a model-response `session.updated`.
 *
 * A fallback for a raw or unaugmented row: the worker normalizes usage into a
 * top-level `context_usage` the shared reader prefers, and only an event that
 * bypassed it reaches here. `contextWindow` is the model's own limit and rides along
 * so the fill gauge has a denominator.
 */
export function zcodeContextUsageFromMessage(parsed: ParsedMessageContent): ContextUsageInfo | null {
  const envelope = zcodeEnvelope(parsed.parentObject)
  if (!envelope || envelope.type !== ZCODE_EVENT.SessionUpdated)
    return null
  // A side query's usage is not what the turn cost the context. Only the main
  // reply's counts belong on the meter.
  if (!zcodeIsMainReply(envelope.payload))
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
  const info: ContextUsageInfo = { inputTokens, cacheCreationInputTokens, cacheReadInputTokens, outputTokens }
  const total = pickNumber(usage, 'totalTokens')
  if (total != null && total > 0)
    info.contextTokens = total
  return info
}
