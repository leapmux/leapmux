import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { OH_MY_PI_EVENT } from '~/generated/contracts/ohmypi-protocol'
import { pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'

/**
 * omp's span role: the flat envelope `type` tells the request from the result.
 *
 * A turn that ended while a call ran stores the call's START frame again as its
 * closing row, so the completion is what separates the two copies.
 */
export function ohMyPiSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const type = pickString(parsed.parentObject, 'type')
  if (type === OH_MY_PI_EVENT.ToolExecutionEnd)
    return 'result'
  if (type !== OH_MY_PI_EVENT.ToolExecutionStart)
    return 'other'
  return retainedRowIsFinal(parsed.completion)
    ? 'result'
    : 'request'
}

/**
 * Whether one row draws from its span's request: a `tool_execution_end` frame states no
 * arguments and no intent, so it reads them from the paired start frame. A start frame,
 * also one that a stopped turn retained, states its own.
 */
export function ohMyPiNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return pickString(parsed.parentObject, 'type') === OH_MY_PI_EVENT.ToolExecutionEnd
}
