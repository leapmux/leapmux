import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest('closes the UI tab and waits for owned process exit and Worker close', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await exerciseCloseAgent(context)
})
