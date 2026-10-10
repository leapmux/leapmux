import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { isObject, pickObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody } from '../helpers/nativeMessages'

/**
 * Match Codewhale's native item to the exact call id that the scripted model
 * request stated. The runtime answers the model under its own `provider_tool_use_id`
 * while its item frames carry the runtime's `tool_use_id` as their span, so a
 * proof that reads the Worker's stored frames needs the native id.
 */
export function codewhaleNativeOutputCallId(snapshot: NativeMessageSnapshot, scriptedCallId: string): string {
  if (!snapshot.agentId || !snapshot.agentSessionId || !scriptedCallId.trim())
    throw new Error('The Codewhale native tool output requires its started native session and scripted call id.')
  const ids = new Set<string>()
  for (const message of snapshot.messages) {
    const frame = nativeMessageBody(message)
    if (!isObject(frame))
      continue
    const metadata = pickObject(pickObject(frame, 'payload'), 'item') && pickObject(pickObject(pickObject(frame, 'payload'), 'item'), 'metadata')
    if (!isObject(metadata) || metadata.provider_tool_use_id !== scriptedCallId)
      continue
    const toolUseID = metadata.tool_use_id
    if (typeof toolUseID !== 'string' || !toolUseID || message.spanId !== toolUseID || message.agentSessionId !== snapshot.agentSessionId)
      throw new Error('The Codewhale native tool output has inconsistent native tool identity.')
    ids.add(toolUseID)
  }
  if (ids.size !== 1)
    throw new Error('The Codewhale native tool output requires one native item for the scripted call.')
  return [...ids][0]!
}
