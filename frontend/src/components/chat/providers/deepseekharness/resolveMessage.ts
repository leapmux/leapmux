import type { ParsedMessageContent } from '~/lib/messageParser'
import { DEEPSEEK_HARNESS_EVENT, DEEPSEEK_HARNESS_SUPPLEMENT } from '~/generated/contracts/deepseek-harness-protocol'
import { isObject } from '~/lib/jsonPick'
import { deepseekHarnessImageResults } from './imageResults'

/** Merge the native block identity into the display copy. */
export function resolveDeepseekHarnessMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const images = deepseekHarnessImageResults(parsed)
  if (images)
    return images
  const payload = parsed.parentObject
  if (!payload || payload.type !== DEEPSEEK_HARNESS_EVENT.AssistantMessage || !isObject(parsed.supplementalContent))
    return undefined
  const index = parsed.supplementalContent[DEEPSEEK_HARNESS_SUPPLEMENT.BlockIndex]
  if (typeof index !== 'number' || !Number.isSafeInteger(index) || index < 0)
    return undefined
  return { ...payload, [DEEPSEEK_HARNESS_SUPPLEMENT.BlockIndex]: index }
}
