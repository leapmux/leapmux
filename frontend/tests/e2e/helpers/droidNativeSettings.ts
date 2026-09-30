import type { DroidNativeSettingsUpdate } from './droidSettingsFrame'
import { ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { decompressContentToString } from '../../../src/lib/decompress'
import { getTestChannel } from './api'
import { parseDroidNativeSettingsUpdates } from './droidSettingsFrame'
import { listAgentsViaAPI } from './worktree'

interface DroidSettingsServer {
  hubUrl: string
  adminToken: string
  workerId: string
}

/** Read settings that the installed Droid actually reported to the Worker. */
export async function droidNativeSettingsUpdates(server: DroidSettingsServer, workspaceId: string): Promise<DroidNativeSettingsUpdate[]> {
  const agents = await listAgentsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
  const agent = agents[0]
  if (!agent)
    return []
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(server.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, {
    agentId: agent.id,
    limit: 200,
  })
  return response.messages.flatMap((message) => {
    const raw = decompressContentToString(message.content, message.contentCompression)
    if (!raw)
      return []
    return parseDroidNativeSettingsUpdates(raw)
  })
}
