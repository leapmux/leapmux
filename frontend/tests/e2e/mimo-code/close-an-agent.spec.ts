import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest('closes the UI tab and waits for owned process exit and Worker close', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseCloseAgent(context)
})
