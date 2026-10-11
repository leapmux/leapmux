import type { MockModelMatcher } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { REASONIX_CAPABILITY_ACTION, REASONIX_CAPABILITY_PREFIX, REASONIX_TOOL } from '../../../src/generated/contracts/reasonix-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'

/** Match the unique current task inside Reasonix's native child context pack. */
export function reasonixChildTaskMatcher(marker: string): MockModelMatcher {
  if (marker.trim() === '')
    throw new Error('The native Reasonix child matcher requires a nonempty task marker.')
  return { user: `^<subagent-context event="SubagentStart">[\\s\\S]*?</subagent-context>\\n[\\s\\S]*?\\n## Task\\n${escapeRegExp(marker)}(?=[:\\s]|$)` }
}

/** Resolve the exact native read-only task receipt without guessing its display title. */
export function reasonixChildTaskId(snapshot: NativeMessageSnapshot, spawnCallId: string, prompt: string): string {
  if (snapshot.agentSessionId.trim() === '')
    throw new Error('The native Reasonix child identity requires a nonempty session ID.')
  if (!spawnCallId || prompt.trim() === '')
    throw new Error('The native Reasonix child identity requires a call ID and prompt.')
  let found = false
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId)
      continue
    const body = nativeMessageBody(message)
    if (!isObject(body))
      throw new Error('The native Reasonix parent frame must contain an object.')
    if (body.sessionUpdate !== 'tool_call' || body.toolCallId !== spawnCallId)
      continue
    let title = body.title
    let input = body.rawInput
    if (title === REASONIX_TOOL.UseCapability && isObject(input) && input.action === REASONIX_CAPABILITY_ACTION.Call
      && typeof input.capability_id === 'string' && input.capability_id.startsWith(REASONIX_CAPABILITY_PREFIX.Tool)) {
      title = input.capability_id.slice(REASONIX_CAPABILITY_PREFIX.Tool.length)
      input = input.arguments
    }
    if (title !== REASONIX_TOOL.ReadOnlyTask || !isObject(input) || input.prompt !== prompt)
      throw new Error('The paired Reasonix call has another tool or child prompt.')
    found = true
  }
  if (!found)
    throw new Error('The exact Reasonix task has no native parent receipt.')
  return spawnCallId
}

export async function readReasonixChildTaskId(context: ManagedNativeScenarioContext, parentId: string, spawnCallId: string, prompt: string): Promise<string> {
  return reasonixChildTaskId(await readNativeMessageSnapshot(context, parentId), spawnCallId, prompt)
}
