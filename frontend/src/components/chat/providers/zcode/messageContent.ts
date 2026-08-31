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

/**
 * Whether a `session.updated` row is the MODEL-RESPONSE variant, and so the one that
 * carries assistant text.
 *
 * `session.updated` is the app-server's catch-all: every internal event with no
 * explicit wire mapping becomes one, so the same type carries request telemetry,
 * per-iteration counters, background-task lifecycle, and the assistant's reply. They
 * are told apart by field presence, and a model response is the one that states a
 * `stopReason` -- the only variant that reports a finished generation.
 */
export function zcodeIsModelResponse(payload: Record<string, unknown>): boolean {
  return typeof payload.content === 'string' && pickString(payload, 'stopReason') !== ''
}

/**
 * Whether a `session.updated` row is a background-task lifecycle update, which is
 * the one variant the worker turns into a registry row rather than a transcript one.
 */
export function zcodeIsBackgroundTask(payload: Record<string, unknown>): boolean {
  return pickString(payload, 'taskId') !== ''
}
