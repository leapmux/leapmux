import type { ResultDividerModel } from '../../registry'
import { ZCODE_EVENT, ZCODE_RESULT } from '~/generated/contracts/zcode-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { formatDuration } from '../../../rendererUtils'
import { zcodeEnvelope } from '../extractors/toolCommon'

/**
 * A ZCode turn end as the provider-neutral divider model.
 *
 * The two ends carry different shapes: `turn.completed` states a `resultType` and a
 * millisecond `duration`, while `turn.failed` states an `error` object. Returns null
 * for any other row so the caller falls back to the raw-JSON renderer.
 */
export function zcodeResultDivider(parsed: unknown): ResultDividerModel | null {
  const envelope = zcodeEnvelope(parsed)
  if (!envelope)
    return null
  const payload = envelope.payload

  if (envelope.type === ZCODE_EVENT.TurnCompleted) {
    const duration = pickNumber(payload, 'duration')
    if (pickString(payload, 'resultType') === ZCODE_RESULT.Cancelled) {
      return { label: 'Turn cancelled', isError: true }
    }
    return { label: duration != null ? `Took ${formatDuration(duration)}` : 'Turn ended' }
  }

  if (envelope.type === ZCODE_EVENT.TurnFailed) {
    const error = pickObject(payload, 'error')
    const message = pickString(error, 'message')
    const code = pickString(error, 'code') || pickString(error, 'type')
    const detail = pickString(error, 'detail')
    const head = code ? `Turn failed (${code})` : 'Turn failed'
    const model: ResultDividerModel = {
      label: message ? `${head} — ${message}` : head,
      isError: true,
    }
    // `detail` is the app-server's long-form explanation (a provider response body,
    // a stack). It goes in the detail block rather than the label so a multi-line
    // value does not stretch the rule.
    if (detail)
      model.detail = detail
    return model
  }

  return null
}
