import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelMatcher } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { AgentProvider, AgentStatus, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { escapeRegExp } from '../../../src/lib/regexp'
import { sameAgentOwnership } from '../helpers/nativeMessages'
import { nativeAgentById, nativeAgentsByIds } from '../helpers/nativeScenario'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { retryUntilPass } from '../helpers/retryUntilPass'

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

function validIdentity(value: unknown): value is string {
  return typeof value === 'string' && value.trim() !== ''
}

function requireCopilotParent(parent: AgentInfo | null | undefined): AgentInfo {
  if (!parent || parent.status !== AgentStatus.ACTIVE || parent.agentProvider !== AgentProvider.GITHUB_COPILOT
    || ![parent.id, parent.rootAgentId, parent.workerId, parent.agentSessionId].every(validIdentity)) {
    throw new Error('The Copilot child identity requires an active parent with native session and owner IDs.')
  }
  return parent
}

/** Resolve the exact spawning call through the child links that the Worker retains. */
export function copilotChildTaskId(parent: AgentInfo, children: readonly AgentInfo[], spawnCallId: string): string {
  requireCopilotParent(parent)
  if (!validIdentity(spawnCallId))
    throw new Error('The native Copilot child identity requires a spawn call ID.')
  const ids = new Map<string, string>()
  for (const child of children) {
    if (child.parentAgentId !== parent.id || child.rootAgentId !== parent.rootAgentId || child.workerId !== parent.workerId
      || child.agentProvider !== parent.agentProvider || child.status !== AgentStatus.ACTIVE || child.spawnSpanId !== spawnCallId) {
      continue
    }
    if (!validIdentity(child.id) || !validIdentity(child.providerChildKey))
      throw new Error('The matching Copilot child link has no native child ID or Worker identity.')
    const previous = ids.get(child.id)
    if (previous !== undefined && previous !== child.providerChildKey)
      throw new Error('The Copilot child links contain conflicting native identities.')
    ids.set(child.id, child.providerChildKey)
  }
  if (ids.size !== 1)
    throw new Error('The exact Copilot task call must identify one native child.')
  const id = ids.values().next().value
  if (id === undefined)
    throw new Error('The exact Copilot task has no native child ID.')
  return id
}

export async function readCopilotChildTaskId(context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>, parentId: string, spawnCallId: string): Promise<string> {
  if (!validIdentity(parentId) || !validIdentity(spawnCallId))
    throw new Error('The Copilot child lookup requires parent and spawn call IDs.')
  const owner = requireCopilotParent(await nativeAgentById(context, parentId))
  if (owner.id !== parentId || owner.workerId !== context.leapmuxServer.workerId)
    throw new Error('The Copilot child lookup received another parent or Worker.')
  return retryUntilPass(async () => {
    const snapshot = await readNativeSidebarSnapshot(context, parentId)
    const ids = new Set(snapshot.backgroundTasks
      .filter(task => task.kind === BackgroundTaskKind.SUBAGENT && task.status === BackgroundTaskStatus.RUNNING
        && validIdentity(task.childAgentId)
        && (task.parentAgentId === parentId || (task.parentAgentId === '' && owner.id === owner.rootAgentId)))
      .map(task => task.childAgentId))
    if (ids.size === 0)
      throw new Error('The Worker registered no running Copilot child.')
    const children = (await nativeAgentsByIds(context, [...ids])).filter(child => ids.has(child.id))
    const current = await nativeAgentById(context, parentId)
    if (!current || current.status !== AgentStatus.ACTIVE || !sameAgentOwnership(owner, current))
      throw new Error('The Copilot parent ownership changed during the child lookup.')
    return copilotChildTaskId(owner, children, spawnCallId)
  })
}
