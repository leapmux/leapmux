import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('reopens the native picker handle and restores the saved transcript', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await exerciseSessionResume(context)
})
