import type { MockModelMatcher } from '../helpers/mockModelScript'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { COPILOT_EVENT, COPILOT_METHOD } from '../../../src/generated/contracts/copilot-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { escapeRegExp } from '../../../src/lib/regexp'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'

/**
 * Match the own turn of a Copilot child from the start of a line of its task.
 * Copilot puts a `<current_datetime>` block before each user turn, so the task starts a line, not the text.
 * Copilot returns a tool result as a `tool` message, so the last user text of a parent turn never holds the task.
 */
export function copilotChildTaskMatcher(task: string): MockModelMatcher {
  if (task.trim() === '')
    throw new Error('The native Copilot child matcher requires a nonempty task.')
  return { user: `(?:^|\\n)${escapeRegExp(task)}` }
}

/** Resolve the actual child start event that belongs to this exact parent task call. */
export function copilotChildTaskId(snapshot: NativeMessageSnapshot, spawnCallId: string): string {
  if (snapshot.agentSessionId.trim() === '')
    throw new Error('The native Copilot child identity requires a nonempty session ID.')
  if (!spawnCallId)
    throw new Error('The native Copilot child identity requires a spawn call ID.')
  const ids = new Set<string>()
  for (const message of snapshot.messages) {
    if (message.agentSessionId !== snapshot.agentSessionId)
      continue
    const body = nativeMessageBody(message)
    if (!isObject(body))
      throw new Error('The native Copilot parent frame must contain an object.')
    if (body.method !== COPILOT_METHOD.SessionEvent)
      continue
    const params = body.params
    if (!isObject(params))
      throw new Error('The native Copilot session event contains invalid parameters.')
    if (params.sessionId !== snapshot.agentSessionId)
      continue
    const event = params.event
    if (!isObject(event))
      throw new Error('The native Copilot session event contains no event object.')
    if (event.type !== COPILOT_EVENT.SubagentStarted)
      continue
    if (!isObject(event.data))
      throw new Error('The native Copilot child start contains no data object.')
    if (event.data.toolCallId !== spawnCallId)
      continue
    if (typeof event.agentId !== 'string' || event.agentId.trim() === '')
      throw new Error('The native Copilot child start contains no agent ID.')
    ids.add(event.agentId)
  }
  if (ids.size !== 1)
    throw new Error('The exact Copilot task call must identify one native child.')
  const id = [...ids][0]
  if (id === undefined)
    throw new Error('The exact Copilot task has no native child ID.')
  return id
}

export async function readCopilotChildTaskId(context: ManagedNativeScenarioContext, parentId: string, spawnCallId: string): Promise<string> {
  return copilotChildTaskId(await readNativeMessageSnapshot(context, parentId), spawnCallId)
}
