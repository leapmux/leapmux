import type { DividerIR } from '../../../ir/divider'
import { ZCODE_EVENT, ZCODE_RESULT } from '~/generated/contracts/zcode-protocol'
import { pickNumber, pickObject, pickString } from '~/lib/jsonPick'
import { turnEndLabel } from '../../../turnEndLabel'
import { zcodeEnvelope } from './toolCommon'

/**
 * Why a turn ENDED in failure, in the app server's own result words.
 *
 * Each states a limit the turn reached rather than an error it hit, which is why
 * the reason reads as a sentence: the reader must know what to raise.
 */
const ZCODE_FAILED_RESULTS: ReadonlyMap<string, string> = new Map([
  [ZCODE_RESULT.ErrorMaxTurns, 'reached the turn limit'],
  [ZCODE_RESULT.ErrorMaxToolCalls, 'reached the tool-call limit'],
  [ZCODE_RESULT.ErrorMaxBudget, 'reached the budget limit'],
  [ZCODE_RESULT.ErrorDuringExecution, 'stopped during execution'],
])

/**
 * A ZCode turn end as the provider-neutral divider model.
 *
 * The two ends carry different shapes: `turn.completed` states a `resultType` and a
 * millisecond `duration`, while `turn.failed` states an `error` object. Returns null
 * for any other row so the caller falls back to the raw-JSON renderer.
 */
export function zcodeResultDivider(parsed: unknown): DividerIR | null {
  const envelope = zcodeEnvelope(parsed)
  if (!envelope)
    return null
  const payload = envelope.payload

  if (envelope.type === ZCODE_EVENT.TurnCompleted) {
    const duration = pickNumber(payload, 'duration')
    const resultType = pickString(payload, 'resultType')
    // A cancelled turn is not an error: the reader asked for it.
    if (resultType === ZCODE_RESULT.Cancelled)
      return { label: turnEndLabel('interrupted', { durationMs: duration }) }
    // `turn.completed` reports a FAILURE too. Every `error_*` result used to read
    // as a plain end in the ordinary colour, so a turn that hit its budget or its
    // turn cap looked exactly like one that finished its work.
    const failure = ZCODE_FAILED_RESULTS.get(resultType)
    if (failure)
      return { label: turnEndLabel('failed', { durationMs: duration, reason: failure }), isError: true }
    return { label: turnEndLabel('ended', { durationMs: duration }) }
  }

  if (envelope.type === ZCODE_EVENT.TurnFailed) {
    const error = pickObject(payload, 'error')
    const message = pickString(error, 'message')
    const code = pickString(error, 'code') || pickString(error, 'type')
    const detail = pickString(error, 'detail')
    const model: DividerIR = {
      label: turnEndLabel('failed', { qualifiers: [code], reason: message }),
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
