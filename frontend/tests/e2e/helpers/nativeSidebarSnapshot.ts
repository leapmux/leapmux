import type { AgentGoal, BackgroundTaskItem, ListAgentMessagesResponse, TodoItem } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { WorkerAgentService } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { currentNativeAgent } from './nativeScenario'

export type NativeSidebarContext = Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>

/** One agent's authoritative to-do list, background-task registry and goal. */
export interface NativeSidebarSnapshot {
  todos: TodoItem[]
  backgroundTasks: BackgroundTaskItem[]
  /** The goal, or undefined when the agent has none. */
  goal: AgentGoal | undefined
}

/**
 * Refuse an incomplete read rather than treating an unavailable snapshot as an empty list. The Worker omits a snapshot
 * it could not read, so an absent one fails here.
 */
export function requireNativeSidebarSnapshot(response: ListAgentMessagesResponse): NativeSidebarSnapshot {
  if (!response.todos || !response.backgroundTasks || !response.goal)
    throw new Error('The Worker did not load the native task, to-do and goal snapshots.')
  return { todos: response.todos.todos, backgroundTasks: response.backgroundTasks.tasks, goal: response.goal.goal }
}

/** Read authoritative task and to-do snapshots from the native session's Worker. */
export async function readNativeSidebarSnapshot(context: NativeSidebarContext, agentId?: string): Promise<NativeSidebarSnapshot> {
  const id = agentId ?? (await currentNativeAgent(context)).id
  if (!id)
    throw new Error('The native sidebar snapshot requires an agent ID.')
  const server = context.leapmuxServer
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(server.workerId, WorkerAgentService.method.listAgentMessages, { agentId: id, limit: 1 })
  return requireNativeSidebarSnapshot(response)
}
