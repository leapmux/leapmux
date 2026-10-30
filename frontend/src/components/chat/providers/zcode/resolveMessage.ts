import type { ParsedMessageContent } from '~/lib/messageParser'
import { ZCODE_EVENT, ZCODE_RESPONSE_TEXT, ZCODE_SUPPLEMENT, ZCODE_SUPPLEMENT_PAYLOAD, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { zcodeEnvelope } from './extractors/toolCommon'
import { zcodeSessionUpdateKind } from './sessionUpdateKind'

/** Resolve stream arguments and accepted text coverage without changing the original provider object. */
export function resolveZCodeMessage(parsed: ParsedMessageContent): Record<string, unknown> | undefined {
  const original = parsed.parentObject
  const event = zcodeEnvelope(original)
  if (event?.type === ZCODE_EVENT.SessionUpdated && zcodeSessionUpdateKind(event.payload) === 'modelResponse') {
    const coverage = isObject(parsed.supplementalContent)
      ? pickObject(parsed.supplementalContent, ZCODE_SUPPLEMENT.ResponseText)
      : undefined
    const prefix = coverage?.[ZCODE_RESPONSE_TEXT.AcceptedPrefix]
    const text = event.payload.content
    if (typeof prefix === 'string' && typeof text === 'string' && text.startsWith(prefix)) {
      return {
        ...original,
        payload: {
          ...event.payload,
          content: text.slice(prefix.length),
        },
      }
    }
    return original
  }
  const supplemental = zcodeEnvelope(parsed.supplementalContent)
  if (!event || !supplemental || event.type !== ZCODE_EVENT.ToolUpdated || supplemental.type !== event.type
    || event.payload.kind !== ZCODE_TOOL_KIND.Scheduled || supplemental.payload.kind !== event.payload.kind
    || !pickString(event.payload, 'toolCallId') || supplemental.payload.toolCallId !== event.payload.toolCallId) {
    return original
  }
  const current = event.payload.input
  if (current != null && !isObject(current))
    return original
  const input = pickObject(supplemental.payload, ZCODE_SUPPLEMENT_PAYLOAD.Input)
  if (!input || !Object.keys(input).some(key => !current || !Object.hasOwn(current, key)))
    return original
  return {
    ...original,
    payload: {
      ...event.payload,
      input: {
        ...input,
        ...current,
      },
    },
  }
}
