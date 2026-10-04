import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest('applies native Bypass before and after reload without a permission prompt', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  await exerciseBypassPermissions({ page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD })
})
