import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { COPILOT_EVENT, COPILOT_METHOD } from '../../../src/generated/contracts/copilot-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody } from '../helpers/nativeMessages'

/**
 * The error message that the Copilot runtime stores for a tool call whose permission the reader rejected.
 * The runtime of Copilot 1.0.87 holds the literal without a final period.
 * A comment in the Copilot SDK permission test quotes it with one, so the pattern accepts both.
 */
export const COPILOT_USER_REJECTION = /^The user rejected this tool call\.?$/

/** The native completion of one root Copilot tool call. */
export interface CopilotToolCompletion {
  success: boolean
  error?: { message: string, code?: string }
}

/** Read the one native completion of a root tool call in the native session of the snapshot. */
export function copilotToolCompletion(snapshot: NativeMessageSnapshot, callId: string): CopilotToolCompletion {
  if (!callId || snapshot.agentId.trim() === '' || snapshot.agentSessionId.trim() === '')
    throw new Error('The native Copilot completion requires an exact agent, session, and call ID.')
  const completions: CopilotToolCompletion[] = []
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId || message.spanId !== callId)
      continue
    const frame = nativeMessageBody(message)
    const params = isObject(frame) && frame.method === COPILOT_METHOD.SessionEvent && isObject(frame.params) ? frame.params : undefined
    const event = isObject(params?.event) ? params.event : undefined
    if (event?.type !== COPILOT_EVENT.ToolCompleted)
      continue
    const data = isObject(event.data) ? event.data : undefined
    if (params?.sessionId !== snapshot.agentSessionId || data?.toolCallId !== callId)
      throw new Error('The paired native Copilot completion identifies another session or call.')
    if (event.agentId !== undefined && event.agentId !== '')
      throw new Error('The paired native Copilot completion belongs to a subagent.')
    if (typeof data.success !== 'boolean')
      throw new Error('The native Copilot completion states no success value.')
    const error = data.error
    if (error === undefined) {
      completions.push({ success: data.success })
      continue
    }
    if (!isObject(error) || typeof error.message !== 'string' || (error.code !== undefined && typeof error.code !== 'string'))
      throw new Error('The native Copilot completion holds a malformed error.')
    completions.push({
      success: data.success,
      error: { message: error.message, ...(typeof error.code === 'string' ? { code: error.code } : {}) },
    })
  }
  const completion = completions[0]
  if (completions.length !== 1 || !completion)
    throw new Error(`The native Copilot call ${callId} has ${completions.length} completions. Exactly one is required.`)
  return completion
}
