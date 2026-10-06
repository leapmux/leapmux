import type { MockModelMatcher } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { GOOSE_SUBAGENT } from '../../../src/generated/contracts/goose-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'

/**
 * Match the own turn of a Goose child from the start of its task.
 * Goose 1.52 opens the message of a subagent with a `Subagent ID: <id>` line of its own, which the matcher admits.
 * The anchor keeps a parent turn that quotes the task in its delegate call from matching.
 */
export function gooseChildTaskMatcher(task: string): MockModelMatcher {
  if (task.trim() === '')
    throw new Error('The native Goose child matcher requires a nonempty task.')
  return { user: `^(?:Subagent ID: [^\\n]*\\n+)?${escapeRegExp(task)}` }
}

/** Correlate the native Goose delegate frame with its exact child prompt. */
export function gooseChildTaskId(snapshot: NativeMessageSnapshot, spawnCallId: string, prompt: string): string {
  if (snapshot.agentSessionId.trim() === '')
    throw new Error('The native Goose child identity requires a nonempty session ID.')
  if (!spawnCallId || prompt.trim() === '')
    throw new Error('The native Goose child identity requires a call ID and prompt.')
  let found = false
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId)
      continue
    const body = nativeMessageBody(message)
    if (!isObject(body))
      throw new Error('The native Goose parent frame must contain an object.')
    if (body.sessionUpdate !== 'tool_call' || body.toolCallId !== spawnCallId)
      continue
    const meta = body._meta
    const tool = isObject(meta) && isObject(meta.goose) ? meta.goose.toolCall : undefined
    if (!isObject(tool) || tool.toolName !== GOOSE_SUBAGENT.Tool || tool.extensionName !== GOOSE_SUBAGENT.Extension)
      throw new Error('The paired Goose call is not a native summon delegate.')
    if (!isObject(body.rawInput) || body.rawInput.instructions !== prompt)
      throw new Error('The paired Goose delegate has another child prompt.')
    found = true
  }
  if (!found)
    throw new Error('The exact Goose delegate has no native parent receipt.')
  return spawnCallId
}

export async function readGooseChildTaskId(context: ManagedNativeScenarioContext, parentId: string, spawnCallId: string, prompt: string): Promise<string> {
  return gooseChildTaskId(await readNativeMessageSnapshot(context, parentId), spawnCallId, prompt)
}
