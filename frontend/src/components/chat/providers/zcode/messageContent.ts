import { ZCODE_EVENT, ZCODE_QUERY_SOURCE, ZCODE_STOP_REASON } from '~/generated/contracts/zcode-protocol'
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
 * Whether a `session.updated` payload is the RUNNING TURN's reply, and so the one
 * that carries assistant text for the transcript and usage for the context meter.
 *
 * `session.updated` is the app-server's catch-all: every internal event with no
 * explicit wire mapping becomes one, so the same type carries request telemetry,
 * per-iteration counters, background-task lifecycle, and every generation the
 * model made. The reply is a finished generation (`content` beside a `stopReason`)
 * whose `querySource` is the main turn. A payload with no `querySource` states its
 * role through the stop reason: `tool_internal` marks a side query. A `querySource`
 * of any other JSON type is not the reply either. The worker replays the same
 * rule; the shared corpus is testdata/zcode_model_response_conformance.json.
 */
export function zcodeIsMainReply(payload: Record<string, unknown>): boolean {
  if (typeof payload.content !== 'string' || pickString(payload, 'stopReason') === '')
    return false
  if (payload.querySource !== undefined)
    return zcodeIsMainQuery(payload)
  return pickString(payload, 'stopReason') !== ZCODE_STOP_REASON.ToolInternal
}

/**
 * Whether a payload's `querySource` identifies the model call of the running turn.
 *
 * An absent key states no side query. A present key must be the string
 * `main_turn`: any other word, and any other JSON type, is a side query. A network
 * status has no `stopReason`, so this is its whole rule. The worker's
 * `zcodeIsMainQuery` applies the same rule.
 */
export function zcodeIsMainQuery(payload: Record<string, unknown>): boolean {
  return payload.querySource === undefined || payload.querySource === ZCODE_QUERY_SOURCE.MainTurn
}
