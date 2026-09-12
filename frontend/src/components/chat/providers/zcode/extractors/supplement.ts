import type { ParsedMessageContent } from '~/lib/messageParser'
import { ZCODE_EVENT, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { zcodeEnvelope } from './toolCommon'

/** Resolve omitted stream arguments for display. Keep the original provider object unchanged. */
export function resolveZCodeMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const event = zcodeEnvelope(original)
  const supplemental = zcodeEnvelope(parsed.supplementalContent)
  if (!event || !supplemental || event.type !== ZCODE_EVENT.ToolUpdated || supplemental.type !== event.type
    || event.payload.kind !== ZCODE_TOOL_KIND.Scheduled || supplemental.payload.kind !== event.payload.kind
    || !pickString(event.payload, 'toolCallId') || supplemental.payload.toolCallId !== event.payload.toolCallId) {
    return original
  }
  const current = event.payload.input
  if (current != null && !isObject(current))
    return original
  const input = pickObject(supplemental.payload, 'input')
  if (!input || !Object.keys(input).some(key => !current || !Object.hasOwn(current, key)))
    return original
  return { ...original, payload: { ...event.payload, input: { ...input, ...current } } }
}
