import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

ampTest('clears the native context while the saved transcript stays visible', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  await exerciseSessionReset(context)
})
