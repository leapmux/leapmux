import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { DROID_NOTIFICATION_FIELD, DROID_TOOL_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { pickString } from '~/lib/jsonPick'

/**
 * Droid's span role: a `tool_call` notification is the request, and a
 * `tool_result` notification is the result.
 */
export function droidSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const parent = parsed.parentObject
  const type = pickString(parent, DROID_NOTIFICATION_FIELD.Type)
  if (type === DROID_TOOL_NOTIFICATION.ToolResult)
    return 'result'
  if (type === DROID_TOOL_NOTIFICATION.ToolCall)
    return 'request'
  return 'other'
}

/** The span side one row needs beside it. */
export function droidRelatedMessages(parsed: ResolvedMessageContent) {
  const role = droidSpanRole(parsed)
  return role === 'result' ? ['request'] as const : role === 'request' ? ['result'] as const : []
}
