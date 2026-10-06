import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseCompactPreviewRefusal, exerciseCompactRefusal } from './compactionScenarios'

fastAgentTest.describe('native manual compaction', () => {
  fastAgentTest('refuses the compact command on its ACP path', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await exerciseCompactPreviewRefusal({ page, modelScript, provider: AgentProvider.FAST_AGENT })
  })

  fastAgentTest('refuses the exact compact command without a completed boundary', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await exerciseCompactRefusal({ page, modelScript, provider: AgentProvider.FAST_AGENT })
  })
})
