import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND } from '~/generated/contracts/letta-protocol'
import { pickString } from '~/lib/jsonPick'
import { retainedRowIsFinal } from '../registry'
import { isLettaToolProgress, lettaReturnedData, lettaToolPayload } from './toolOutput'

/**
 * Letta's span role: `client_tool_start` and `tool_call_message` are requests.
 * Current output windows and empty lifecycle ends supply no result.
 * Actual returned data keeps the result role.
 *
 * A turn or a process that ends while a call runs gets no answer from Letta Code, so
 * the Worker stores the call's opening frame AGAIN as its closing row. The completion
 * column is what separates the two copies (see retainedRowIsFinal).
 *
 * The worker stores one native delta for each tool call. Read its fields directly.
 */
export function lettaSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const source = lettaToolPayload(parsed.parentObject)
  const messageType = pickString(source, LETTA_DELTA_FIELD.MessageType)
  if (messageType === LETTA_DELTA_KIND.ToolReturnMessage) {
    return isLettaToolProgress(source)
      ? 'none'
      : 'result'
  }
  if (messageType === LETTA_DELTA_KIND.ClientToolEnd) {
    return lettaReturnedData(source).kind === 'present'
      ? 'result'
      : 'none'
  }
  if (isLettaCallFrame(messageType)) {
    return retainedRowIsFinal(parsed.completion)
      ? 'result'
      : 'request'
  }
  return 'other'
}

/**
 * Whether one row draws from its span's request: a native answer can omit the tool
 * name and the arguments, so it reads them from its request. A call frame states its
 * own call, also the copy of it that closes a call its turn outlived.
 */
export function lettaNeedsRequest(parsed: ResolvedMessageContent): boolean {
  const messageType = pickString(lettaToolPayload(parsed.parentObject), LETTA_DELTA_FIELD.MessageType)
  return !isLettaCallFrame(messageType) && lettaSpanRole(parsed) === 'result'
}

/** Whether a frame of this type opens a call: it states the call's name and arguments. */
function isLettaCallFrame(messageType: string): boolean {
  return messageType === LETTA_DELTA_KIND.ClientToolStart || messageType === LETTA_DELTA_KIND.ToolCallMessage
}
