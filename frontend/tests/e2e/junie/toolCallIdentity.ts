import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { isObject } from '../../../src/lib/jsonPick'
import { nativeMessageBody } from '../helpers/nativeMessages'

/** Match Junie's ACP terminal call to the exact command that the scripted model requested. */
export function junieNativeOutputFileCallId(snapshot: NativeMessageSnapshot, command: string, workingDirectory: string): string {
  if (!snapshot.agentId || !snapshot.agentSessionId || !command.trim() || !workingDirectory)
    throw new Error('The Junie full tool output requires its started native session and exact command.')
  const ids = new Set<string>()
  for (const message of snapshot.messages) {
    const frame = nativeMessageBody(message)
    if (!isObject(frame) || frame.sessionUpdate !== 'tool_call' || frame.kind !== 'execute'
      || !isObject(frame.rawInput) || frame.rawInput.command !== command || frame.rawInput.cwd !== workingDirectory) {
      continue
    }
    if (typeof frame.toolCallId !== 'string' || !frame.toolCallId || message.spanId !== frame.toolCallId || message.spanType !== 'execute')
      throw new Error('The Junie full tool output has inconsistent native tool identity.')
    ids.add(frame.toolCallId)
  }
  if (ids.size !== 1)
    throw new Error('The Junie full tool output requires one actual ACP call for the exact scripted command.')
  return [...ids][0]!
}
