import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest('shows the native model failure and accepts the next valid prompt', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  await exerciseModelError(context)
})
