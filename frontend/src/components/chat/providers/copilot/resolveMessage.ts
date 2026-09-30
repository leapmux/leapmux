import type { ParsedMessageContent } from '~/lib/messageParser'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { copilotEventData } from './protocol'

/** Read model-facing images from a live Copilot tool result for display. */
export function resolveCopilotMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const data = copilotEventData(original, COPILOT_EVENT.ToolCompleted)
  const result = pickObject(data, 'result')
  const binary = result?.binaryResultsForLlm
  if (!original || !result || !Array.isArray(binary))
    return original
  if (result.contents != null && !Array.isArray(result.contents))
    return original

  const contents = Array.isArray(result.contents) ? result.contents : []
  const present = new Set<string>()
  for (const item of contents) {
    if (isObject(item) && item.type === 'image')
      present.add(`${pickString(item, 'mimeType')}\0${pickString(item, 'data')}`)
  }
  const images: Array<{ type: 'image', data: string, mimeType: string }> = []
  for (const item of binary) {
    if (!isObject(item) || item.type !== 'image')
      continue
    const encoded = pickString(item, 'data')
    const mimeType = pickString(item, 'mimeType')
    if (!encoded || !mimeType.startsWith('image/'))
      continue
    const identity = `${mimeType}\0${encoded}`
    if (present.has(identity))
      continue
    present.add(identity)
    images.push({ type: 'image', data: encoded, mimeType })
  }
  if (images.length === 0)
    return original

  const params = pickObject(original, 'params')
  const event = pickObject(params, 'event')
  if (!params || !event)
    return original
  return {
    ...original,
    params: {
      ...params,
      event: {
        ...event,
        data: { ...data, result: { ...result, contents: [...contents, ...images] } },
      },
    },
  }
}
