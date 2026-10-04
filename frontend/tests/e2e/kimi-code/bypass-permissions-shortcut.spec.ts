import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest('applies native Bypass before and after reload without a permission prompt', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  await exerciseBypassPermissions({ page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE })
})
