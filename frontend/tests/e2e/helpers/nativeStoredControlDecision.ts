import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeControlFrame } from './nativeControlWatch'
import type { NativeMessageSnapshot } from './nativeMessages'
import { isDeepStrictEqual } from 'node:util'
import { parsePersistedControlResponse } from '../../../src/components/chat/persistedControlResponse'
import { MarkType, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '../../../src/lib/messageParser'

/**
 * One saved answer to a native control request, as the Worker stored it.
 *
 * The Worker writes this row after it delivers the answer. The provider spec reads the
 * native fields of `request` and `response`. This module reads no provider field.
 */
export interface NativeStoredControlDecision {
  /** The Worker row that holds the answer. */
  message: AgentChatMessage
  /** The Worker request ID that the answer belongs to. */
  requestId: string
  /** The token of the request instance that the answer belongs to. */
  claimToken: string
  /** The complete native request, which the Worker stores beside the answer. */
  request: Record<string, unknown>
  /** The native answer that the Worker delivered to the provider. */
  response: Record<string, unknown>
}

/**
 * Select the one native control request that a Worker watch observed.
 *
 * It returns the first frame of that request. It throws in each of these cases:
 *
 * - The watch observed no request.
 * - The watch observed a second request ID.
 * - The frames of the one request disagree about its payload. The Worker gives a
 *   changed payload a new instance, and that instance is not the request that the
 *   caller saw.
 */
export function onlyObservedNativeControl(frames: readonly NativeControlFrame[]): NativeControlFrame {
  const first = frames[0]
  if (!first)
    throw new Error('The Worker watch observed no native control request.')
  if (!first.requestId.trim())
    throw new Error('The observed native control request has no request ID.')
  for (const frame of frames) {
    if (frame.requestId !== first.requestId)
      throw new Error(`The Worker sent a second native control request ${frame.requestId} after ${first.requestId}.`)
    if (!isDeepStrictEqual(frame.payload, first.payload))
      throw new Error(`The native control request ${first.requestId} changed its payload.`)
  }
  return first
}

/**
 * Read the one saved answer to an observed request in the root native session.
 *
 * The answer must satisfy each of these conditions:
 *
 * - The Worker wrote it as a user control-response row.
 * - It belongs to the root scope of the snapshot's native session.
 * - It holds the native request, the native answer, and the claim token.
 * - It is the only row for the request ID.
 *
 * Any other row that states the request ID makes the read fail. A foreign or
 * malformed row is not skipped, because a skipped row would hide a defect.
 */
export function readNativeStoredControlDecision(snapshot: NativeMessageSnapshot, requestId: string): NativeStoredControlDecision {
  if (!snapshot.agentId.trim() || !snapshot.agentSessionId.trim() || !requestId.trim())
    throw new Error('The saved native decision read requires an agent ID, a native session ID, and an observed request ID.')
  const decisions: NativeStoredControlDecision[] = []
  for (const message of snapshot.messages) {
    const saved = parsePersistedControlResponse(parseMessageContent(message))
    if (!saved || saved.requestId !== requestId)
      continue
    if (message.source !== MessageSource.USER || message.markType !== MarkType.CONTROL_RESPONSE)
      throw new Error(`The row for native request ${requestId} is not a saved control response.`)
    if (message.agentSessionId !== snapshot.agentSessionId || message.depth !== 0 || message.parentSpanId !== '')
      throw new Error(`The saved decision for native request ${requestId} is outside the root scope of native session ${snapshot.agentSessionId}.`)
    if (!message.id.trim() || message.seq < 0n)
      throw new Error(`The saved decision for native request ${requestId} has no valid Worker row identity.`)
    if (!saved.claimToken.trim() || !saved.request || !saved.response)
      throw new Error(`The saved decision for native request ${requestId} lacks its native request, its native answer, or its claim token.`)
    decisions.push({ message, requestId: saved.requestId, claimToken: saved.claimToken, request: saved.request, response: saved.response })
  }
  const [decision, ...others] = decisions
  if (!decision || others.length > 0)
    throw new Error(`The Worker holds ${decisions.length} saved decisions for native request ${requestId}. Exactly one must exist.`)
  return decision
}
