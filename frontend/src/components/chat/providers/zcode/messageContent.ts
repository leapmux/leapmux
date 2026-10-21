import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { zcodeEnvelope } from './extractors/toolCommon'

/**
 * The assistant text a persisted ZCode row carries.
 *
 * The carrier is a model-response `session.updated`, whose payload states the whole
 * reply under `content`. A turn that only called tools reports `content: ""` with a
 * `tool-calls` stop reason, so an empty string is the normal case and not a parse
 * failure.
 */
export function zcodeAssistantText(parsed: unknown): string {
  const envelope = zcodeEnvelope(parsed)
  if (!envelope || envelope.type !== ZCODE_EVENT.SessionUpdated)
    return ''
  return pickString(envelope.payload, 'content')
}
