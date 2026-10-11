import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { retainedRowIsFinal } from '../registry'
import { ampToolResult, ampToolUse } from './extractors/toolCommon'

/**
 * Amp's span role: an assistant row with a `tool_use` block is the request, and a user
 * row with a `tool_result` block is the result.
 *
 * A turn that ended while a call ran stores the call's request row again as its
 * closing row, so the completion is what separates the two copies.
 */
export function ampSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  if (ampToolResult(parsed.parentObject))
    return 'result'
  if (!ampToolUse(parsed.parentObject))
    return 'other'
  return retainedRowIsFinal(parsed.completion) ? 'result' : 'request'
}

/** The span side one row needs beside it: a request needs its result, and a result its request. */
export function ampRelatedMessages(parsed: ResolvedMessageContent) {
  const role = ampSpanRole(parsed)
  return role === 'result' ? ['request'] as const : role === 'request' ? ['result'] as const : []
}
