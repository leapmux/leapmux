import { expect } from '@playwright/test'
import { AgentProvider, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { exerciseNativeGoalPauseAndResume } from '../helpers/nativeGoalLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('pauses through the native user command and resumes new native goal work', async ({ authenticatedQwenWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await exerciseNativeGoalPauseAndResume(context, { pausedProof: async () => {
    const agent = await currentNativeAgent(context)
    const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    await expect.poll(async () => {
      const response = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId: agent.id, limit: 1 })
      return response.goal?.statusDetail
    }).toContain('Paused with /goal pause.')
  } })
})
