import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseModelError } from '../helpers/nativeModelError'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest('shows the native model failure and accepts the next valid prompt', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  await exerciseModelError(context)
})
