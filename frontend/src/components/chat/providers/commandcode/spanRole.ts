import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { COMMAND_CODE_EVENT } from '~/generated/contracts/commandcode-protocol'
import { pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'
import { commandCodeEvent } from './protocol'

export const COMMAND_CODE_TOOL_RESULTS: ReadonlySet<string> = new Set([
  COMMAND_CODE_EVENT.ToolCompleted,
  COMMAND_CODE_EVENT.ToolErrored,
  COMMAND_CODE_EVENT.ToolDenied,
  COMMAND_CODE_EVENT.ToolHookBlocked,
])

/** The type of the native tool event in `parsed`, or undefined for a frame of no call. */
function commandCodeToolEventType(parsed: ResolvedMessageContent): string | undefined {
  const event = commandCodeEvent(parsed.parentObject)
  return event && pickString(event, 'toolCallId')
    ? pickString(event, 'type')
    : undefined
}

export function commandCodeSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const type = commandCodeToolEventType(parsed)
  if (type === undefined)
    return 'other'
  // A turn that ends while the call runs stores the queued frame AGAIN, with a
  // completion, as the closing row. The completion column separates the two copies.
  if (type === COMMAND_CODE_EVENT.ToolQueued) {
    return retainedRowIsFinal(parsed.completion)
      ? 'result'
      : 'request'
  }
  return COMMAND_CODE_TOOL_RESULTS.has(type)
    ? 'result'
    : 'other'
}

/**
 * A finished frame states no tool arguments: they are on the queued request. A queued
 * request, and the closing copy of it that a turn's end leaves, draws from its own frame
 * only.
 */
export function commandCodeNeedsRequest(parsed: ResolvedMessageContent): boolean {
  const type = commandCodeToolEventType(parsed)
  return type !== undefined && COMMAND_CODE_TOOL_RESULTS.has(type)
}
