import { expect } from '@playwright/test'
import { AgentGoalStatus, AgentProvider, ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { exerciseNativeGoalPauseAndResume } from '../helpers/nativeGoalLifecycle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

// Qwen Code changes its goal through its goal control
// (`qwen/control/session/goal/control`), which pauses the goal at once and
// cancels the running round. Qwen starts each goal round the moment the round
// before it ends, so a `/goal pause` prompt that waited for an idle agent
// reached Qwen only after the goal paused itself.
qwenTest('pauses through the native goal control and resumes new native goal work', async ({ authenticatedQwenWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  await exerciseNativeGoalPauseAndResume(context, {
    pauseTiming: 'at-once',
    pausedProof: async () => {
      const agent = await currentNativeAgent(context)
      const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
      // Qwen states a reason for each pause of its own ("Three Goal turns in a
      // row recorded nothing to judge ..."). The reader's pause states none.
      await expect.poll(async () => {
        const response = await channel.callWorker(leapmuxServer.workerId, 'ListAgentMessages', ListAgentMessagesRequestSchema, ListAgentMessagesResponseSchema, { agentId: agent.id, limit: 1 })
        return { status: response.goal?.status, statusDetail: response.goal?.statusDetail }
      }).toEqual({ status: AgentGoalStatus.PAUSED, statusDetail: '' })
    },
  })
})
