import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { retainedRowIsFinal } from '../registry'
import { clineToolFinish, clineToolStart } from './extractors/toolCommon'

/**
 * Cline's span role: a `tool.started` row is the request, and a `tool.finished` row is
 * the result.
 *
 * A turn that ended while a call ran stores the call's start again as its closing row,
 * so the completion is what separates the two copies.
 */
export function clineSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  if (clineToolFinish(parsed.parentObject))
    return 'result'
  if (!clineToolStart(parsed.parentObject))
    return 'other'
  return retainedRowIsFinal(parsed.completion) ? 'result' : 'request'
}

/** The span side one row needs beside it: a request needs its result, and a result its request. */
export function clineRelatedMessages(parsed: ResolvedMessageContent) {
  const role = clineSpanRole(parsed)
  return role === 'result' ? ['request'] as const : role === 'request' ? ['result'] as const : []
}
