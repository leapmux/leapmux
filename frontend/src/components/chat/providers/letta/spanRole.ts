import type { ResolvedMessageContent } from '../../rowExtractionTypes'
import type { ToolSpanRole } from '~/lib/messageSpan'
import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND } from '~/generated/contracts/letta-protocol'
import { pickString } from '~/lib/jsonPick'
import { isLettaToolProgress, lettaReturnedData, lettaToolPayload } from './toolOutput'

/**
 * Letta's span role: `client_tool_start` and `tool_call_message` are requests.
 * Current output windows and empty lifecycle ends supply no result.
 * Actual returned data keeps the result role.
 *
 * The worker persists a native payload for each tool call. A raw frame wraps
 * its payload under `payload`, so normalize before reading.
 */
export function lettaSpanRole(parsed: ResolvedMessageContent): ToolSpanRole {
  const source = lettaToolPayload(parsed.parentObject)
  const messageType = pickString(source, LETTA_DELTA_FIELD.MessageType)
  if (messageType === LETTA_DELTA_KIND.ToolReturnMessage)
    return isLettaToolProgress(source) ? 'none' : 'result'
  if (messageType === LETTA_DELTA_KIND.ClientToolEnd)
    return lettaReturnedData(source).kind === 'present' ? 'result' : 'none'
  if (messageType === LETTA_DELTA_KIND.ClientToolStart || messageType === LETTA_DELTA_KIND.ToolCallMessage)
    return 'request'
  return 'other'
}

/** The span side one row needs beside it. */
export function lettaRelatedMessages(parsed: ResolvedMessageContent) {
  const role = lettaSpanRole(parsed)
  return role === 'result' ? ['request'] as const : role === 'request' ? ['result'] as const : []
}
