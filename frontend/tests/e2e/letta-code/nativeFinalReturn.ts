import { LETTA_DELTA_FIELD, LETTA_DELTA_KIND, LETTA_TOOL_OUTPUT } from '../../../src/generated/contracts/letta-protocol'
import { isObject } from '../../../src/lib/jsonPick'

/** Match the final reference-bearing native return, while earlier chunks keep the same call ID. */
export function lettaNativeFinalReturn(frames: readonly unknown[], callId: string, modelText: string): Record<string, unknown> {
  if (!callId || !modelText)
    throw new Error('The native Letta native result requires its exact call and model result.')
  const results = frames.filter(isObject).filter(frame => frame.message_type === LETTA_DELTA_KIND.ToolReturnMessage && !lettaNativeProgressReturn(frame)
    && frame[LETTA_DELTA_FIELD.ToolCallID] === callId && frame[LETTA_DELTA_FIELD.Status] === 'success'
    && frame[LETTA_DELTA_FIELD.ToolReturn] === modelText)
  const result = results.length === 1 ? results[0] : undefined
  if (!result)
    throw new Error('The native Letta native result has no unique final return for the exact model result.')
  return result
}

/** Match the exact native streaming ID. Other synthetic IDs keep their final role. */
export function lettaNativeProgressReturn(frame: Record<string, unknown>): boolean {
  const callId = frame[LETTA_DELTA_FIELD.ToolCallID]
  return typeof callId === 'string' && callId !== '' && frame[LETTA_DELTA_FIELD.MessageType] === LETTA_DELTA_KIND.ToolReturnMessage
    && frame[LETTA_DELTA_FIELD.ID] === LETTA_TOOL_OUTPUT.StreamIDPrefix + callId
}
