import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext, NativeToolRowIdResolver } from '../helpers/nativeScenario'
import { isObject, pickObject } from '../../../src/lib/jsonPick'

import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'

/**
 * Read the native item id of the exact call id that the scripted model request
 * stated. Codewhale answers the model under its `provider_tool_use_id` while
 * its item frames carry the runtime's own `tool_use_id` as their span, so both
 * a proof that reads the Worker's stored frames and one that locates a browser
 * tool row need the native id.
 */
export function codewhaleNativeCallId(snapshot: NativeMessageSnapshot, scriptedCallId: string): string {
  if (!snapshot.agentId || !snapshot.agentSessionId || !scriptedCallId.trim())
    throw new Error('The Codewhale native call query requires its started native session and scripted call id.')
  const ids = new Set<string>()
  for (const message of snapshot.messages) {
    const frame = nativeMessageBody(message)
    if (!isObject(frame))
      continue
    const metadata = pickObject(pickObject(pickObject(frame, 'payload'), 'item'), 'metadata')
    if (!isObject(metadata) || metadata.provider_tool_use_id !== scriptedCallId)
      continue
    const toolUseID = metadata.tool_use_id
    if (typeof toolUseID !== 'string' || !toolUseID || message.spanId !== toolUseID || message.agentSessionId !== snapshot.agentSessionId)
      throw new Error('The Codewhale native tool identity is inconsistent.')
    ids.add(toolUseID)
  }
  if (ids.size !== 1)
    throw new Error('The Codewhale native call query requires one native item for the scripted call.')
  return [...ids][0]!
}

/**
 * The resolver the codewhale scenario context reads browser row ids through.
 *
 * A caller may state the scripted model id or an id a proof already resolved;
 * an id that already names a stored item resolves to itself.
 */
export function codewhaleToolRowIdResolver(context: Pick<ManagedNativeScenarioContext, 'leapmuxServer'>): NativeToolRowIdResolver {
  return async ({ callId, agentId }) => {
    const snapshot = await readNativeMessageSnapshot(context, agentId)
    for (const message of snapshot.messages) {
      if (message.spanId === callId)
        return callId
    }
    return codewhaleNativeCallId(snapshot, callId)
  }
}
