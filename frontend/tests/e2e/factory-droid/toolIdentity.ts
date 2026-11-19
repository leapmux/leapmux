import type { MockModelToolCall } from '../helpers/mockModelScript'
import type { NativeContextFixtures, NativeToolRowIdQuery } from '../helpers/nativeScenario'
import { isDeepStrictEqual } from 'node:util'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { droidToolCallId } from '../helpers/providerToolCalls'

/** Locate the one scripted call whose native execution the test inspects. */
export function scriptedDroidCall(calls: readonly MockModelToolCall[], id: string): MockModelToolCall {
  const matches = calls.filter(call => call.id === droidToolCallId(id))
  if (matches.length !== 1)
    throw new Error('The Droid receipt requires one exact scripted call.')
  return matches[0]!
}

/** Resolve the live Worker row by its native tool name and exact input. */
export function droidToolRowIdFromFrames(frames: readonly unknown[], expected: MockModelToolCall): string {
  const ids = new Set(frames.filter(isObject).filter(frame => frame.type === 'tool_call').map(frame => frame.toolUse).filter(isObject).filter(call => call.name === expected.name && isDeepStrictEqual(call.input, expected.arguments)).map(call => call.id))
  if (ids.size !== 1)
    throw new Error('The Droid live row requires one exact native tool call.')
  const id = [...ids][0]
  if (typeof id !== 'string' || !id)
    throw new Error('The Droid live row has no native tool identity.')
  return id
}

/** The model response and Worker transcript retain separate sides of native ID regeneration. */
export async function droidToolRowId(context: NativeContextFixtures, query: NativeToolRowIdQuery): Promise<string> {
  const snapshot = await readNativeMessageSnapshot(context, query.agentId)
  const frames = snapshot.messages.map(nativeMessageBody)
  // A native output receipt already resolved its model call ID. Verify the
  // native identity in this agent's transcript before keeping that ID.
  if (frames.some(frame => isObject(frame) && frame.type === 'tool_call' && isObject(frame.toolUse) && frame.toolUse.id === query.callId))
    return query.callId
  const status = await context.modelScript.status()
  const expected = scriptedDroidCall(status.requests.flatMap(request => request.response?.toolCalls ?? []), query.callId)
  return droidToolRowIdFromFrames(frames, expected)
}
