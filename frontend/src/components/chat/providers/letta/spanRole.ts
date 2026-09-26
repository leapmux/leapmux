import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { LETTA_DELTA_KIND } from '~/generated/contracts/letta-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * Letta's span role: a `client_tool_start` delta is the request, and a
 * `tool_return_message` or `client_tool_end` delta is the result.
 *
 * The worker persists the stream_delta's own payload object. A raw frame wraps
 * it under `payload`, so normalize before reading.
 */
export function lettaSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const parent = parsed.parentObject
  const nested = isObject(parent) ? pickObject(parent, 'payload') : null
  const source = nested && Object.keys(nested).length > 0 ? nested : parent
  const messageType = pickString(source, 'message_type')
  if (messageType === LETTA_DELTA_KIND.ToolReturnMessage || messageType === LETTA_DELTA_KIND.ClientToolEnd)
    return 'result'
  if (messageType === LETTA_DELTA_KIND.ClientToolStart)
    return 'request'
  return 'other'
}

/** The span side one row needs beside it. */
export function lettaRelatedMessages(parsed: ResolvedMessageContent) {
  const role = lettaSpanRole(parsed)
  return role === 'result' ? ['request'] as const : role === 'request' ? ['result'] as const : []
}
