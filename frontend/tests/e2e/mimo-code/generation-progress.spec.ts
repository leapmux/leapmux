import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest('proves the live native generation counter', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
