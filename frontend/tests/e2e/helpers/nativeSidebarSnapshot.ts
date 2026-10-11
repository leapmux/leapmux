import type { ListAgentMessagesResponse } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from './api'
import { currentNativeAgent } from './nativeScenario'

export type NativeSidebarContext = Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>

/** Refuse an incomplete read rather than treating an unavailable snapshot as an empty list. */
export function requireNativeSidebarSnapshot(response: ListAgentMessagesResponse): ListAgentMessagesResponse {
  if (!response.todosLoaded || !response.backgroundTasksLoaded)
    throw new Error('The Worker did not load the native task and to-do snapshots.')
  return response
}

/** Read authoritative task and to-do snapshots from the native session's Worker. */
export async function readNativeSidebarSnapshot(context: NativeSidebarContext, agentId?: string): Promise<ListAgentMessagesResponse> {
  const id = agentId ?? (await currentNativeAgent(context)).id
  if (!id)
    throw new Error('The native sidebar snapshot requires an agent ID.')
  const server = context.leapmuxServer
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(server.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId: id, limit: 1 })
  return requireNativeSidebarSnapshot(response)
}
