import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { kiroTest } from '../kiro-fixtures'

kiroTest('clears the native context while the saved transcript stays visible', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await exerciseSessionReset(context)
})
