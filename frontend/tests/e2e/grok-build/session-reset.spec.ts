import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest('clears the native context while the saved transcript stays visible', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  await exerciseSessionReset(context)
})
