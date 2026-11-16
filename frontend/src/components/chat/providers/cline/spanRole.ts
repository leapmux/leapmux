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
  return retainedRowIsFinal(parsed.completion)
    ? 'result'
    : 'request'
}

/**
 * Whether one row draws from its span's request: a `tool.finished` row states the
 * tool's name but not its arguments, so it reads them from the start. A start row,
 * also one that a stopped turn retained, reads its own `tool.started` event.
 */
export function clineNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return clineToolFinish(parsed.parentObject) !== null
}
