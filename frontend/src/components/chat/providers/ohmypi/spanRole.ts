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
  return retainedRowIsFinal(parsed.completion) ? 'result' : 'request'
}

/** The span side one row needs beside it: a request needs its result, and a result its request. */
export function ohMyPiRelatedMessages(parsed: ResolvedMessageContent) {
  const role = ohMyPiSpanRole(parsed)
  return role === 'result' ? ['request'] as const : role === 'request' ? ['result'] as const : []
}
