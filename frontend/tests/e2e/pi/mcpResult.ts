import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'

/** Require one actual native completion with exact tool and call identity. */
export function piMcpResultFromSnapshot(snapshot: NativeMessageSnapshot, callId: string, toolName: string): { result: Record<string, unknown>, failed: boolean } {
  if (!callId || !toolName || !snapshot.agentId || !snapshot.agentSessionId)
    throw new Error('The native Pi result requires a started session and exact tool identity.')
  const matches = snapshot.messages
    .map(message => ({ message, frame: nativeMessageBody(message) }))
    .filter(({ frame }) => isObject(frame) && frame.type === 'tool_execution_end' && frame.toolCallId === callId && frame.toolName === toolName)
  if (matches.length !== 1)
    throw new Error(`The native Pi transcript contains ${matches.length} completed results for ${callId}.`)
  const { message, frame } = matches[0]!
  if (message.spanId !== callId || message.spanType !== toolName)
    throw new Error('The native Pi completion has mismatched Worker span identity.')
  if (!isObject(frame) || !isObject(frame.result) || typeof frame.isError !== 'boolean')
    throw new Error('The native Pi completion lacks its result or failure status.')
  return { result: frame.result, failed: frame.isError }
}

export async function readPiMcpResult(context: ManagedNativeScenarioContext, callId: string, toolName: string) {
  const agent = await currentNativeAgent(context)
  return piMcpResultFromSnapshot(await readNativeMessageSnapshot(context, agent.id), callId, toolName)
}
