import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { DROID_NOTIFICATION_FIELD, DROID_TOOL_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'

/**
 * Droid's span role: a `tool_call` notification is the request, and a
 * `tool_result` notification is the result.
 */
export function droidSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const parent = parsed.parentObject
  const type = pickString(parent, DROID_NOTIFICATION_FIELD.Type)
  if (type === DROID_TOOL_NOTIFICATION.ToolResult)
    return 'result'
  // A `tool_call` notification that LeapMux stored with a completion is the final row of
  // its span (see retainedRowIsFinal). The Droid Worker stores each `tool_call` once with
  // no completion today, so this rule is the defensive one that every provider keeps.
  if (type === DROID_TOOL_NOTIFICATION.ToolCall) {
    return retainedRowIsFinal(parsed.completion)
      ? 'result'
      : 'request'
  }
  return 'other'
}

/**
 * A native result can omit its tool name and arguments, so it reads them from its
 * request. A request, and the closing copy of it that a turn's end leaves, draws from its
 * own notification only.
 */
export function droidNeedsRequest(parsed: ResolvedMessageContent): boolean {
  return pickString(parsed.parentObject, DROID_NOTIFICATION_FIELD.Type) === DROID_TOOL_NOTIFICATION.ToolResult
}
